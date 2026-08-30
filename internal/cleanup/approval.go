package cleanup

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode"
)

const CurrentApprovalSchemaVersion = 2

const (
	ApprovalDraft               = "draft"
	ApprovalApproved            = "approved"
	ApprovalClaimed             = "claimed"
	ApprovalConsumed            = "consumed"
	ApprovalNeedsReconciliation = "needs_reconciliation"
)

type Approval struct {
	SchemaVersion     int              `json:"schema_version"`
	ApprovalID        string           `json:"approval_id"`
	ManifestDigest    string           `json:"manifest_digest"`
	AccountID         string           `json:"account_id"`
	RootID            string           `json:"root_id"`
	Mode              Mode             `json:"mode"`
	MutationSemantics string           `json:"mutation_semantics"`
	QuarantineTarget  QuarantineTarget `json:"quarantine_target"`
	MaxObjects        int              `json:"max_objects"`
	MaxBytes          int64            `json:"max_bytes"`
	ExpiresAt         time.Time        `json:"expires_at"`
	Nonce             string           `json:"nonce"`
	Issuer            string           `json:"issuer"`
	FixtureDigest     string           `json:"fixture_digest"`
}

type Intent struct {
	SchemaVersion int       `json:"schema_version"`
	IntentDigest  string    `json:"intent_digest"`
	Approval      Approval  `json:"approval"`
	SignatureHex  string    `json:"signature"`
	State         string    `json:"state"`
	CreatedAt     time.Time `json:"created_at"`
}

func CanonicalApproval(approval Approval) ([]byte, error) {
	if approval.SchemaVersion != CurrentApprovalSchemaVersion {
		return nil, fmt.Errorf("unsupported approval schema_version %d", approval.SchemaVersion)
	}
	if approval.ApprovalID == "" || approval.ManifestDigest == "" || approval.AccountID == "" || approval.RootID == "" || approval.Nonce == "" || approval.Issuer == "" || approval.FixtureDigest == "" {
		return nil, errors.New("approval identity, scope, nonce, issuer, and fixture are required")
	}
	if err := validateOpaqueApprovalID(approval.ApprovalID); err != nil {
		return nil, err
	}
	if approval.Mode != ModeQuarantine {
		return nil, fmt.Errorf("unsupported approval mode %q; only quarantine is supported", approval.Mode)
	}
	if approval.MutationSemantics != MutationSemanticsDriveOwnerRisk {
		return nil, errors.New("approval must explicitly bind Drive owner-risk single-attempt no-CAS semantics")
	}
	if err := validateSHA256Hex(approval.ManifestDigest, "approval manifest_digest"); err != nil {
		return nil, err
	}
	if err := validateSHA256Hex(approval.FixtureDigest, "approval fixture_digest"); err != nil {
		return nil, err
	}
	if err := validateQuarantineTarget(approval.QuarantineTarget, approval.AccountID); err != nil {
		return nil, err
	}
	if approval.MaxObjects <= 0 || approval.MaxBytes <= 0 {
		return nil, errors.New("approval budgets must be positive")
	}
	if approval.ExpiresAt.IsZero() {
		return nil, errors.New("approval expiry is required")
	}
	return marshalCanonical(approval)
}

func validateOpaqueApprovalID(id string) error {
	if len(id) == 0 || len(id) > 128 {
		return errors.New("approval ID must be 1-128 ASCII opaque characters")
	}
	for _, r := range id {
		if r > 127 || unicode.IsControl(r) || (r != '-' && r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r)) {
			return errors.New("approval ID must contain only ASCII letters, digits, '-' or '_'")
		}
	}
	return nil
}

func SignApproval(approval Approval, privateKey ed25519.PrivateKey) ([]byte, error) {
	canonical, err := CanonicalApproval(approval)
	if err != nil {
		return nil, err
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 private key")
	}
	return ed25519.Sign(privateKey, canonical), nil
}

func VerifyApproval(approval Approval, signature []byte, publicKey ed25519.PublicKey, now time.Time) error {
	canonical, err := CanonicalApproval(approval)
	if err != nil {
		return err
	}
	if !now.IsZero() && !now.Before(approval.ExpiresAt) {
		return fmt.Errorf("approval expired at %s", approval.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return errors.New("invalid Ed25519 signature material")
	}
	if !ed25519.Verify(publicKey, canonical, signature) {
		return errors.New("approval signature verification failed")
	}
	return nil
}

func ActivateApproval(approval Approval, signature []byte, root TrustRoot, now time.Time) (Intent, error) {
	if err := VerifyApprovalAgainstTrustRoot(approval, signature, root, now); err != nil {
		return Intent{}, err
	}
	canonical, err := CanonicalApproval(approval)
	if err != nil {
		return Intent{}, err
	}
	return Intent{
		SchemaVersion: CurrentApprovalSchemaVersion,
		IntentDigest:  Digest(canonical),
		Approval:      approval,
		SignatureHex:  fmt.Sprintf("%x", signature),
		State:         ApprovalApproved,
		CreatedAt:     now.UTC(),
	}, nil
}

func ValidateApprovalForManifest(approval Approval, manifest Manifest, now time.Time) error {
	if _, err := CanonicalApproval(approval); err != nil {
		return err
	}
	validation, err := ValidateManifest(manifest, now)
	if err != nil {
		return err
	}
	if approval.ManifestDigest != validation.ManifestDigest {
		return errors.New("approval manifest_digest does not match canonical manifest")
	}
	if approval.AccountID != manifest.AccountID ||
		approval.RootID != manifest.RootID ||
		approval.Mode != manifest.Mode ||
		approval.MutationSemantics != manifest.MutationSemantics ||
		approval.MaxObjects != manifest.Budget.MaxObjects ||
		approval.MaxBytes != manifest.Budget.MaxBytes ||
		approval.Nonce != manifest.Nonce ||
		approval.FixtureDigest != manifest.FixtureDigest ||
		!approval.ExpiresAt.Equal(manifest.ExpiresAt) {
		return errors.New("approval scope, budget, expiry, nonce, or fixture does not match manifest")
	}
	if approval.QuarantineTarget != manifest.QuarantineTarget {
		return errors.New("approval quarantine target does not match manifest")
	}
	return nil
}

func marshalCanonical(value any) ([]byte, error) {
	return json.Marshal(value)
}
