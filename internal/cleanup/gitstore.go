package cleanup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// GitRepo is a local bare-repository simulation using filesystem refs and
// content-addressed blobs. It enforces create-only and CAS semantics with
// exact OID readback via file content. Production transports (workstation
// broker / OCI executor) retain credentials and perform the same ref
// operations through authenticated Git; this repo is used for local
// integration tests without remote calls.
type GitRepo struct {
	Root string
	mu   sync.Mutex
}

func NewGitRepo(root string) (*GitRepo, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("git repo root is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &GitRepo{Root: root}, nil
}

func (r *GitRepo) blobPath(oid string) string {
	if len(oid) < 4 {
		return filepath.Join(r.Root, "objects", oid)
	}
	return filepath.Join(r.Root, "objects", oid[:2], oid[2:])
}

func (r *GitRepo) refPath(ref string) string {
	// ref is already validated; join under Root.
	return filepath.Join(r.Root, filepath.FromSlash(ref))
}

// WriteBlob stores data content-addressed by Digest and returns its OID.
func (r *GitRepo) WriteBlob(data []byte) (string, error) {
	oid := Digest(data)
	path := r.blobPath(oid)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return oid, nil
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*.blob")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return oid, nil
	}
	if err := os.Rename(tmpName, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return oid, nil
		}
		return "", err
	}
	return oid, nil
}

func (r *GitRepo) ReadBlob(oid string) ([]byte, error) {
	if strings.TrimSpace(oid) == "" {
		return nil, errors.New("oid is required")
	}
	path := r.blobPath(oid)
	return os.ReadFile(path)
}

// ReadRef returns the OID stored at ref via ls-remote semantics.
func (r *GitRepo) ReadRef(ref string) (string, bool, error) {
	if err := validateSafeRef(ref); err != nil {
		return "", false, err
	}
	path := r.refPath(ref)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	oid := strings.TrimSpace(string(data))
	if oid == "" {
		return "", false, errors.New("ref is empty")
	}
	return oid, true, nil
}

// CreateRef creates ref with oid if absent (create-only). Returns existing OID on conflict.
func (r *GitRepo) CreateRef(ref, oid string) (string, error) {
	if strings.TrimSpace(ref) == "" || strings.TrimSpace(oid) == "" {
		return "", errors.New("ref and oid are required")
	}
	if err := validateSafeRef(ref); err != nil {
		return "", err
	}
	path := r.refPath(ref)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, _, readErr := r.ReadRef(ref)
			if readErr != nil {
				return "", readErr
			}
			return existing, fmt.Errorf("ref %q already exists with oid %q", ref, existing)
		}
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(oid + "\n"); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	return oid, nil
}

// CAS updates ref from expectedOID to newOID atomically. expectedOID "" means create.
func (r *GitRepo) CAS(ref, expectedOID, newOID string) error {
	if err := validateSafeRef(ref); err != nil {
		return err
	}
	if strings.TrimSpace(newOID) == "" {
		return errors.New("new oid is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, exists, err := r.ReadRef(ref)
	if err != nil {
		return err
	}
	if !exists {
		if expectedOID != "" {
			return fmt.Errorf("ref %q does not exist, expected %q", ref, expectedOID)
		}
		_, err := r.CreateRef(ref, newOID)
		return err
	}
	if current != expectedOID {
		return fmt.Errorf("ref %q stale OID: got %q want %q", ref, current, expectedOID)
	}
	dir := filepath.Dir(r.refPath(ref))
	tmp, err := os.CreateTemp(dir, ".tmp-*.ref")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(newOID + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, r.refPath(ref))
}

// AtomicCreateTwoRefs creates two refs create-only atomically with best-effort
// semantics: if the second create fails the first remains (split state) and
// the caller must reconcile.
func (r *GitRepo) AtomicCreateTwoRefs(ref1, oid1, ref2, oid2 string) error {
	if _, err := r.CreateRef(ref1, oid1); err != nil {
		return fmt.Errorf("create %q: %w", ref1, err)
	}
	if _, err := r.CreateRef(ref2, oid2); err != nil {
		return fmt.Errorf("create %q: %w (split state %q already created)", ref2, err, ref1)
	}
	return nil
}

func validateSafeRef(ref string) error {
	if !strings.HasPrefix(ref, "refs/") {
		return fmt.Errorf("ref %q must start with refs/", ref)
	}
	if strings.Contains(ref, "..") || strings.Contains(ref, "\\") || strings.Contains(ref, "\x00") || strings.ContainsAny(ref, "\r\n") {
		return fmt.Errorf("ref %q contains traversal or control characters", ref)
	}
	parts := strings.Split(ref, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return fmt.Errorf("ref %q contains invalid component", ref)
		}
	}
	if strings.ContainsAny(ref, "*?[]") {
		return fmt.Errorf("ref %q contains wildcard", ref)
	}
	return nil
}
