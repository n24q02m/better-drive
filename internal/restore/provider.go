package restore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// SourceReference binds an immutable provider object that holds a sealed artifact.
// It is the only production fetch source; ambient filesystem SourcePath is never used for production.
type SourceReference struct {
	Provider         string `json:"provider"`
	AccountID        string `json:"account_id"`
	ObjectID         string `json:"object_id"`
	Version          string `json:"version"`
	Size             int64  `json:"size"`
	CiphertextDigest string `json:"ciphertext_digest"`
}

// SourceReadback is the provider's authoritative readback for one Open.
type SourceReadback struct {
	Reference        SourceReference `json:"reference"`
	Size             int64           `json:"size"`
	CiphertextDigest string          `json:"ciphertext_digest"`
	Version          string          `json:"version"`
}

// SourceProvider is the typed, caller-injected authority for artifact bytes.
// Implementations must not access ambient env, net, or filesystem beyond the bound reference.
type SourceProvider interface {
	Open(ctx context.Context, ref SourceReference) (io.ReadCloser, SourceReadback, error)
}

func (r SourceReference) Validate() error {
	if strings.TrimSpace(r.Provider) == "" {
		return fmt.Errorf("source provider is required")
	}
	if strings.TrimSpace(r.AccountID) == "" {
		return fmt.Errorf("source account_id is required")
	}
	if strings.TrimSpace(r.ObjectID) == "" {
		return fmt.Errorf("source object_id is required")
	}
	if strings.TrimSpace(r.Version) == "" {
		return fmt.Errorf("source version is required")
	}
	if r.Size < 0 {
		return fmt.Errorf("source size must be non-negative")
	}
	if err := validateSHA256Digest("source ciphertext_digest", r.CiphertextDigest); err != nil {
		return err
	}
	return nil
}

func (r SourceReadback) Validate(expected SourceReference) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	if r.Reference.Provider != expected.Provider || r.Reference.AccountID != expected.AccountID || r.Reference.ObjectID != expected.ObjectID || r.Reference.Version != expected.Version {
		return fmt.Errorf("source readback identity mismatch")
	}
	if r.Size != expected.Size {
		return fmt.Errorf("source readback size mismatch")
	}
	if r.CiphertextDigest != expected.CiphertextDigest {
		return fmt.Errorf("source readback ciphertext digest mismatch")
	}
	if r.Reference.CiphertextDigest != expected.CiphertextDigest {
		return fmt.Errorf("source readback reference digest mismatch")
	}
	if r.Version != expected.Version {
		return fmt.Errorf("source readback version mismatch")
	}
	return nil
}

func validateCanonicalSHA256DigestField(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return fmt.Errorf("%s must be a canonical sha256 digest", field)
	}
	digest := value[len("sha256:"):]
	if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
		return fmt.Errorf("%s must be a canonical sha256 digest", field)
	}
	return nil
}
