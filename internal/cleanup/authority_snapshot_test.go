package cleanup

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestOwnerRiskSnapshotBindsApprovedIntentAndFreshGitOIDs(t *testing.T) {
	authorityPublicKey, authorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	approval := validApproval()
	approval.ExpiresAt = now.Add(time.Minute)
	approvalSignature, err := SignApproval(approval, approvalPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	approvalCanonical, err := CanonicalApproval(approval)
	if err != nil {
		t.Fatal(err)
	}
	request := OwnerRiskSnapshotRequest{
		SchemaVersion:    CurrentOwnerRiskSchemaVersion,
		Repository:       "n24q02m/private-control",
		ApprovalID:       approval.ApprovalID,
		ManifestDigest:   approval.ManifestDigest,
		QuarantineTarget: approval.QuarantineTarget,
		RequestID:        "snapshot-request-01",
	}
	requestCanonical, err := CanonicalOwnerRiskSnapshotRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := SignOwnerRiskSnapshot(OwnerRiskSnapshot{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		Request:       request,
		RequestDigest: Digest(requestCanonical),
		Intent: Intent{
			SchemaVersion: CurrentApprovalSchemaVersion,
			IntentDigest:  Digest(approvalCanonical),
			Approval:      approval,
			SignatureHex:  hex.EncodeToString(approvalSignature),
			State:         ApprovalApproved,
			CreatedAt:     now.Add(-time.Minute),
		},
		IntentOID:  strings.Repeat("1", 40),
		StateOID:   strings.Repeat("2", 40),
		Authority:  "cleanup-broker",
		ObservedAt: now,
	}, authorityPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOwnerRiskSnapshot(snapshot, request, authorityPublicKey, now); err != nil {
		t.Fatalf("VerifyOwnerRiskSnapshot() error = %v", err)
	}

	tampered := snapshot
	tampered.StateOID = strings.Repeat("3", 40)
	if err := VerifyOwnerRiskSnapshot(tampered, request, authorityPublicKey, now); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected snapshot OID tamper rejection, got %v", err)
	}
	if err := VerifyOwnerRiskSnapshot(snapshot, request, authorityPublicKey, now.Add(31*time.Second)); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected stale snapshot rejection, got %v", err)
	}
}
