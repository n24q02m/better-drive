package cleanup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type gitAuthorityFixture struct {
	store              *ApprovalStore
	repo               *GitRepo
	authority          *GitOwnerRiskAuthority
	approvalRoot       TrustRoot
	approvalPrivateKey ed25519.PrivateKey
	authorityPublicKey ed25519.PublicKey
	request            OwnerRiskClaimRequest
	now                time.Time
}

func newGitAuthorityFixture(t *testing.T) gitAuthorityFixture {
	t.Helper()
	now := time.Unix(150, 0).UTC()
	approvalPublicKey, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, authorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approvalRoot, err := NewTrustRoot(
		"cleanup-approvers",
		validApproval().Issuer,
		CleanupTrustPurpose,
		approvalPublicKey,
		time.Unix(100, 0).UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	store := newTestApprovalStore(t, protectedTestDir(t))
	repo, err := NewGitRepo(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	request := activateAuthorityApproval(t, store, approvalRoot, approvalPrivateKey, validApproval())
	authority, err := NewGitOwnerRiskAuthority(
		store,
		approvalRoot,
		authorityPrivateKey,
		"cleanup-broker",
		"n24q02m/private-control",
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	return gitAuthorityFixture{
		store:              store,
		repo:               repo,
		authority:          authority,
		approvalRoot:       approvalRoot,
		approvalPrivateKey: approvalPrivateKey,
		authorityPublicKey: authorityPublicKey,
		request:            request,
		now:                now,
	}
}

func activateAuthorityApproval(
	t *testing.T,
	store *ApprovalStore,
	root TrustRoot,
	privateKey ed25519.PrivateKey,
	approval Approval,
) OwnerRiskClaimRequest {
	t.Helper()
	signature, err := SignApproval(approval, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prepare(approval); err != nil {
		t.Fatal(err)
	}
	intent, err := ActivateApproval(approval, signature, root, time.Unix(150, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(intent); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ReadSnapshot(approval.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
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
		IntentOID:          snapshot.IntentOID,
		StateRef:           StateRef(approval.ApprovalID),
		StateExpectedOID:   snapshot.StateOID,
		JournalRef:         JournalRef(approval.ApprovalID),
		JournalExpectedOID: snapshot.JournalOID,
		Operation:          OwnerRiskOperationQuarantine,
		LeaseRef:           LeaseRef(identityHash),
		LeaseExpectedOID:   snapshot.LeaseOID,
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

func claimDigest(t *testing.T, claim OwnerRiskClaim) string {
	t.Helper()
	canonical, err := CanonicalOwnerRiskClaim(claim)
	if err != nil {
		t.Fatal(err)
	}
	return Digest(canonical)
}

func TestGitOwnerRiskAuthorityClaimRecheckAndSettle(t *testing.T) {
	fixture := newGitAuthorityFixture(t)
	snapshotRequest := OwnerRiskSnapshotRequest{
		SchemaVersion:    CurrentOwnerRiskSchemaVersion,
		Repository:       fixture.request.Repository,
		ApprovalID:       fixture.request.ApprovalID,
		ManifestDigest:   fixture.request.ManifestDigest,
		QuarantineTarget: fixture.request.QuarantineTarget,
		RequestID:        "snapshot-01",
	}
	snapshot, err := fixture.authority.SnapshotOwnerRisk(context.Background(), snapshotRequest)
	if err != nil {
		t.Fatalf("SnapshotOwnerRisk() error = %v", err)
	}
	if err := VerifyOwnerRiskSnapshot(snapshot, snapshotRequest, fixture.authorityPublicKey, fixture.now); err != nil {
		t.Fatalf("VerifyOwnerRiskSnapshot() error = %v", err)
	}
	if snapshot.IntentOID != fixture.request.IntentOID || snapshot.StateOID != fixture.request.StateExpectedOID ||
		snapshot.JournalOID != fixture.request.JournalExpectedOID || snapshot.LeaseOID != fixture.request.LeaseExpectedOID {
		t.Fatalf("snapshot refs do not match claim inputs: %+v", snapshot)
	}
	claim, err := fixture.authority.ClaimOwnerRisk(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("ClaimOwnerRisk() error = %v", err)
	}
	if err := VerifyOwnerRiskClaim(claim, fixture.request, fixture.authorityPublicKey, fixture.now); err != nil {
		t.Fatalf("VerifyOwnerRiskClaim() error = %v", err)
	}

	fenceRequest := OwnerRiskFenceRequest{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		ClaimID:       claim.ClaimID,
		ClaimDigest:   claimDigest(t, claim),
		ApprovalID:    claim.Request.ApprovalID,
		ExecutionID:   claim.Request.ExecutionID,
		RequestID:     claim.Request.RequestID,
		StateOID:      claim.StateOID,
		JournalOID:    claim.JournalOID,
		LeaseOID:      claim.LeaseOID,
		Generation:    claim.Generation,
		Fence:         claim.Fence,
	}
	readback, err := fixture.authority.RecheckOwnerRisk(context.Background(), fenceRequest)
	if err != nil {
		t.Fatalf("RecheckOwnerRisk() error = %v", err)
	}
	if err := VerifyOwnerRiskFenceReadback(readback, fenceRequest, fixture.authorityPublicKey, fixture.now); err != nil {
		t.Fatalf("VerifyOwnerRiskFenceReadback() error = %v", err)
	}

	settlementRequest := OwnerRiskSettlementRequest{
		SchemaVersion:      CurrentOwnerRiskSchemaVersion,
		ClaimID:            claim.ClaimID,
		ClaimDigest:        fenceRequest.ClaimDigest,
		ApprovalID:         claim.Request.ApprovalID,
		ExecutionID:        claim.Request.ExecutionID,
		RequestID:          claim.Request.RequestID,
		StateExpectedOID:   claim.StateOID,
		JournalExpectedOID: claim.JournalOID,
		LeaseExpectedOID:   claim.LeaseOID,
		Settlement:         OwnerRiskConsumed,
		OutcomeDigest:      strings.Repeat("a", 64),
		ProviderRequests:   []string{"drive-request-01"},
	}
	settlement, err := fixture.authority.SettleOwnerRisk(context.Background(), settlementRequest)
	if err != nil {
		t.Fatalf("SettleOwnerRisk() error = %v", err)
	}
	if err := VerifyOwnerRiskSettlement(settlement, settlementRequest, fixture.authorityPublicKey, fixture.now); err != nil {
		t.Fatalf("VerifyOwnerRiskSettlement() error = %v", err)
	}

	finalSnapshot, err := fixture.store.ReadSnapshot(fixture.request.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if finalSnapshot.State.State != OwnerRiskConsumed {
		t.Fatalf("state = %q, want consumed", finalSnapshot.State.State)
	}
	leaseData, err := fixture.store.RuntimeTransport.ReadBlob(finalSnapshot.LeaseOID)
	if err != nil {
		t.Fatal(err)
	}
	var lease LeaseRecord
	if err := json.Unmarshal(leaseData, &lease); err != nil {
		t.Fatal(err)
	}
	if lease.State != LeaseConsumed || lease.Fence != claim.Fence+1 || lease.ClaimDigest != settlementRequest.ClaimDigest {
		t.Fatalf("unexpected settled lease: %+v", lease)
	}
	if _, err := fixture.authority.ClaimOwnerRisk(context.Background(), fixture.request); err == nil {
		t.Fatal("replayed claim unexpectedly succeeded")
	}
	if _, err := fixture.authority.RecheckOwnerRisk(context.Background(), fenceRequest); err == nil {
		t.Fatal("settled claim still passed a fence recheck")
	}
}

type leaseRaceTransport struct {
	base        *LocalGitTransport
	repo        *GitRepo
	injectedOID string
	injected    bool
}

func (transport *leaseRaceTransport) ReadRef(ref string) (string, bool, error) {
	return transport.base.ReadRef(ref)
}

func (transport *leaseRaceTransport) ReadBlob(oid string) ([]byte, error) {
	return transport.base.ReadBlob(oid)
}

func (transport *leaseRaceTransport) CommitRuntimeTransition(transition RuntimeAuthorityTransition) (RuntimeAuthorityTransitionResult, error) {
	if !transport.injected {
		oid, err := transport.repo.WriteBlob([]byte("concurrent lease"))
		if err != nil {
			return RuntimeAuthorityTransitionResult{}, err
		}
		if err := transport.repo.CAS(transition.Lease.Ref, transition.Lease.ExpectedOID, oid); err != nil {
			return RuntimeAuthorityTransitionResult{}, err
		}
		transport.injectedOID = oid
		transport.injected = true
	}
	return transport.base.CommitRuntimeTransition(transition)
}

func TestGitOwnerRiskAuthorityStaleLeaseAbortsStateAndJournal(t *testing.T) {
	fixture := newGitAuthorityFixture(t)
	initialStateOID := fixture.request.StateExpectedOID
	base := NewLocalGitTransport(fixture.repo)
	race := &leaseRaceTransport{base: base, repo: fixture.repo}
	fixture.store.RuntimeTransport = race

	if _, err := fixture.authority.ClaimOwnerRisk(context.Background(), fixture.request); err == nil {
		t.Fatal("claim unexpectedly survived a concurrent lease write")
	}
	stateOID, exists, err := fixture.repo.ReadRef(fixture.request.StateRef)
	if err != nil || !exists || stateOID != initialStateOID {
		t.Fatalf("state moved after stale lease rejection: oid=%q exists=%v err=%v", stateOID, exists, err)
	}
	if journalOID, exists, err := fixture.repo.ReadRef(fixture.request.JournalRef); err != nil || exists {
		t.Fatalf("journal was created after stale lease rejection: oid=%q exists=%v err=%v", journalOID, exists, err)
	}
	leaseOID, exists, err := fixture.repo.ReadRef(fixture.request.LeaseRef)
	if err != nil || !exists || leaseOID != race.injectedOID {
		t.Fatalf("concurrent lease was not preserved: oid=%q exists=%v err=%v", leaseOID, exists, err)
	}
}

func TestGitOwnerRiskAuthorityNeedsReconciliationFencesTarget(t *testing.T) {
	fixture := newGitAuthorityFixture(t)
	claim, err := fixture.authority.ClaimOwnerRisk(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	settlementRequest := OwnerRiskSettlementRequest{
		SchemaVersion:      CurrentOwnerRiskSchemaVersion,
		ClaimID:            claim.ClaimID,
		ClaimDigest:        claimDigest(t, claim),
		ApprovalID:         claim.Request.ApprovalID,
		ExecutionID:        claim.Request.ExecutionID,
		RequestID:          claim.Request.RequestID,
		StateExpectedOID:   claim.StateOID,
		JournalExpectedOID: claim.JournalOID,
		LeaseExpectedOID:   claim.LeaseOID,
		Settlement:         OwnerRiskNeedsReconciliation,
		OutcomeDigest:      strings.Repeat("b", 64),
		ProviderRequests:   []string{"drive-request-unknown"},
	}
	if _, err := fixture.authority.SettleOwnerRisk(context.Background(), settlementRequest); err != nil {
		t.Fatal(err)
	}
	firstSnapshot, err := fixture.store.ReadSnapshot(fixture.request.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}

	secondApproval := validApproval()
	secondApproval.ApprovalID = "approval-2"
	secondApproval.Nonce = "nonce-2"
	secondRequest := activateAuthorityApproval(
		t,
		fixture.store,
		fixture.approvalRoot,
		fixture.approvalPrivateKey,
		secondApproval,
	)
	secondRequest.LeaseExpectedOID = firstSnapshot.LeaseOID
	secondRequest.ExecutionID = "execution-02"
	secondRequest.RequestID = "request-02"
	if _, err := fixture.authority.ClaimOwnerRisk(context.Background(), secondRequest); err == nil || !strings.Contains(err.Error(), "reconciliation") {
		t.Fatalf("reconciliation-fenced target claim error = %v", err)
	}
	secondSnapshot, err := fixture.store.ReadSnapshot(secondApproval.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if secondSnapshot.State.State != ApprovalApproved || secondSnapshot.JournalOID != "" {
		t.Fatalf("fenced approval advanced: %+v", secondSnapshot)
	}
}

func TestGitOwnerRiskAuthorityRejectsMalformedConsumedLeaseLineage(t *testing.T) {
	fixture := newGitAuthorityFixture(t)
	claim, err := fixture.authority.ClaimOwnerRisk(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	settlementRequest := OwnerRiskSettlementRequest{
		SchemaVersion:      CurrentOwnerRiskSchemaVersion,
		ClaimID:            claim.ClaimID,
		ClaimDigest:        claimDigest(t, claim),
		ApprovalID:         claim.Request.ApprovalID,
		ExecutionID:        claim.Request.ExecutionID,
		RequestID:          claim.Request.RequestID,
		StateExpectedOID:   claim.StateOID,
		JournalExpectedOID: claim.JournalOID,
		LeaseExpectedOID:   claim.LeaseOID,
		Settlement:         OwnerRiskConsumed,
		OutcomeDigest:      strings.Repeat("c", 64),
		ProviderRequests:   []string{"drive-request-01"},
	}
	if _, err := fixture.authority.SettleOwnerRisk(context.Background(), settlementRequest); err != nil {
		t.Fatal(err)
	}
	firstSnapshot, err := fixture.store.ReadSnapshot(fixture.request.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	leaseData, err := fixture.store.RuntimeTransport.ReadBlob(firstSnapshot.LeaseOID)
	if err != nil {
		t.Fatal(err)
	}
	var malformed LeaseRecord
	if err := json.Unmarshal(leaseData, &malformed); err != nil {
		t.Fatal(err)
	}
	malformed.ClaimDigest = ""
	malformedData, err := marshalAuthorityRecord(malformed)
	if err != nil {
		t.Fatal(err)
	}
	malformedOID, err := fixture.repo.WriteBlob([]byte(malformedData))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.CAS(fixture.request.LeaseRef, firstSnapshot.LeaseOID, malformedOID); err != nil {
		t.Fatal(err)
	}

	secondApproval := validApproval()
	secondApproval.ApprovalID = "approval-2"
	secondApproval.Nonce = "nonce-2"
	secondRequest := activateAuthorityApproval(
		t,
		fixture.store,
		fixture.approvalRoot,
		fixture.approvalPrivateKey,
		secondApproval,
	)
	secondRequest.LeaseExpectedOID = malformedOID
	secondRequest.ExecutionID = "execution-02"
	secondRequest.RequestID = "request-02"
	if _, err := fixture.authority.ClaimOwnerRisk(context.Background(), secondRequest); err == nil || !strings.Contains(err.Error(), "lineage") {
		t.Fatalf("malformed consumed lease error = %v", err)
	}
}

func TestGitOwnerRiskAuthorityConsumedLeaseAdvancesGenerationAndFence(t *testing.T) {
	fixture := newGitAuthorityFixture(t)
	firstClaim, err := fixture.authority.ClaimOwnerRisk(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	firstSettlement := OwnerRiskSettlementRequest{
		SchemaVersion:      CurrentOwnerRiskSchemaVersion,
		ClaimID:            firstClaim.ClaimID,
		ClaimDigest:        claimDigest(t, firstClaim),
		ApprovalID:         firstClaim.Request.ApprovalID,
		ExecutionID:        firstClaim.Request.ExecutionID,
		RequestID:          firstClaim.Request.RequestID,
		StateExpectedOID:   firstClaim.StateOID,
		JournalExpectedOID: firstClaim.JournalOID,
		LeaseExpectedOID:   firstClaim.LeaseOID,
		Settlement:         OwnerRiskConsumed,
		OutcomeDigest:      strings.Repeat("d", 64),
		ProviderRequests:   []string{"drive-request-01"},
	}
	if _, err := fixture.authority.SettleOwnerRisk(context.Background(), firstSettlement); err != nil {
		t.Fatal(err)
	}
	firstSnapshot, err := fixture.store.ReadSnapshot(fixture.request.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}

	secondApproval := validApproval()
	secondApproval.ApprovalID = "approval-2"
	secondApproval.Nonce = "nonce-2"
	secondRequest := activateAuthorityApproval(
		t,
		fixture.store,
		fixture.approvalRoot,
		fixture.approvalPrivateKey,
		secondApproval,
	)
	secondRequest.LeaseExpectedOID = firstSnapshot.LeaseOID
	secondRequest.ExecutionID = "execution-02"
	secondRequest.RequestID = "request-02"
	secondClaim, err := fixture.authority.ClaimOwnerRisk(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("second ClaimOwnerRisk() error = %v", err)
	}
	if secondClaim.Generation != firstClaim.Generation+1 || secondClaim.Fence != firstClaim.Fence+2 {
		t.Fatalf(
			"second generation/fence = %d/%d, want %d/%d",
			secondClaim.Generation,
			secondClaim.Fence,
			firstClaim.Generation+1,
			firstClaim.Fence+2,
		)
	}
}
