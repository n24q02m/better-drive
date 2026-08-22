package restore

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Entry struct {
	RelativePath string `json:"relative_path"`
	SourcePath   string `json:"source_path"`
	SourceDigest string `json:"source_digest"`
	Size         int64  `json:"size"`
}

type Plan struct {
	Root      string   `json:"root"`
	Entries   []Entry  `json:"entries"`
	Conflicts []string `json:"conflicts"`
}

func BuildPlan(root string, entries []Entry) (Plan, error) {
	if !filepath.IsAbs(root) {
		return Plan{}, fmt.Errorf("restore root must be absolute")
	}
	cleanRoot := filepath.Clean(root)
	plan := Plan{Root: cleanRoot, Entries: make([]Entry, 0, len(entries)), Conflicts: make([]string, 0)}
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
	if err := ensureSafeRoot(plan.Root); err != nil {
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
	if err := os.Rename(tmpPath, destination); err != nil {
		return fmt.Errorf("commit restore file: %w", err)
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
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("restore ancestor is not a safe directory")
		}
	}
	return parent, nil
}

type Journal struct {
	Path string
}

type JournalRecord struct {
	Entry        string `json:"entry"`
	Action       string `json:"action"`
	Before       string `json:"before"`
	After        string `json:"after"`
	SourceDigest string `json:"source_digest"`
}

func (j Journal) Append(record JournalRecord) error {
	if strings.TrimSpace(j.Path) == "" || strings.TrimSpace(record.Entry) == "" || strings.TrimSpace(record.Action) == "" {
		return fmt.Errorf("journal path, entry, and action are required")
	}
	if record.Action != "create" && record.Action != "replace" {
		return fmt.Errorf("unsupported journal action %q", record.Action)
	}
	if err := os.MkdirAll(filepath.Dir(j.Path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(j.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(record)
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
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (j Journal) ValidateRecovery(records []JournalRecord) error {
	for i, record := range records {
		if record.Before == "" || record.After == "" || record.SourceDigest == "" {
			return fmt.Errorf("journal record %d lacks recovery fields", i)
		}
	}
	return nil
}
