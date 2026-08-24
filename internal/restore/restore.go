package restore

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type RootIdentity struct {
	Path  string `json:"path"`
	Token string `json:"token"`
}

type Entry struct {
	RelativePath     string `json:"relative_path"`
	SourcePath       string `json:"source_path"`
	SourceDigest     string `json:"source_digest"`
	CiphertextDigest string `json:"ciphertext_digest,omitempty"`
	Size             int64  `json:"size"`
}

type Plan struct {
	Root         string       `json:"root"`
	RootIdentity RootIdentity `json:"root_identity"`
	Entries      []Entry      `json:"entries"`
	Conflicts    []string     `json:"conflicts"`
}

func CaptureRootIdentity(root string) (RootIdentity, error) {
	clean, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return RootIdentity{}, fmt.Errorf("restore root path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return RootIdentity{}, fmt.Errorf("resolve restore root: %w", err)
	}
	clean = filepath.Clean(resolved)
	if err := ensureSafeRoot(clean); err != nil {
		return RootIdentity{}, err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return RootIdentity{}, fmt.Errorf("restore root identity: %w", err)
	}
	return RootIdentity{Path: clean, Token: stableIdentityToken(info)}, nil
}

func (id RootIdentity) Validate(root string) error {
	current, err := CaptureRootIdentity(root)
	if err != nil {
		return err
	}
	if id.Path == "" || id.Token == "" || current.Path != id.Path || current.Token != id.Token {
		return fmt.Errorf("restore root identity drifted")
	}
	return nil
}

func stableIdentityToken(info os.FileInfo) string {
	value := reflect.ValueOf(info.Sys())
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return ""
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return ""
	}
	parts := make([]string, 0, 6)
	for _, field := range []string{"Dev", "Ino", "VolumeSerialNumber", "FileIndexHigh", "FileIndexLow", "CreationTime", "LowDateTime", "HighDateTime"} {
		member := value.FieldByName(field)
		if member.IsValid() && member.CanInterface() {
			parts = append(parts, field+"="+fmt.Sprint(member.Interface()))
		}
	}
	return strings.Join(parts, "|")
}

func BuildPlan(root string, entries []Entry) (Plan, error) {
	if !filepath.IsAbs(root) {
		return Plan{}, fmt.Errorf("restore root must be absolute")
	}
	cleanRoot := filepath.Clean(root)
	identity, err := CaptureRootIdentity(cleanRoot)
	if err != nil {
		return Plan{}, err
	}
	cleanRoot = identity.Path
	plan := Plan{Root: cleanRoot, RootIdentity: identity, Entries: make([]Entry, 0, len(entries)), Conflicts: make([]string, 0)}
	seen := make(map[string]struct{}, len(entries))
	for i, entry := range entries {
		clean, err := cleanRelativePath(entry.RelativePath)
		if err != nil {
			return Plan{}, fmt.Errorf("entry %d: %w", i, err)
		}
		if _, exists := seen[clean]; exists {
			return Plan{}, fmt.Errorf("duplicate restore destination %q", clean)
		}
		seen[clean] = struct{}{}
		entry.RelativePath = clean
		destination := filepath.Join(cleanRoot, filepath.FromSlash(clean))
		if _, err := os.Lstat(destination); err == nil {
			plan.Conflicts = append(plan.Conflicts, clean)
		} else if !os.IsNotExist(err) {
			return Plan{}, fmt.Errorf("entry %q: inspect destination: %w", clean, err)
		}
		plan.Entries = append(plan.Entries, entry)
	}
	return plan, nil
}

func cleanRelativePath(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return "", fmt.Errorf("path must be relative and cannot contain drive or alternate-stream syntax")
	}
	parts := strings.Split(value, "/")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", fmt.Errorf("path traversal is forbidden")
		}
		upper := strings.ToUpper(strings.TrimSuffix(part, "."))
		switch upper {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return "", fmt.Errorf("device name %q is forbidden", part)
		}
		clean = append(clean, part)
	}
	if len(clean) == 0 {
		return "", fmt.Errorf("path resolves to restore root")
	}
	return strings.Join(clean, "/"), nil
}

func StageFile(plan Plan, entry Entry) error {
	clean, err := cleanRelativePath(entry.RelativePath)
	if err != nil {
		return err
	}
	if clean != entry.RelativePath {
		return fmt.Errorf("entry path is not canonical")
	}
	if plan.RootIdentity.Path != "" {
		if err := plan.RootIdentity.Validate(plan.Root); err != nil {
			return err
		}
	} else if err := ensureSafeRoot(plan.Root); err != nil {
		return err
	}
	sourceInfo, err := os.Lstat(entry.SourcePath)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.Mode().IsRegular() {
		return fmt.Errorf("source must be a regular non-symlink file")
	}
	if entry.Size < 0 || sourceInfo.Size() != entry.Size {
		return fmt.Errorf("source size mismatch")
	}
	parent, err := safeParent(plan.Root, clean)
	if err != nil {
		return err
	}
	if plan.RootIdentity.Path != "" {
		if err := plan.RootIdentity.Validate(plan.Root); err != nil {
			return err
		}
	}
	destination := filepath.Join(plan.Root, filepath.FromSlash(clean))
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("restore destination %q already exists", clean)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect restore destination: %w", err)
	}
	source, err := os.Open(entry.SourcePath)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer source.Close()
	tmp, err := os.CreateTemp(parent, ".restore-*.tmp")
	if err != nil {
		return fmt.Errorf("create staging file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	defer cleanup()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), source); err != nil {
		return fmt.Errorf("stage restore file: %w", err)
	}
	if got := "sha256:" + hex.EncodeToString(hash.Sum(nil)); got != entry.SourceDigest {
		return fmt.Errorf("source digest mismatch: got %s", got)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync restore file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close restore file: %w", err)
	}
	if plan.RootIdentity.Path != "" {
		if err := plan.RootIdentity.Validate(plan.Root); err != nil {
			return err
		}
	}
	if _, exists, err := safeExistingParent(plan.Root, clean); err != nil {
		return err
	} else if !exists {
		return fmt.Errorf("restore ancestor disappeared")
	}
	if err := os.Link(tmpPath, destination); err != nil {
		return fmt.Errorf("commit restore file without overwrite: %w", err)
	}
	if plan.RootIdentity.Path != "" {
		if err := plan.RootIdentity.Validate(plan.Root); err != nil {
			return fmt.Errorf("restore root changed after commit: %w", err)
		}
	}
	return nil
}

func commitNoReplace(source, destination string) error {
	if err := os.Link(source, destination); err == nil {
		return nil
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := error(nil)
	if copyErr == nil {
		syncErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return copyErr
	}
	if syncErr != nil {
		_ = os.Remove(destination)
		return syncErr
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return closeErr
	}
	return nil
}

func ensureSafeRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("restore root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("restore root must be a regular directory")
	}
	return nil
}

func isSafeDirectoryPath(path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	cleanPath := filepath.Clean(path)
	cleanResolved := filepath.Clean(resolved)
	return cleanPath == cleanResolved || strings.EqualFold(cleanPath, cleanResolved)
}

func safeParent(root, relative string) (string, error) {
	parts := strings.Split(relative, "/")
	parent := root
	for _, part := range parts[:len(parts)-1] {
		parent = filepath.Join(parent, part)
		info, err := os.Lstat(parent)
		if os.IsNotExist(err) {
			if err := os.Mkdir(parent, 0o700); err != nil {
				return "", fmt.Errorf("create restore directory: %w", err)
			}
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !isSafeDirectoryPath(parent) {
			return "", fmt.Errorf("restore ancestor is not a safe directory")
		}
	}
	return parent, nil
}

type Journal struct {
	Path          string
	TransactionID string
	RootIdentity  RootIdentity
}

type JournalRecord struct {
	TransactionID    string `json:"transaction_id"`
	Entry            string `json:"entry"`
	Action           string `json:"action"`
	Before           string `json:"before"`
	After            string `json:"after"`
	SourceDigest     string `json:"source_digest"`
	CiphertextDigest string `json:"ciphertext_digest,omitempty"`
	Root             string `json:"root,omitempty"`
	RootIdentity     string `json:"root_identity,omitempty"`
}

type Transaction struct {
	ID           string
	Root         string
	RootIdentity RootIdentity
	Journal      Journal
}

func NewTransactionID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generate restore transaction id: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

func BeginTransaction(root, id string) (*Transaction, error) {
	if err := validateTransactionID(id); err != nil {
		return nil, err
	}
	identity, err := CaptureRootIdentity(root)
	if err != nil {
		return nil, err
	}
	path := TransactionJournalPath(identity.Path, id)
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("restore transaction %q already exists", id)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect restore transaction: %w", err)
	}
	return &Transaction{ID: id, Root: identity.Path, RootIdentity: identity, Journal: Journal{Path: path, TransactionID: id, RootIdentity: identity}}, nil
}

func OpenTransaction(root, id string) (*Transaction, error) {
	if err := validateTransactionID(id); err != nil {
		return nil, err
	}
	identity, err := CaptureRootIdentity(root)
	if err != nil {
		return nil, err
	}
	path := TransactionJournalPath(identity.Path, id)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		legacy := filepath.Join(identity.Path, ".restore-apply.jsonl")
		if _, legacyErr := os.Lstat(legacy); legacyErr != nil {
			return nil, fmt.Errorf("restore transaction %q journal not found", id)
		}
		path = legacy
	} else if err != nil {
		return nil, fmt.Errorf("inspect restore transaction: %w", err)
	}
	return &Transaction{ID: id, Root: identity.Path, RootIdentity: identity, Journal: Journal{Path: path, TransactionID: id, RootIdentity: identity}}, nil
}

func TransactionJournalPath(root, id string) string {
	return filepath.Join(filepath.Clean(root), ".restore-transactions", id+".jsonl")
}

func (tx *Transaction) Append(record JournalRecord) error {
	if tx == nil {
		return fmt.Errorf("restore transaction is nil")
	}
	if err := tx.RootIdentity.Validate(tx.Root); err != nil {
		return err
	}
	record.TransactionID = tx.ID
	record.Root = tx.Root
	record.RootIdentity = tx.RootIdentity.Token
	return tx.Journal.Append(record)
}

func (tx *Transaction) Read() ([]JournalRecord, error) {
	if tx == nil {
		return nil, fmt.Errorf("restore transaction is nil")
	}
	if err := tx.RootIdentity.Validate(tx.Root); err != nil {
		return nil, err
	}
	records, err := tx.Journal.Read()
	if err != nil {
		return nil, err
	}
	if err := tx.Journal.ValidateRecovery(records); err != nil {
		return nil, err
	}
	for i, record := range records {
		if record.Root != "" {
			root, err := filepath.Abs(filepath.Clean(record.Root))
			if err != nil || root != tx.Root {
				return nil, fmt.Errorf("transaction %q root mismatch at record %d", tx.ID, i)
			}
		}
		if record.RootIdentity != "" && record.RootIdentity != tx.RootIdentity.Token {
			return nil, fmt.Errorf("transaction %q root identity drifted at record %d", tx.ID, i)
		}
	}
	return records, nil
}

func (j Journal) Append(record JournalRecord) error {
	if strings.TrimSpace(j.Path) == "" || strings.TrimSpace(record.Entry) == "" || strings.TrimSpace(record.Action) == "" {
		return fmt.Errorf("journal path, entry, and action are required")
	}
	if record.Action != "create" {
		return fmt.Errorf("replace and other journal actions are disabled")
	}
	if j.TransactionID != "" {
		if record.TransactionID == "" {
			record.TransactionID = j.TransactionID
		}
		if record.TransactionID != j.TransactionID {
			return fmt.Errorf("journal transaction mismatch")
		}
	}
	if j.RootIdentity.Token != "" {
		if record.RootIdentity == "" {
			record.RootIdentity = j.RootIdentity.Token
		}
		if record.RootIdentity != j.RootIdentity.Token {
			return fmt.Errorf("journal root identity mismatch")
		}
	}
	if err := os.MkdirAll(filepath.Dir(j.Path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(j.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(record)
	syncErr := error(nil)
	if encodeErr == nil {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	if syncErr != nil {
		return fmt.Errorf("sync restore journal: %w", syncErr)
	}
	if closeErr != nil {
		return closeErr
	}
	syncDirectory(filepath.Dir(j.Path))
	return nil
}

func (j Journal) Read() ([]JournalRecord, error) {
	file, err := os.Open(j.Path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var records []JournalRecord
	line := 0
	for scanner.Scan() {
		line++
		var record JournalRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("journal line %d: %w", line, err)
		}
		if j.TransactionID != "" && record.TransactionID != j.TransactionID {
			continue
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if j.TransactionID != "" && len(records) == 0 {
		return nil, fmt.Errorf("transaction %q has no journal records", j.TransactionID)
	}
	return records, nil
}

func (j Journal) ValidateRecovery(records []JournalRecord) error {
	if len(records) == 0 {
		return fmt.Errorf("restore transaction has no journal records")
	}
	var transactionID string
	for i, record := range records {
		if record.TransactionID == "" || record.Before == "" || record.After == "" || record.SourceDigest == "" {
			return fmt.Errorf("journal record %d lacks recovery fields", i)
		}
		if transactionID == "" {
			transactionID = record.TransactionID
		} else if transactionID != record.TransactionID {
			return fmt.Errorf("journal records contain multiple transactions")
		}
		if record.Action != "create" || record.Before != "absent" || (record.After != "created" && record.After != "staged") {
			return fmt.Errorf("journal record %d is not a create-only recovery marker", i)
		}
	}
	if j.TransactionID != "" && transactionID != j.TransactionID {
		return fmt.Errorf("journal transaction mismatch")
	}
	return nil
}

func syncDirectory(path string) {
	directory, err := os.Open(path)
	if err != nil {
		return
	}
	_ = directory.Sync()
	_ = directory.Close()
}

func validateTransactionID(id string) error {
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, `/\:`) {
		return fmt.Errorf("restore transaction id must be a safe non-empty name")
	}
	return nil
}

func safeExistingParent(root, relative string) (string, bool, error) {
	parts := strings.Split(relative, "/")
	parent := root
	for _, part := range parts[:len(parts)-1] {
		parent = filepath.Join(parent, part)
		info, err := os.Lstat(parent)
		if os.IsNotExist(err) {
			return parent, false, nil
		}
		if err != nil {
			return "", false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !isSafeDirectoryPath(parent) {
			return "", false, fmt.Errorf("restore ancestor is not a safe directory")
		}
	}
	return parent, true, nil
}

// RecoverTransaction removes only files recorded by one explicit transaction.
func RecoverTransaction(root, transactionID string) error {
	tx, err := OpenTransaction(root, transactionID)
	if err != nil {
		return err
	}
	records, err := tx.Read()
	if err != nil {
		return err
	}
	return RecoverCreateOnlyWithIdentity(tx.Root, tx.RootIdentity, records)
}

// RecoverCreateOnly removes only files that a journal proves were newly
// created and whose current content still matches the recorded digest.
// Replace actions are intentionally unsupported.
func RecoverCreateOnly(root string, records []JournalRecord) error {
	identity, err := CaptureRootIdentity(root)
	if err != nil {
		return err
	}
	return RecoverCreateOnlyWithIdentity(identity.Path, identity, records)
}

func RecoverCreateOnlyWithIdentity(root string, identity RootIdentity, records []JournalRecord) error {
	if len(records) == 0 {
		return fmt.Errorf("restore transaction has no journal records")
	}
	if err := identity.Validate(root); err != nil {
		return err
	}
	journal := Journal{TransactionID: records[0].TransactionID}
	if err := journal.ValidateRecovery(records); err != nil {
		return err
	}
	for i := len(records) - 1; i >= 0; i-- {
		record := records[i]
		if record.Root != "" {
			recordRoot, err := filepath.Abs(filepath.Clean(record.Root))
			if err != nil || recordRoot != identity.Path {
				return fmt.Errorf("journal record %d root mismatch", i)
			}
		}
		if record.RootIdentity != "" && record.RootIdentity != identity.Token {
			return fmt.Errorf("journal record %d root identity drifted", i)
		}
		if err := identity.Validate(root); err != nil {
			return err
		}
		clean, err := cleanRelativePath(record.Entry)
		if err != nil {
			return fmt.Errorf("journal record %d: %w", i, err)
		}
		parent, exists, err := safeExistingParent(root, clean)
		if err != nil {
			return fmt.Errorf("journal record %d: %w", i, err)
		}
		if !exists {
			continue
		}
		destination := filepath.Join(parent, filepath.Base(filepath.FromSlash(clean)))
		info, err := os.Lstat(destination)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect recovery destination %q: %w", clean, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("recovery destination %q is not a regular file", clean)
		}
		file, err := os.Open(destination)
		if err != nil {
			return fmt.Errorf("open recovery destination %q: %w", clean, err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("hash recovery destination %q: %w", clean, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close recovery destination %q: %w", clean, closeErr)
		}
		got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if got != record.SourceDigest {
			return fmt.Errorf("recovery destination %q digest mismatch", clean)
		}
		if err := identity.Validate(root); err != nil {
			return err
		}
		if err := os.Remove(destination); err != nil {
			return fmt.Errorf("remove recovered destination %q: %w", clean, err)
		}
	}
	return identity.Validate(root)
}
