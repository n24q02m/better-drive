package restore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"strings"
	"time"
	"unicode"
)

// StagingEvidence is a fresh, independently collected attestation for the
// exact restore root. It is intentionally descriptive only: the verifier owns
// how proof is collected and must not return secret material here.
type StagingEvidence struct {
	RootIdentity       RootIdentity `json:"root_identity"`
	ProofDigest        string       `json:"proof_digest"`
	ProofID            string       `json:"proof_id"`
	VerifiedAt         time.Time    `json:"verified_at"`
	EncryptedAtRest    bool         `json:"encrypted_at_rest"`
	OwnerOnly          bool         `json:"owner_only"`
	NonInheritedACL    bool         `json:"non_inherited_acl"`
	ExcludedFromBackup bool         `json:"excluded_from_backup"`
}

// StagingVerifier independently verifies the root's storage and access
// properties for one exact RootIdentity. Each call must collect fresh
// evidence rather than returning a cached attestation.
type StagingVerifier interface {
	Verify(root string, identity RootIdentity) (StagingEvidence, error)
}

// VerifyStagingEvidence requires an enrolled verifier and validates the
// evidence against the current root identity before any restore writes.
func VerifyStagingEvidence(root string, identity RootIdentity, verifier StagingVerifier) (StagingEvidence, error) {
	if verifier == nil {
		return StagingEvidence{}, fmt.Errorf("staging verifier is required for restore execution")
	}
	if err := validateRootIdentityFields(identity); err != nil {
		return StagingEvidence{}, err
	}
	if err := identity.Validate(root); err != nil {
		return StagingEvidence{}, fmt.Errorf("restore root identity: %w", err)
	}
	evidence, err := verifier.Verify(root, identity)
	if err != nil {
		return StagingEvidence{}, fmt.Errorf("staging evidence verification failed: %w", err)
	}
	if err := evidence.Validate(root, identity); err != nil {
		return StagingEvidence{}, fmt.Errorf("staging evidence: %w", err)
	}
	return evidence, nil
}

// Validate checks that evidence is complete, canonical, and bound to the
// exact root identity supplied by the restore plan.
func (e StagingEvidence) Validate(root string, identity RootIdentity) error {
	if err := validateRootIdentityFields(identity); err != nil {
		return err
	}
	if err := identity.Validate(root); err != nil {
		return fmt.Errorf("restore root identity: %w", err)
	}
	if err := validateRootIdentityFields(e.RootIdentity); err != nil {
		return fmt.Errorf("evidence root identity: %w", err)
	}
	if e.RootIdentity != identity {
		return fmt.Errorf("evidence root identity drifted")
	}
	if err := validateCanonicalSHA256Digest("proof_digest", e.ProofDigest); err != nil {
		return err
	}
	if err := validateEvidenceString("proof_id", e.ProofID); err != nil {
		return err
	}
	if e.VerifiedAt.IsZero() {
		return fmt.Errorf("verification time is required")
	}
	if !e.EncryptedAtRest {
		return fmt.Errorf("staging root is not encrypted at rest")
	}
	if !e.OwnerOnly {
		return fmt.Errorf("staging root is not owner-only")
	}
	if !e.ExcludedFromBackup {
		return fmt.Errorf("staging root is included in a backup or sync manifest")
	}
	if runtime.GOOS == "windows" && !e.NonInheritedACL {
		return fmt.Errorf("staging root has an inherited Windows ACL")
	}
	return nil
}

// Equivalent compares the stable attestation fields. Verification timestamps
// are intentionally excluded because a verifier is required to return fresh
// evidence on every call.
func (e StagingEvidence) Equivalent(other StagingEvidence) bool {
	return e.RootIdentity == other.RootIdentity &&
		e.ProofDigest == other.ProofDigest &&
		e.ProofID == other.ProofID &&
		e.EncryptedAtRest == other.EncryptedAtRest &&
		e.OwnerOnly == other.OwnerOnly &&
		e.NonInheritedACL == other.NonInheritedACL &&
		e.ExcludedFromBackup == other.ExcludedFromBackup
}

func validateRootIdentityFields(identity RootIdentity) error {
	if err := validateEvidenceString("root identity path", identity.Path); err != nil {
		return err
	}
	if err := validateEvidenceString("root identity token", identity.Token); err != nil {
		return err
	}
	return nil
}

func validateEvidenceString(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains control characters", field)
		}
	}
	return nil
}

func validateCanonicalSHA256Digest(field, value string) error {
	if err := validateEvidenceString(field, value); err != nil {
		return err
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
