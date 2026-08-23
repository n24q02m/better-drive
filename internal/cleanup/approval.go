package cleanup

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const CurrentApprovalSchemaVersion = 1

const (
	ApprovalDraft    = "draft"
	ApprovalApproved = "approved"
	ApprovalClaimed  = "claimed"
	ApprovalConsumed = "consumed"
)

type Approval struct {
	SchemaVersion  int       `json:"schema_version"`
	ApprovalID     string    `json:"approval_id"`
	ManifestDigest string    `json:"manifest_digest"`
	AccountID      string    `json:"account_id"`
	RootID         string    `json:"root_id"`
	Mode           Mode      `json:"mode"`
	MaxObjects     int       `json:"max_objects"`
	MaxBytes       int64     `json:"max_bytes"`
	ExpiresAt      time.Time `json:"expires_at"`
	Nonce          string    `json:"nonce"`
	Issuer         string    `json:"issuer"`
	FixtureDigest  string    `json:"fixture_digest"`
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
	if approval.Mode != ModeQuarantine && approval.Mode != ModeTrash {
		return nil, fmt.Errorf("unsupported approval mode %q", approval.Mode)
	}
	if approval.MaxObjects <= 0 || approval.MaxBytes <= 0 {
		return nil, errors.New("approval budgets must be positive")
	}
	if approval.ExpiresAt.IsZero() {
		return nil, errors.New("approval expiry is required")
	}
	return marshalCanonical(approval)
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

func ActivateApproval(approval Approval, signature []byte, publicKey ed25519.PublicKey, now time.Time) (Intent, error) {
	if err := VerifyApproval(approval, signature, publicKey, now); err != nil {
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

func marshalCanonical(value any) ([]byte, error) {
	return json.Marshal(value)
}
