package driveapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
	"github.com/n24q02m/better-drive/internal/protectedfs"
)

type signingOwnerRiskAuthority struct {
	privateKey         ed25519.PrivateKey
	claimErr           error
	fenceErr           error
	settlementErr      error
	tamperClaim        bool
	claimRequests      []cleanup.OwnerRiskClaimRequest
	fenceRequests      []cleanup.OwnerRiskFenceRequest
	settlementRequests []cleanup.OwnerRiskSettlementRequest
}

func (authority *signingOwnerRiskAuthority) ClaimOwnerRisk(_ context.Context, request cleanup.OwnerRiskClaimRequest) (cleanup.OwnerRiskClaim, error) {
	authority.claimRequests = append(authority.claimRequests, request)
	if authority.claimErr != nil {
		return cleanup.OwnerRiskClaim{}, authority.claimErr
	}
	canonical, err := cleanup.CanonicalOwnerRiskClaimRequest(request)
	if err != nil {
		return cleanup.OwnerRiskClaim{}, err
	}
	claim, err := cleanup.SignOwnerRiskClaim(cleanup.OwnerRiskClaim{
		SchemaVersion: cleanup.CurrentOwnerRiskSchemaVersion,
		ClaimID:       "claim-01",
		Request:       request,
		RequestDigest: cleanup.Digest(canonical),
		State:         cleanup.OwnerRiskClaimed,
		StateOID:      strings.Repeat("3", 40),
		JournalOID:    strings.Repeat("4", 40),
		LeaseOID:      strings.Repeat("5", 40),
		Generation:    1,
		Fence:         1,
		Atomic:        true,
		Authority:     "cleanup-broker",
		IssuedAt:      time.Unix(150, 0).UTC(),
	}, authority.privateKey)
	if err != nil {
		return cleanup.OwnerRiskClaim{}, err
	}
	if authority.tamperClaim {
		claim.Fence++
	}
	return claim, nil
}

func (authority *signingOwnerRiskAuthority) RecheckOwnerRisk(_ context.Context, request cleanup.OwnerRiskFenceRequest) (cleanup.OwnerRiskFenceReadback, error) {
	authority.fenceRequests = append(authority.fenceRequests, request)
	if authority.fenceErr != nil {
		return cleanup.OwnerRiskFenceReadback{}, authority.fenceErr
	}
	canonical, err := cleanup.CanonicalOwnerRiskFenceRequest(request)
	if err != nil {
		return cleanup.OwnerRiskFenceReadback{}, err
	}
	return cleanup.SignOwnerRiskFenceReadback(cleanup.OwnerRiskFenceReadback{
		SchemaVersion: cleanup.CurrentOwnerRiskSchemaVersion,
		Request:       request,
		RequestDigest: cleanup.Digest(canonical),
		State:         cleanup.OwnerRiskClaimed,
		Authority:     "cleanup-broker",
		ObservedAt:    time.Unix(150, 0).UTC(),
	}, authority.privateKey)
}

func (authority *signingOwnerRiskAuthority) SettleOwnerRisk(_ context.Context, request cleanup.OwnerRiskSettlementRequest) (cleanup.OwnerRiskSettlement, error) {
	authority.settlementRequests = append(authority.settlementRequests, request)
	if authority.settlementErr != nil {
		return cleanup.OwnerRiskSettlement{}, authority.settlementErr
	}
	canonical, err := cleanup.CanonicalOwnerRiskSettlementRequest(request)
	if err != nil {
		return cleanup.OwnerRiskSettlement{}, err
	}
	return cleanup.SignOwnerRiskSettlement(cleanup.OwnerRiskSettlement{
		SchemaVersion: cleanup.CurrentOwnerRiskSchemaVersion,
		Request:       request,
		RequestDigest: cleanup.Digest(canonical),
		State:         request.Settlement,
		StateOID:      strings.Repeat("6", 40),
		JournalOID:    strings.Repeat("7", 40),
		LeaseOID:      strings.Repeat("8", 40),
		Authority:     "cleanup-broker",
		ObservedAt:    time.Unix(150, 0).UTC(),
	}, authority.privateKey)
}

func validQuarantineExecution(t *testing.T, approvalPrivateKey ed25519.PrivateKey) QuarantineExecutionRequest {
	t.Helper()
	now := time.Unix(150, 0).UTC()
	manifest := cleanup.Manifest{
		SchemaVersion:     cleanup.CurrentSchemaVersion,
		ManifestID:        "manifest-01",
		AccountID:         "account-01",
		RootID:            "source-parent",
		Namespace:         "backup/home",
		Mode:              cleanup.ModeQuarantine,
		MutationSemantics: cleanup.MutationSemanticsDriveOwnerRisk,
		QuarantineTarget: cleanup.QuarantineTarget{
			Provider:         "drive",
			AccountID:        "account-01",
			ParentID:         "quarantine-parent",
			EnrollmentDigest: strings.Repeat("a", 64),
		},
		CreatedAt:           now.Add(-time.Minute),
		ExpiresAt:           now.Add(time.Hour),
		Nonce:               "manifest-nonce-01",
		Budget:              cleanup.Budget{MaxObjects: 1, MaxBytes: 5},
		SourceInventoryHash: strings.Repeat("b", 64),
		FixtureDigest:       strings.Repeat("c", 64),
		Objects: []cleanup.Object{{
			ID:              "object-1",
			ParentID:        "source-parent",
			Name:            "object.bin",
			Path:            "object.bin",
			ObjectType:      cleanup.ObjectTypeFile,
			ContentHash:     "abc123",
			Size:            5,
			Provider:        "drive",
			AccountID:       "account-01",
			RootID:          "source-parent",
			Namespace:       "backup/home",
			Version:         "7",
			Generation:      "revision-1",
			ETag:            `"etag-1"`,
			ModifiedAt:      time.Unix(100, 0).UTC(),
			Depth:           1,
			Class:           cleanup.ClassOrphan,
			OwnershipMarker: "marker-01",
			RestoreEvidence: "restore-01",
		}},
	}
	validation, err := cleanup.ValidateManifest(manifest, now)
	if err != nil {
		t.Fatal(err)
	}
	approval := cleanup.Approval{
		SchemaVersion:     cleanup.CurrentApprovalSchemaVersion,
		ApprovalID:        "approval-01",
		ManifestDigest:    validation.ManifestDigest,
		AccountID:         manifest.AccountID,
		RootID:            manifest.RootID,
		Mode:              manifest.Mode,
		MutationSemantics: manifest.MutationSemantics,
		QuarantineTarget:  manifest.QuarantineTarget,
		MaxObjects:        manifest.Budget.MaxObjects,
		MaxBytes:          manifest.Budget.MaxBytes,
		ExpiresAt:         manifest.ExpiresAt,
		Nonce:             manifest.Nonce,
		Issuer:            "cleanup-signer",
		FixtureDigest:     manifest.FixtureDigest,
	}
	canonical, err := cleanup.CanonicalApproval(approval)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := cleanup.SignApproval(approval, approvalPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return QuarantineExecutionRequest{
		Repository: "n24q02m/private-control",
		Manifest:   manifest,
		Intent: cleanup.Intent{
			SchemaVersion: cleanup.CurrentApprovalSchemaVersion,
			IntentDigest:  cleanup.Digest(canonical),
			Approval:      approval,
			SignatureHex:  strings.ToLower(strings.TrimSpace(stringHex(signature))),
			State:         cleanup.ApprovalApproved,
			CreatedAt:     now.Add(-time.Minute),
		},
		IntentOID:        strings.Repeat("1", 40),
		StateExpectedOID: strings.Repeat("2", 40),

		JournalExpectedOID: "",
		LeaseExpectedOID:   "",
		Owner:              "executor-home",
		ExecutionID:        "execution-01",
		RequestID:          "request-01",
	}
}
func resignQuarantineExecution(t *testing.T, request QuarantineExecutionRequest, privateKey ed25519.PrivateKey) QuarantineExecutionRequest {
	t.Helper()
	validation, err := cleanup.ValidateManifest(request.Manifest, time.Unix(150, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	approval := request.Intent.Approval
	approval.ManifestDigest = validation.ManifestDigest
	approval.AccountID = request.Manifest.AccountID
	approval.RootID = request.Manifest.RootID
	approval.Mode = request.Manifest.Mode
	approval.MutationSemantics = request.Manifest.MutationSemantics
	approval.QuarantineTarget = request.Manifest.QuarantineTarget
	approval.MaxObjects = request.Manifest.Budget.MaxObjects
	approval.MaxBytes = request.Manifest.Budget.MaxBytes
	approval.ExpiresAt = request.Manifest.ExpiresAt
	approval.Nonce = request.Manifest.Nonce
	approval.FixtureDigest = request.Manifest.FixtureDigest
	canonical, err := cleanup.CanonicalApproval(approval)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := cleanup.SignApproval(approval, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	request.Intent.Approval = approval
	request.Intent.IntentDigest = cleanup.Digest(canonical)
	request.Intent.SignatureHex = stringHex(signature)
	return request
}

func stringHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, current := range value {
		encoded[index*2] = alphabet[current>>4]
		encoded[index*2+1] = alphabet[current&0x0f]
	}
	return string(encoded)
}

func executionDriveServer(t *testing.T, patchStatus int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var patchCalls atomic.Int32
	parent := "source-parent"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			version, etag := "7", `"etag-1"`
			if parent == "quarantine-parent" {
				version, etag = "8", `"etag-2"`
			}
			writeDriveFile(t, writer, parent, version, etag)
		case http.MethodPatch:
			patchCalls.Add(1)
			if patchStatus >= http.StatusBadRequest {
				writer.WriteHeader(patchStatus)
				return
			}
			parent = "quarantine-parent"
			writeDriveFile(t, writer, parent, "8", `"etag-2"`)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	return server, &patchCalls
}

func newTestQuarantineExecutor(t *testing.T, server *httptest.Server, authority *signingOwnerRiskAuthority, approvalPublicKey, authorityPublicKey ed25519.PublicKey) *QuarantineExecutor {
	t.Helper()
	executor, err := newQuarantineExecutor(
		server.Client(), server.URL+"/drive/v3/", "secret-token", authority,
		approvalPublicKey, authorityPublicKey, "cleanup-broker", func() time.Time { return time.Unix(150, 0).UTC() },
	)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func TestQuarantineExecutorRequiresAtomicSignedClaimBeforeProviderRead(t *testing.T) {
	approvalPublicKey, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, authorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server, patchCalls := executionDriveServer(t, http.StatusOK)
	defer server.Close()
	authority := &signingOwnerRiskAuthority{privateKey: authorityPrivateKey, claimErr: errors.New("replayed claim")}
	executor := newTestQuarantineExecutor(t, server, authority, approvalPublicKey, authorityPublicKey)
	if _, err := executor.Execute(context.Background(), validQuarantineExecution(t, approvalPrivateKey)); err == nil || !strings.Contains(err.Error(), "claim") {
		t.Fatalf("expected claim rejection, got %v", err)
	}
	if patchCalls.Load() != 0 {
		t.Fatalf("PATCH calls = %d, want zero", patchCalls.Load())
	}
}

func TestQuarantineExecutorConsumesClaimAndSettlesExactMove(t *testing.T) {
	approvalPublicKey, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, authorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server, patchCalls := executionDriveServer(t, http.StatusOK)
	defer server.Close()
	authority := &signingOwnerRiskAuthority{privateKey: authorityPrivateKey}
	executor := newTestQuarantineExecutor(t, server, authority, approvalPublicKey, authorityPublicKey)
	request := validQuarantineExecution(t, approvalPrivateKey)
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if patchCalls.Load() != 1 || len(authority.claimRequests) != 1 || len(authority.settlementRequests) != 1 {
		t.Fatalf("calls: patch=%d claim=%d settlement=%d", patchCalls.Load(), len(authority.claimRequests), len(authority.settlementRequests))
	}
	if result.Settlement != cleanup.OwnerRiskConsumed || authority.settlementRequests[0].Settlement != cleanup.OwnerRiskConsumed {
		t.Fatalf("unexpected settlement: %+v", result)
	}

	if _, err := executor.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("expected local replay rejection, got %v", err)
	}
	if patchCalls.Load() != 1 {
		t.Fatalf("replayed PATCH calls = %d, want one total", patchCalls.Load())
	}
}

func TestQuarantineExecutorRejectsTamperedClaimBeforeMutation(t *testing.T) {
	approvalPublicKey, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, authorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server, patchCalls := executionDriveServer(t, http.StatusOK)
	defer server.Close()
	authority := &signingOwnerRiskAuthority{privateKey: authorityPrivateKey, tamperClaim: true}
	executor := newTestQuarantineExecutor(t, server, authority, approvalPublicKey, authorityPublicKey)
	if _, err := executor.Execute(context.Background(), validQuarantineExecution(t, approvalPrivateKey)); !errors.Is(err, ErrSettlementUnknown) || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("claim verification error = %v, want signed settlement-unknown rejection", err)
	}
	if patchCalls.Load() != 0 {
		t.Fatalf("PATCH calls = %d, want zero", patchCalls.Load())
	}
}

func TestQuarantineExecutorRetainsClaimOnAmbiguousProviderSettlement(t *testing.T) {
	approvalPublicKey, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, authorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server, patchCalls := executionDriveServer(t, http.StatusBadGateway)
	defer server.Close()
	authority := &signingOwnerRiskAuthority{privateKey: authorityPrivateKey}
	executor := newTestQuarantineExecutor(t, server, authority, approvalPublicKey, authorityPublicKey)
	_, err = executor.Execute(context.Background(), validQuarantineExecution(t, approvalPrivateKey))
	if !errors.Is(err, ErrSettlementUnknown) {
		t.Fatalf("Execute() error = %v, want ErrSettlementUnknown", err)
	}
	if patchCalls.Load() != 1 || len(authority.settlementRequests) != 1 {
		t.Fatalf("calls: patch=%d settlement=%d", patchCalls.Load(), len(authority.settlementRequests))
	}
	if authority.settlementRequests[0].Settlement != cleanup.OwnerRiskNeedsReconciliation {
		t.Fatalf("settlement = %q, want needs_reconciliation", authority.settlementRequests[0].Settlement)
	}
}

func TestQuarantineExecutorReturnsReconciliationEvidenceWhenAuthoritySettlementFails(t *testing.T) {
	approvalPublicKey, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, authorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server, patchCalls := executionDriveServer(t, http.StatusOK)
	defer server.Close()
	settlementFailure := errors.New("authority unavailable after settlement request")
	authority := &signingOwnerRiskAuthority{
		privateKey:    authorityPrivateKey,
		settlementErr: settlementFailure,
	}
	executor := newTestQuarantineExecutor(t, server, authority, approvalPublicKey, authorityPublicKey)
	result, err := executor.Execute(context.Background(), validQuarantineExecution(t, approvalPrivateKey))
	if !errors.Is(err, ErrSettlementUnknown) || !errors.Is(err, settlementFailure) {
		t.Fatalf("settlement error = %v, want settlement unknown plus original cause", err)
	}
	if result.ClaimID == "" || result.Settlement != cleanup.OwnerRiskConsumed ||
		len(result.Moves) != 1 || patchCalls.Load() != 1 {
		t.Fatalf("reconciliation evidence = %+v, patch calls = %d", result, patchCalls.Load())
	}
}

func TestQuarantineExecutorRejectsFoldersUntilFencedEmptyFolderProtocolExists(t *testing.T) {
	approvalPublicKey, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, authorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server, patchCalls := executionDriveServer(t, http.StatusOK)
	defer server.Close()
	authority := &signingOwnerRiskAuthority{privateKey: authorityPrivateKey}
	executor := newTestQuarantineExecutor(t, server, authority, approvalPublicKey, authorityPublicKey)
	request := validQuarantineExecution(t, approvalPrivateKey)
	request.Manifest.Objects[0].ObjectType = cleanup.ObjectTypeFolder
	request.Manifest.Objects[0].ContentHash = ""
	request.Manifest.Objects[0].Size = 0
	request.Manifest.Objects[0].ChildrenComplete = true
	request.Manifest.Objects[0].SubtreeComplete = true
	request.Manifest.Objects[0].SubtreeWriterFence = "subtree-fence-01"
	request.Manifest.Objects[0].EmptyCheckIDs = []string{"empty-check-01", "empty-check-02"}
	request.Manifest.Budget.MaxBytes = 1
	request = resignQuarantineExecution(t, request, approvalPrivateKey)

	if _, err := executor.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "empty-folder protocol") {
		t.Fatalf("expected fenced protocol rejection, got %v", err)
	}
	if patchCalls.Load() != 0 || len(authority.claimRequests) != 0 {
		t.Fatalf("calls: patch=%d claim=%d, want zero", patchCalls.Load(), len(authority.claimRequests))
	}
}

func TestQuarantineExecutorKeepsFirstObjectExpiryInReconciliation(t *testing.T) {
	approvalPublicKey, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, authorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server, patchCalls := executionDriveServer(t, http.StatusOK)
	defer server.Close()
	authority := &signingOwnerRiskAuthority{privateKey: authorityPrivateKey}
	request := validQuarantineExecution(t, approvalPrivateKey)
	request.Manifest.ExpiresAt = time.Unix(151, 0).UTC()
	request = resignQuarantineExecution(t, request, approvalPrivateKey)
	var clockCalls atomic.Int32
	clock := func() time.Time {
		if clockCalls.Add(1) == 1 {
			return time.Unix(150, 0).UTC()
		}
		return time.Unix(151, 0).UTC()
	}
	executor, err := newQuarantineExecutor(
		server.Client(), server.URL+"/drive/v3/", "secret-token", authority,
		approvalPublicKey, authorityPublicKey, "cleanup-broker", clock,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), request)
	if !errors.Is(err, ErrSettlementUnknown) {
		t.Fatalf("Execute() error = %v, want reconciliation", err)
	}
	if result.Settlement != cleanup.OwnerRiskNeedsReconciliation || authority.settlementRequests[0].Settlement != cleanup.OwnerRiskNeedsReconciliation {
		t.Fatalf("expiry settlement released claim: result=%+v request=%+v", result, authority.settlementRequests[0])
	}
	if patchCalls.Load() != 0 {
		t.Fatalf("PATCH calls = %d, want zero", patchCalls.Load())
	}
}

func TestQuarantineExecutorKeepsFailedFirstFenceRecheckInReconciliation(t *testing.T) {
	approvalPublicKey, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, authorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server, patchCalls := executionDriveServer(t, http.StatusOK)
	defer server.Close()
	authority := &signingOwnerRiskAuthority{privateKey: authorityPrivateKey, fenceErr: errors.New("lease lost")}
	executor := newTestQuarantineExecutor(t, server, authority, approvalPublicKey, authorityPublicKey)
	result, err := executor.Execute(context.Background(), validQuarantineExecution(t, approvalPrivateKey))
	if !errors.Is(err, ErrSettlementUnknown) {
		t.Fatalf("Execute() error = %v, want reconciliation", err)
	}
	if result.Settlement != cleanup.OwnerRiskNeedsReconciliation || authority.settlementRequests[0].Settlement != cleanup.OwnerRiskNeedsReconciliation {
		t.Fatalf("fence failure settlement released claim: result=%+v request=%+v", result, authority.settlementRequests[0])
	}
	if patchCalls.Load() != 0 {
		t.Fatalf("PATCH calls = %d, want zero", patchCalls.Load())
	}
}

func TestQuarantineExecutorRejectsMultiObjectClaimBeforeAuthorityOrProvider(t *testing.T) {
	approvalPublicKey, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, authorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server, patchCalls := executionDriveServer(t, http.StatusOK)
	defer server.Close()
	authority := &signingOwnerRiskAuthority{privateKey: authorityPrivateKey}
	executor := newTestQuarantineExecutor(t, server, authority, approvalPublicKey, authorityPublicKey)
	request := validQuarantineExecution(t, approvalPrivateKey)
	second := request.Manifest.Objects[0]
	second.ID = "object-2"
	second.Name = "object-2.bin"
	second.Path = "object-2.bin"
	request.Manifest.Objects = append(request.Manifest.Objects, second)
	request.Manifest.Budget = cleanup.Budget{MaxObjects: 2, MaxBytes: 10}
	request = resignQuarantineExecution(t, request, approvalPrivateKey)

	if _, err := executor.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "exactly one leaf") {
		t.Fatalf("expected one-object claim rejection, got %v", err)
	}
	if patchCalls.Load() != 0 || len(authority.claimRequests) != 0 {
		t.Fatalf("calls: patch=%d claim=%d, want zero", patchCalls.Load(), len(authority.claimRequests))
	}
}

func TestQuarantineExecutorUsesGitOwnerRiskAuthorityEndToEnd(t *testing.T) {
	now := time.Unix(150, 0).UTC()
	approvalPublicKey, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, authorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	request := validQuarantineExecution(t, approvalPrivateKey)
	approvalRoot, err := cleanup.NewTrustRoot(
		"cleanup-approvers",
		request.Intent.Approval.Issuer,
		cleanup.CleanupTrustPurpose,
		approvalPublicKey,
		now.Add(-time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "authority.git")
	if err := protectedfs.EnsurePrivateDir(storePath); err != nil {
		t.Fatal(err)
	}
	store, err := cleanup.NewApprovalStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	store.Now = func() time.Time { return now }
	if _, err := store.Prepare(request.Intent.Approval); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(request.Intent); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ReadSnapshot(request.Intent.Approval.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	request.IntentOID = snapshot.IntentOID
	request.StateExpectedOID = snapshot.StateOID
	request.JournalExpectedOID = snapshot.JournalOID
	request.LeaseExpectedOID = snapshot.LeaseOID

	authority, err := cleanup.NewGitOwnerRiskAuthority(
		store,
		approvalRoot,
		authorityPrivateKey,
		"cleanup-broker",
		request.Repository,
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	server, patchCalls := executionDriveServer(t, http.StatusOK)
	defer server.Close()
	executor, err := newQuarantineExecutor(
		server.Client(),
		server.URL+"/drive/v3/",
		"secret-token",
		authority,
		approvalPublicKey,
		authorityPublicKey,
		"cleanup-broker",
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Settlement != cleanup.OwnerRiskConsumed || patchCalls.Load() != 1 {
		t.Fatalf("result=%+v patch calls=%d", result, patchCalls.Load())
	}
	finalSnapshot, err := store.ReadSnapshot(request.Intent.Approval.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if finalSnapshot.State.State != cleanup.OwnerRiskConsumed ||
		finalSnapshot.StateOID == snapshot.StateOID ||
		finalSnapshot.JournalOID == "" ||
		finalSnapshot.LeaseOID == "" {
		t.Fatalf("private Git authority did not settle all refs: %+v", finalSnapshot)
	}
}
