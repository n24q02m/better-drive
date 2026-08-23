package cleanup

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func validApproval() Approval {
	return Approval{
		SchemaVersion:  CurrentApprovalSchemaVersion,
		ApprovalID:     "approval-1",
		ManifestDigest: strings.Repeat("a", 64),
		AccountID:      "account-1",
		RootID:         "root-1",
		Mode:           ModeQuarantine,
		MaxObjects:     2,
		MaxBytes:       20,
		ExpiresAt:      time.Unix(200, 0).UTC(),
		Nonce:          "nonce-1",
		Issuer:         "issuer-1",
		FixtureDigest:  strings.Repeat("b", 64),
	}
}

func TestApprovalSignVerifyAndTamperRejects(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approval := validApproval()
	signature, err := SignApproval(approval, privateKey)
	if err != nil {
		t.Fatalf("SignApproval() error = %v", err)
	}
	if err := VerifyApproval(approval, signature, publicKey, time.Unix(150, 0).UTC()); err != nil {
		t.Fatalf("VerifyApproval() error = %v", err)
	}
	approval.MaxBytes++
	if err := VerifyApproval(approval, signature, publicKey, time.Unix(150, 0).UTC()); err == nil {
		t.Fatal("tampered approval unexpectedly verified")
	}
}

func TestActivateApprovalBindsDraftAndRejectsExpired(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approval := validApproval()
	signature, err := SignApproval(approval, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := NewTrustRoot("root-key-1", approval.Issuer, CleanupTrustPurpose, publicKey, time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatalf("NewTrustRoot() error = %v", err)
	}
	intent, err := ActivateApproval(approval, signature, root, time.Unix(150, 0).UTC())
	if err != nil {
		t.Fatalf("ActivateApproval() error = %v", err)
	}
	if intent.State != ApprovalApproved || intent.IntentDigest == "" {
		t.Fatalf("unexpected intent: %+v", intent)
	}
	if _, err := ActivateApproval(approval, signature, root, time.Unix(250, 0).UTC()); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired approval rejection, got %v", err)
	}
}
func TestVerifyApprovalAgainstTrustRootRejectsUnknownIssuerAndKeySubstitution(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approval := validApproval()
	signature, err := SignApproval(approval, privateKey)
	if err != nil {
		t.Fatalf("SignApproval() error = %v", err)
	}
	root, err := NewTrustRoot("root-key-1", approval.Issuer, CleanupTrustPurpose, publicKey, time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatalf("NewTrustRoot() error = %v", err)
	}
	if err := VerifyApprovalAgainstTrustRoot(approval, signature, root, time.Unix(150, 0).UTC()); err != nil {
		t.Fatalf("VerifyApprovalAgainstTrustRoot() error = %v", err)
	}

	unknownIssuer := approval
	unknownIssuer.Issuer = "un enrolled"
	if err := VerifyApprovalAgainstTrustRoot(unknownIssuer, signature, root, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "not enrolled") {
		t.Fatalf("expected unknown issuer rejection, got %v", err)
	}

	substitutedKey := root
	substitutedKey.PublicKeyHex = strings.Repeat("00", ed25519.PublicKeySize)
	if err := VerifyApprovalAgainstTrustRoot(approval, signature, substitutedKey, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("expected key substitution rejection, got %v", err)
	}

	revoked := root
	revoked.Active = false
	if err := VerifyApprovalAgainstTrustRoot(approval, signature, revoked, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("expected inactive root rejection, got %v", err)
	}
}
