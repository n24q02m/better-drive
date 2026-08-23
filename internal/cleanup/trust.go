package cleanup

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	CurrentTrustRootSchemaVersion = 1
	CleanupTrustPurpose           = "BD-CLEANUP-APPROVAL-V1"
)

// TrustRoot is a public, non-secret record for an enrolled approval issuer.
// Private keys and enrollment credentials never belong in this record.
type TrustRoot struct {
	SchemaVersion int       `json:"schema_version"`
	RootID        string    `json:"root_id"`
	Issuer        string    `json:"issuer"`
	Purpose       string    `json:"purpose"`
	PublicKeyHex  string    `json:"public_key"`
	Fingerprint   string    `json:"fingerprint"`
	EnrolledAt    time.Time `json:"enrolled_at"`
	Active        bool      `json:"active"`
}

// NewTrustRoot only builds the public record. Persisting or activating it is a
// separate user/security-owner-gated enrollment operation.
func NewTrustRoot(rootID, issuer, purpose string, publicKey ed25519.PublicKey, enrolledAt time.Time) (TrustRoot, error) {
	if strings.TrimSpace(rootID) == "" || strings.TrimSpace(issuer) == "" {
		return TrustRoot{}, errors.New("trust root ID and issuer are required")
	}
	if purpose != CleanupTrustPurpose {
		return TrustRoot{}, fmt.Errorf("unsupported trust root purpose %q", purpose)
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return TrustRoot{}, errors.New("invalid Ed25519 trust root public key")
	}
	if enrolledAt.IsZero() {
		return TrustRoot{}, errors.New("trust root enrollment time is required")
	}
	return TrustRoot{
		SchemaVersion: CurrentTrustRootSchemaVersion,
		RootID:        rootID,
		Issuer:        issuer,
		Purpose:       purpose,
		PublicKeyHex:  hex.EncodeToString(publicKey),
		Fingerprint:   trustFingerprint(publicKey),
		EnrolledAt:    enrolledAt.UTC(),
		Active:        true,
	}, nil
}

// VerifyApprovalAgainstTrustRoot rejects unknown issuers and substituted or
// inactive public keys before delegating to the canonical approval verifier.
func VerifyApprovalAgainstTrustRoot(approval Approval, signature []byte, root TrustRoot, now time.Time) error {
	if approval.Issuer != root.Issuer {
		return fmt.Errorf("approval issuer %q is not enrolled in trust root %q", approval.Issuer, root.RootID)
	}
	publicKey, err := root.publicKey(now)
	if err != nil {
		return err
	}
	return VerifyApproval(approval, signature, publicKey, now)
}

func (root TrustRoot) publicKey(now time.Time) (ed25519.PublicKey, error) {
	if root.SchemaVersion != CurrentTrustRootSchemaVersion {
		return nil, fmt.Errorf("unsupported trust root schema_version %d", root.SchemaVersion)
	}
	if strings.TrimSpace(root.RootID) == "" || strings.TrimSpace(root.Issuer) == "" {
		return nil, errors.New("trust root ID and issuer are required")
	}
	if root.Purpose != CleanupTrustPurpose {
		return nil, fmt.Errorf("unsupported trust root purpose %q", root.Purpose)
	}
	if !root.Active {
		return nil, errors.New("trust root is inactive")
	}
	if root.EnrolledAt.IsZero() {
		return nil, errors.New("trust root enrollment time is required")
	}
	if !now.IsZero() && now.Before(root.EnrolledAt) {
		return nil, fmt.Errorf("trust root is not active until %s", root.EnrolledAt.UTC().Format(time.RFC3339))
	}
	publicKey, err := hex.DecodeString(root.PublicKeyHex)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("invalid Ed25519 trust root public key encoding")
	}
	if root.Fingerprint != trustFingerprint(publicKey) {
		return nil, errors.New("trust root public key fingerprint mismatch")
	}
	return append(ed25519.PublicKey(nil), publicKey...), nil
}

func trustFingerprint(publicKey []byte) string {
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:])
}
