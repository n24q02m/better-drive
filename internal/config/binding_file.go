package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const DefaultMaxBindingBytes int64 = 1 << 20

// FileBindingResolver reads non-secret binding authorities from local files.
// A binding reference is either file:<absolute path> or an absolute path. The
// file itself is never returned; only its reference and bounded SHA-256 digest
// are exposed to callers.
type FileBindingResolver struct {
	MaxBytes int64
}

func (r FileBindingResolver) maxBytes() int64 {
	if r.MaxBytes <= 0 {
		return DefaultMaxBindingBytes
	}
	return r.MaxBytes
}

func (r FileBindingResolver) ReadRoleBinding(ref string) (BindingReadback, error) {
	return r.read("role", ref)
}

func (r FileBindingResolver) ReadPolicyBinding(ref string) (BindingReadback, error) {
	return r.read("policy", ref)
}

func (r FileBindingResolver) read(kind, ref string) (BindingReadback, error) {
	path, canonicalRef, err := bindingFilePath(ref)
	if err != nil {
		return BindingReadback{}, fmt.Errorf("%s binding ref: %w", kind, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return BindingReadback{}, fmt.Errorf("%s binding %q: %w", kind, canonicalRef, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return BindingReadback{}, fmt.Errorf("%s binding %q must not be a symlink", kind, canonicalRef)
	}
	if !info.Mode().IsRegular() {
		return BindingReadback{}, fmt.Errorf("%s binding %q must be a regular file", kind, canonicalRef)
	}
	file, err := os.Open(path)
	if err != nil {
		return BindingReadback{}, fmt.Errorf("%s binding %q open: %w", kind, canonicalRef, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return BindingReadback{}, fmt.Errorf("%s binding %q stat: %w", kind, canonicalRef, err)
	}
	if !opened.Mode().IsRegular() {
		return BindingReadback{}, fmt.Errorf("%s binding %q must be a regular file", kind, canonicalRef)
	}
	maxBytes := r.maxBytes()
	hash := sha256.New()
	count, err := io.CopyN(hash, file, maxBytes+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return BindingReadback{}, fmt.Errorf("%s binding %q read: %w", kind, canonicalRef, err)
	}
	if count > maxBytes {
		return BindingReadback{}, fmt.Errorf("%s binding %q exceeds bounded read limit of %d bytes", kind, canonicalRef, maxBytes)
	}
	return BindingReadback{Ref: canonicalRef, Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil))}, nil
}

func bindingFilePath(ref string) (path, canonicalRef string, err error) {
	canonicalRef = strings.TrimSpace(ref)
	if canonicalRef == "" {
		return "", "", errors.New("reference is required")
	}
	if strings.ContainsAny(canonicalRef, "\x00\r\n") {
		return "", "", errors.New("reference contains control characters")
	}
	path = canonicalRef
	if strings.HasPrefix(canonicalRef, "file:") {
		path = strings.TrimPrefix(canonicalRef, "file:")
	}
	if !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("unsupported reference %q; use file:<absolute path> or an absolute path", canonicalRef)
	}
	return filepath.Clean(path), canonicalRef, nil
}
