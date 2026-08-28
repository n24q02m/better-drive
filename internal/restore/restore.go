package restore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/n24q02m/better-drive/internal/artifactcrypto"
)

type RootIdentity struct {
	Path  string `json:"path"`
	Token string `json:"token"`
}

type Entry struct {
	RelativePath     string                  `json:"relative_path"`
	SourcePath       string                  `json:"source_path,omitempty"`
	SourceReference  *SourceReference        `json:"source_reference,omitempty"`
	ArtifactMetadata artifactcrypto.Metadata `json:"artifact_metadata"`
	PlaintextDigest  string                  `json:"plaintext_digest"`
	CiphertextDigest string                  `json:"ciphertext_digest"`
	PlaintextSize    int64                   `json:"plaintext_size"`
}

type Plan struct {
	Root          string       `json:"root"`
	RootIdentity  RootIdentity `json:"root_identity"`
	Entries       []Entry      `json:"entries"`
	Conflicts     []string     `json:"conflicts"`
	CapacityBytes int64        `json:"capacity_bytes"`
	TotalObjects  int          `json:"total_objects"`
}

func CaptureRootIdentity(root string) (RootIdentity, error) {
	clean, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return RootIdentity{}, fmt.Errorf("restore root path: %w", err)
	}
	if err := ensureNoSymlinkComponents(clean); err != nil {
		return RootIdentity{}, err
	}
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
	plan := Plan{
		Root:         cleanRoot,
		RootIdentity: identity,
		Entries:      make([]Entry, 0, len(entries)),
		Conflicts:    make([]string, 0),
		TotalObjects: len(entries),
	}
	var totalCapacity int64
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
		if entryIsExecutable(entry) {
			if err := validateExecutableEntry(entry); err != nil {
				return Plan{}, fmt.Errorf("entry %d: %w", i, err)
			}
		}
		destination := filepath.Join(cleanRoot, filepath.FromSlash(clean))
		if _, err := os.Lstat(destination); err == nil {
			plan.Conflicts = append(plan.Conflicts, clean)
		} else if !os.IsNotExist(err) {
			return Plan{}, fmt.Errorf("entry %q: inspect destination: %w", clean, err)
		}
		totalCapacity += entry.PlaintextSize
		plan.Entries = append(plan.Entries, entry)
	}
	plan.CapacityBytes = totalCapacity
	return plan, nil
}

func cleanRelativePath(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return "", fmt.Errorf("path must be relative and cannot contain drive or alternate-stream syntax")
	}
	if strings.Contains(value, "#") {
		return "", fmt.Errorf("path cannot contain archive traversal or fragment syntax (#)")
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

func entryIsExecutable(entry Entry) bool {
	return strings.TrimSpace(entry.SourcePath) != "" ||
		entry.SourceReference != nil ||
		strings.TrimSpace(entry.PlaintextDigest) != "" ||
		strings.TrimSpace(entry.CiphertextDigest) != "" ||
		entry.PlaintextSize != 0 ||
		entry.ArtifactMetadata != (artifactcrypto.Metadata{})
}

func validateExecutableEntry(entry Entry) error {
	if strings.TrimSpace(entry.SourcePath) == "" && entry.SourceReference == nil {
		return fmt.Errorf("executable entry requires source_path or source_reference")
	}
	if entry.SourceReference != nil {
		if err := entry.SourceReference.Validate(); err != nil {
			return fmt.Errorf("invalid source_reference: %w", err)
		}
		if entry.SourceReference.CiphertextDigest != entry.CiphertextDigest {
			return fmt.Errorf("source_reference ciphertext digest mismatch with entry")
		}
	}
	if err := validateSHA256Digest("plaintext_digest", entry.PlaintextDigest); err != nil {
		return err
	}
	if err := validateSHA256Digest("ciphertext_digest", entry.CiphertextDigest); err != nil {
		return err
	}
	if entry.PlaintextSize < 0 {
		return fmt.Errorf("plaintext_size must be non-negative")
	}
	if strings.TrimSpace(entry.ArtifactMetadata.RestoreSetID) == "" ||
		strings.TrimSpace(entry.ArtifactMetadata.Component) == "" ||
		strings.TrimSpace(entry.ArtifactMetadata.KeyRef) == "" ||
		entry.ArtifactMetadata.KeyVersion == 0 {
		return fmt.Errorf("artifact_metadata requires restore_set_id, component, key_ref, and key_version")
	}
	return nil
}

func validateSHA256Digest(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return fmt.Errorf("%s must be a sha256 digest", field)
	}
	if _, err := hex.DecodeString(value[len("sha256:"):]); err != nil {
		return fmt.Errorf("%s must be a sha256 digest", field)
	}
	return nil
}

type sanitizedArtifactResolver struct {
	resolver artifactcrypto.Resolver
}

func (r sanitizedArtifactResolver) Resolve(reference artifactcrypto.KeyReference) ([]byte, error) {
	if r.resolver == nil {
		return nil, errors.New("artifact key resolver is required")
	}
	key, err := r.resolver.Resolve(reference)
	if err != nil {
		return nil, errors.New("artifact key resolution failed")
	}
	return key, nil
}

// StageFile streams and verifies a sealed artifact from its source into the isolated staging destination.
// If entry.SourceReference is present, it uses the injected provider to stream the ciphertext directly.
func StageFile(plan Plan, entry Entry, resolver artifactcrypto.Resolver, verifier StagingVerifier) error {
	return StageFileWithProvider(context.Background(), plan, entry, resolver, verifier, nil)
}

// StageFileWithProvider streams and verifies a sealed artifact using either a typed provider or local source file.
func StageFileWithProvider(ctx context.Context, plan Plan, entry Entry, resolver artifactcrypto.Resolver, verifier StagingVerifier, provider SourceProvider) error {
	if resolver == nil {
		return fmt.Errorf("artifact resolver is required for restore execution")
	}
	if verifier == nil {
		return fmt.Errorf("staging verifier is required for restore execution")
	}
	if err := validateExecutableEntry(entry); err != nil {
		return err
	}
	clean, err := cleanRelativePath(entry.RelativePath)
	if err != nil {
		return err
	}
	if clean != entry.RelativePath {
		return fmt.Errorf("entry path is not canonical")
	}
	if plan.RootIdentity.Path == "" || plan.RootIdentity.Token == "" {
		return fmt.Errorf("restore root identity is required for restore execution")
	}
	evidence, err := VerifyStagingEvidence(plan.Root, plan.RootIdentity, verifier)
	if err != nil {
		return err
	}
	verifyStableEvidence := func() error {
		next, verifyErr := VerifyStagingEvidence(plan.Root, plan.RootIdentity, verifier)
		if verifyErr != nil {
			return verifyErr
		}
		if !evidence.Equivalent(next) {
			return fmt.Errorf("staging evidence drifted")
		}
		return nil
	}

	var sourceStream io.ReadCloser
	if entry.SourceReference != nil {
		if provider == nil {
			return fmt.Errorf("source provider is required for source_reference fetch")
		}
		stream, readback, err := provider.Open(ctx, *entry.SourceReference)
		if err != nil {
			return fmt.Errorf("open source provider artifact: %w", err)
		}
		if err := readback.Validate(*entry.SourceReference); err != nil {
			_ = stream.Close()
			return fmt.Errorf("source provider readback: %w", err)
		}
		sourceStream = stream
	} else if entry.SourcePath != "" {
		sourceInfo, err := os.Lstat(entry.SourcePath)
		if err != nil {
			return fmt.Errorf("source: %w", err)
		}
		if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.Mode().IsRegular() {
			return fmt.Errorf("source must be a regular non-symlink file")
		}
		source, err := os.Open(entry.SourcePath)
		if err != nil {
			return fmt.Errorf("open source: %w", err)
		}
		openedInfo, err := source.Stat()
		if err != nil {
			_ = source.Close()
			return fmt.Errorf("stat source: %w", err)
		}
		if !openedInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, openedInfo) {
			_ = source.Close()
			return fmt.Errorf("source must be a regular non-symlink file")
		}
		currentInfo, err := os.Lstat(entry.SourcePath)
		if err != nil {
			_ = source.Close()
			return fmt.Errorf("recheck source: %w", err)
		}
		if currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, currentInfo) {
			_ = source.Close()
			return fmt.Errorf("source must be a regular non-symlink file")
		}
		sourceStream = source
	} else {
		return fmt.Errorf("no source specified")
	}
	defer sourceStream.Close()

	destination := filepath.Join(plan.Root, filepath.FromSlash(clean))
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("restore destination %q already exists", clean)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect restore destination: %w", err)
	}
	if err := verifyStableEvidence(); err != nil {
		return err
	}
	parent, err := safeParent(plan.Root, clean)
	if err != nil {
		return err
	}
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
	cipherHash := sha256.New()
	if err := artifactcrypto.Open(tmp, io.TeeReader(sourceStream, cipherHash), sanitizedArtifactResolver{resolver}, entry.ArtifactMetadata); err != nil {
		return fmt.Errorf("open sealed restore artifact: %w", err)
	}
	if got := "sha256:" + hex.EncodeToString(cipherHash.Sum(nil)); got != entry.CiphertextDigest {
		return fmt.Errorf("ciphertext digest mismatch: got %s", got)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind staged restore file: %w", err)
	}
	plainHash := sha256.New()
	plainSize, err := io.Copy(plainHash, tmp)
	if err != nil {
		return fmt.Errorf("hash staged restore file: %w", err)
	}
	if plainSize != entry.PlaintextSize {
		return fmt.Errorf("plaintext size mismatch: got %d", plainSize)
	}
	if got := "sha256:" + hex.EncodeToString(plainHash.Sum(nil)); got != entry.PlaintextDigest {
		return fmt.Errorf("plaintext digest mismatch: got %s", got)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync restore file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close restore file: %w", err)
	}
	if _, exists, err := safeExistingParent(plan.Root, clean); err != nil {
		return err
	} else if !exists {
		return fmt.Errorf("restore ancestor disappeared")
	}
	if err := verifyStableEvidence(); err != nil {
		return err
	}
	if err := commitNoReplace(tmpPath, destination); err != nil {
		return fmt.Errorf("commit restore file without overwrite: %w", err)
	}
	if err := plan.RootIdentity.Validate(plan.Root); err != nil {
		return fmt.Errorf("restore root changed after commit: %w", err)
	}
	return nil
}

// StageAndReplaceFile applies a replace operation: writes temporary plaintext, verifies digests,
// snapshots current destination to rollback directory, and atomically renames.
func StageAndReplaceFile(ctx context.Context, plan Plan, entry Entry, resolver artifactcrypto.Resolver, verifier StagingVerifier, provider SourceProvider, rollbackDir string) (rollbackSnapshot string, err error) {
	if resolver == nil {
		return "", fmt.Errorf("artifact resolver is required for restore execution")
	}
	if verifier == nil {
		return "", fmt.Errorf("staging verifier is required for restore execution")
	}
	if err := validateExecutableEntry(entry); err != nil {
		return "", err
	}
	clean, err := cleanRelativePath(entry.RelativePath)
	if err != nil {
		return "", err
	}
	if err := plan.RootIdentity.Validate(plan.Root); err != nil {
		return "", err
	}
	destination := filepath.Join(plan.Root, filepath.FromSlash(clean))
	destInfo, err := os.Lstat(destination)
	if err != nil {
		return "", fmt.Errorf("replace requires existing destination: %w", err)
	}
	if destInfo.Mode()&os.ModeSymlink != 0 || !destInfo.Mode().IsRegular() {
		return "", fmt.Errorf("replace destination must be a regular non-symlink file")
	}

	// Prepare rollback snapshot path in owner-only rollback root.
	if err := os.MkdirAll(rollbackDir, 0o700); err != nil {
		return "", fmt.Errorf("create rollback directory: %w", err)
	}
	rollbackPath := filepath.Join(rollbackDir, strings.ReplaceAll(clean, "/", "_")+".bak")
	if err := copyFileNoFollow(destination, rollbackPath); err != nil {
		return "", fmt.Errorf("snapshot destination to rollback: %w", err)
	}

	parent, err := safeParent(plan.Root, clean)
	if err != nil {
		_ = os.Remove(rollbackPath)
		return "", err
	}
	tmp, err := os.CreateTemp(parent, ".restore-replace-*.tmp")
	if err != nil {
		_ = os.Remove(rollbackPath)
		return "", fmt.Errorf("create temp replace file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanupTmp := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	defer cleanupTmp()

	var sourceStream io.ReadCloser
	if entry.SourceReference != nil {
		if provider == nil {
			_ = os.Remove(rollbackPath)
			return "", fmt.Errorf("source provider is required")
		}
		stream, readback, err := provider.Open(ctx, *entry.SourceReference)
		if err != nil {
			_ = os.Remove(rollbackPath)
			return "", fmt.Errorf("open source provider artifact: %w", err)
		}
		if err := readback.Validate(*entry.SourceReference); err != nil {
			_ = stream.Close()
			_ = os.Remove(rollbackPath)
			return "", fmt.Errorf("source readback: %w", err)
		}
		sourceStream = stream
	} else {
		source, err := os.Open(entry.SourcePath)
		if err != nil {
			_ = os.Remove(rollbackPath)
			return "", fmt.Errorf("open source: %w", err)
		}
		sourceStream = source
	}
	defer sourceStream.Close()

	cipherHash := sha256.New()
	if err := artifactcrypto.Open(tmp, io.TeeReader(sourceStream, cipherHash), sanitizedArtifactResolver{resolver}, entry.ArtifactMetadata); err != nil {
		_ = os.Remove(rollbackPath)
		return "", fmt.Errorf("open sealed artifact: %w", err)
	}
	if got := "sha256:" + hex.EncodeToString(cipherHash.Sum(nil)); got != entry.CiphertextDigest {
		_ = os.Remove(rollbackPath)
		return "", fmt.Errorf("ciphertext digest mismatch: got %s", got)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = os.Remove(rollbackPath)
		return "", err
	}
	plainHash := sha256.New()
	plainSize, err := io.Copy(plainHash, tmp)
	if err != nil {
		_ = os.Remove(rollbackPath)
		return "", err
	}
	if plainSize != entry.PlaintextSize {
		_ = os.Remove(rollbackPath)
		return "", fmt.Errorf("plaintext size mismatch: got %d", plainSize)
	}
	if got := "sha256:" + hex.EncodeToString(plainHash.Sum(nil)); got != entry.PlaintextDigest {
		_ = os.Remove(rollbackPath)
		return "", fmt.Errorf("plaintext digest mismatch: got %s", got)
	}
	if err := tmp.Sync(); err != nil {
		_ = os.Remove(rollbackPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(rollbackPath)
		return "", err
	}

	// Recheck symlink ancestors immediately before rename.
	if err := ensureNoSymlinkComponents(destination); err != nil {
		_ = os.Remove(rollbackPath)
		return "", fmt.Errorf("pre-rename symlink recheck failed: %w", err)
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		_ = os.Remove(rollbackPath)
		return "", fmt.Errorf("atomic rename replace: %w", err)
	}
	syncDirectory(parent)
	return rollbackPath, nil
}

func copyFileNoFollow(src, dst string) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if srcInfo.Mode()&os.ModeSymlink != 0 || !srcInfo.Mode().IsRegular() {
		return fmt.Errorf("cannot copy non-regular or symlink source")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	return nil
}

func commitNoReplace(source, destination string) error {
	// Recheck symlink ancestors immediately before rename/CAS commit.
	if err := ensureNoSymlinkComponents(destination); err != nil {
		return fmt.Errorf("pre-rename symlink recheck: %w", err)
	}
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

func trustedSystemSymlink(path, root, goos string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if goos != "darwin" || path == root {
		return false
	}
	return path == string(filepath.Separator)+"var"
}

func ensureNoSymlinkComponents(path string) error {
	clean := filepath.Clean(path)
	for current := clean; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 && !trustedSystemSymlink(current, clean, runtime.GOOS) {
				return fmt.Errorf("restore root contains a symlink or junction: %s", current)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect restore root component %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return nil
}

func ensureSafeRoot(root string) error {
	if err := ensureNoSymlinkComponents(root); err != nil {
		return err
	}
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
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	return ensureNoSymlinkComponents(path) == nil
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
	PlaintextDigest  string `json:"plaintext_digest"`
	CiphertextDigest string `json:"ciphertext_digest,omitempty"`
	Root             string `json:"root,omitempty"`
	RootIdentity     string `json:"root_identity,omitempty"`
	RollbackPath     string `json:"rollback_path,omitempty"`
	RollbackDigest   string `json:"rollback_digest,omitempty"`
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
	if record.Action != "create" && record.Action != "replace" {
		return fmt.Errorf("unsupported journal action %q", record.Action)
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
		if record.TransactionID == "" || record.Before == "" || record.After == "" || record.PlaintextDigest == "" {
			return fmt.Errorf("journal record %d lacks recovery fields", i)
		}
		if transactionID == "" {
			transactionID = record.TransactionID
		} else if transactionID != record.TransactionID {
			return fmt.Errorf("journal records contain multiple transactions")
		}
		if record.Action == "create" {
			if record.Before != "absent" || (record.After != "created" && record.After != "staged") {
				return fmt.Errorf("journal record %d is not a valid create recovery marker", i)
			}
		} else if record.Action == "replace" {
			if record.Before == "" || (record.After != "replaced" && record.After != "staged") {
				return fmt.Errorf("journal record %d is not a valid replace recovery marker", i)
			}
		} else {
			return fmt.Errorf("journal record %d has unknown action %q", i, record.Action)
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

// RecoverTransaction recovers create and replace actions recorded by one explicit transaction.
func RecoverTransaction(root, transactionID string) error {
	tx, err := OpenTransaction(root, transactionID)
	if err != nil {
		return err
	}
	records, err := tx.Read()
	if err != nil {
		return err
	}
	return RecoverWithIdentity(tx.Root, tx.RootIdentity, records)
}

// RecoverCreateOnly removes only files that a journal proves were newly created.
func RecoverCreateOnly(root string, records []JournalRecord) error {
	identity, err := CaptureRootIdentity(root)
	if err != nil {
		return err
	}
	return RecoverCreateOnlyWithIdentity(identity.Path, identity, records)
}

func RecoverCreateOnlyWithIdentity(root string, identity RootIdentity, records []JournalRecord) error {
	return RecoverWithIdentity(root, identity, records)
}

// RecoverWithIdentity rolls back created and replaced paths from the recorded journal.
func RecoverWithIdentity(root string, identity RootIdentity, records []JournalRecord) error {
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

		if record.Action == "create" {
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
			if got != record.PlaintextDigest {
				return fmt.Errorf("recovery destination %q digest mismatch", clean)
			}
			if err := identity.Validate(root); err != nil {
				return err
			}
			if err := os.Remove(destination); err != nil {
				return fmt.Errorf("remove recovered destination %q: %w", clean, err)
			}
		} else if record.Action == "replace" {
			// For replace: restore rollback snapshot.
			if record.RollbackPath == "" {
				return fmt.Errorf("replace recovery requires rollback snapshot path")
			}
			if err := copyFileNoFollow(record.RollbackPath, destination); err != nil {
				return fmt.Errorf("restore from rollback snapshot: %w", err)
			}
			_ = os.Remove(record.RollbackPath)
		}
	}
	return identity.Validate(root)
}
