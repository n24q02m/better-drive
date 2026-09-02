package cleanup

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func validOwnerRiskClaimRequest(t *testing.T) OwnerRiskClaimRequest {
	t.Helper()
	approval := validApproval()
	identityHash, err := QuarantineIdentityHash(approval.QuarantineTarget)
	if err != nil {
		t.Fatal(err)
	}
	return OwnerRiskClaimRequest{
		SchemaVersion:      CurrentOwnerRiskSchemaVersion,
		Repository:         "n24q02m/private-control",
		ApprovalID:         approval.ApprovalID,
		ManifestDigest:     approval.ManifestDigest,
		IntentRef:          IntentRef(approval.ApprovalID),
		IntentOID:          strings.Repeat("1", 40),
		StateRef:           StateRef(approval.ApprovalID),
		StateExpectedOID:   strings.Repeat("2", 40),
		JournalRef:         JournalRef(approval.ApprovalID),
		JournalExpectedOID: "",
		Operation:          OwnerRiskOperationQuarantine,
		LeaseRef:           LeaseRef(identityHash),
		LeaseExpectedOID:   "",
		MutationSemantics:  approval.MutationSemantics,
		QuarantineTarget:   approval.QuarantineTarget,
		MaxObjects:         approval.MaxObjects,
		MaxBytes:           approval.MaxBytes,
		ExpiresAt:          approval.ExpiresAt,
		Nonce:              approval.Nonce,
		Owner:              "executor-home",
		ExecutionID:        "execution-01",
		RequestID:          "request-01",
	}
}

func signedOwnerRiskClaim(t *testing.T, request OwnerRiskClaimRequest, privateKey ed25519.PrivateKey, now time.Time) OwnerRiskClaim {
	t.Helper()
	canonical, err := CanonicalOwnerRiskClaimRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := SignOwnerRiskClaim(OwnerRiskClaim{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		ClaimID:       "claim-01",
		Request:       request,
		RequestDigest: Digest(canonical),
		State:         OwnerRiskClaimed,
		StateOID:      strings.Repeat("3", 40),
		JournalOID:    strings.Repeat("4", 40),
		LeaseOID:      strings.Repeat("5", 40),
		Generation:    1,
		Fence:         1,
		Atomic:        true,
		Authority:     "cleanup-broker",
		IssuedAt:      now,
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func TestOwnerRiskClaimBindsExactRequestAndAtomicReadback(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := validOwnerRiskClaimRequest(t)
	request.ExpiresAt = now.Add(time.Minute)
	claim := signedOwnerRiskClaim(t, request, privateKey, now)
	if err := VerifyOwnerRiskClaim(claim, request, publicKey, now); err != nil {
		t.Fatalf("VerifyOwnerRiskClaim() error = %v", err)
	}

	tampered := request
	tampered.RequestID = "request-02"
	if err := VerifyOwnerRiskClaim(claim, tampered, publicKey, now); err == nil || !strings.Contains(err.Error(), "exact request") {
		t.Fatalf("expected exact request rejection, got %v", err)
	}

	claim.Atomic = false
	if err := VerifyOwnerRiskClaim(claim, request, publicKey, now); err == nil || !strings.Contains(err.Error(), "atomic") {
		t.Fatalf("expected non-atomic readback rejection, got %v", err)
	}
}

func TestOwnerRiskClaimRejectsTamperingExpiryAndUnadvancedRefs(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := validOwnerRiskClaimRequest(t)
	request.ExpiresAt = now.Add(time.Minute)
	claim := signedOwnerRiskClaim(t, request, privateKey, now)

	tampered := claim
	tampered.Fence++
	if err := VerifyOwnerRiskClaim(tampered, request, publicKey, now); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected tampered signature rejection, got %v", err)
	}
	if err := VerifyOwnerRiskClaim(claim, request, publicKey, request.ExpiresAt); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired claim rejection, got %v", err)
	}

	unadvanced := claim
	unadvanced.StateOID = request.StateExpectedOID
	if _, err := SignOwnerRiskClaim(unadvanced, privateKey); err == nil || !strings.Contains(err.Error(), "advance") {
		t.Fatalf("expected unadvanced state ref rejection, got %v", err)
	}
}

func TestOwnerRiskFenceReadbackBindsCurrentClaimRefs(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claimRequest := validOwnerRiskClaimRequest(t)
	claimRequest.ExpiresAt = now.Add(time.Minute)
	claim := signedOwnerRiskClaim(t, claimRequest, privateKey, now)
	claimCanonical, err := CanonicalOwnerRiskClaim(claim)
	if err != nil {
		t.Fatal(err)
	}
	request := OwnerRiskFenceRequest{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		ClaimID:       claim.ClaimID,
		ClaimDigest:   Digest(claimCanonical),
		ApprovalID:    claim.Request.ApprovalID,
		ExecutionID:   claim.Request.ExecutionID,
		RequestID:     claim.Request.RequestID,
		StateOID:      claim.StateOID,
		JournalOID:    claim.JournalOID,
		LeaseOID:      claim.LeaseOID,
		Generation:    claim.Generation,
		Fence:         claim.Fence,
	}
	canonical, err := CanonicalOwnerRiskFenceRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	readback, err := SignOwnerRiskFenceReadback(OwnerRiskFenceReadback{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		Request:       request,
		RequestDigest: Digest(canonical),
		State:         OwnerRiskClaimed,
		Authority:     "cleanup-broker",
		ObservedAt:    now,
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOwnerRiskFenceReadback(readback, request, publicKey, now); err != nil {
		t.Fatalf("VerifyOwnerRiskFenceReadback() error = %v", err)
	}

	tampered := request
	tampered.LeaseOID = strings.Repeat("a", 40)
	if err := VerifyOwnerRiskFenceReadback(readback, tampered, publicKey, now); err == nil || !strings.Contains(err.Error(), "exact request") {
		t.Fatalf("expected exact fence ref rejection, got %v", err)
	}
}

func TestOwnerRiskSettlementBindsClaimAndAllRefReadbacks(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claimRequest := validOwnerRiskClaimRequest(t)
	claimRequest.ExpiresAt = now.Add(time.Minute)
	claim := signedOwnerRiskClaim(t, claimRequest, privateKey, now)
	claimCanonical, err := CanonicalOwnerRiskClaim(claim)
	if err != nil {
		t.Fatal(err)
	}
	request := OwnerRiskSettlementRequest{
		SchemaVersion:      CurrentOwnerRiskSchemaVersion,
		ClaimID:            claim.ClaimID,
		ClaimDigest:        Digest(claimCanonical),
		ApprovalID:         claim.Request.ApprovalID,
		ExecutionID:        claim.Request.ExecutionID,
		RequestID:          claim.Request.RequestID,
		StateExpectedOID:   claim.StateOID,
		JournalExpectedOID: claim.JournalOID,
		LeaseExpectedOID:   claim.LeaseOID,
		Settlement:         OwnerRiskConsumed,
		OutcomeDigest:      strings.Repeat("6", 64),
		ProviderRequests:   []string{"provider-request-01"},
	}
	requestCanonical, err := CanonicalOwnerRiskSettlementRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := SignOwnerRiskSettlement(OwnerRiskSettlement{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		Request:       request,
		RequestDigest: Digest(requestCanonical),
		State:         OwnerRiskConsumed,
		StateOID:      strings.Repeat("7", 40),
		JournalOID:    strings.Repeat("8", 40),
		LeaseOID:      strings.Repeat("9", 40),
		Authority:     claim.Authority,
		ObservedAt:    now,
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOwnerRiskSettlement(settlement, request, publicKey, now); err != nil {
		t.Fatalf("VerifyOwnerRiskSettlement() error = %v", err)
	}

	tampered := request
	tampered.Settlement = OwnerRiskNeedsReconciliation
	if err := VerifyOwnerRiskSettlement(settlement, tampered, publicKey, now); err == nil || !strings.Contains(err.Error(), "exact request") {
		t.Fatalf("expected settlement request binding rejection, got %v", err)
	}
}
