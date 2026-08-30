package driveapi

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

type QuarantineExecutionRequest struct {
	Repository         string           `json:"repository"`
	Manifest           cleanup.Manifest `json:"manifest"`
	Intent             cleanup.Intent   `json:"intent"`
	IntentOID          string           `json:"intent_oid"`
	StateExpectedOID   string           `json:"state_expected_oid"`
	JournalExpectedOID string           `json:"journal_expected_oid,omitempty"`
	LeaseExpectedOID   string           `json:"lease_expected_oid,omitempty"`
	Owner              string           `json:"owner"`
	ExecutionID        string           `json:"execution_id"`
	RequestID          string           `json:"request_id"`
}

type QuarantineExecutionResult struct {
	ClaimID       string       `json:"claim_id"`
	Settlement    string       `json:"settlement"`
	OutcomeDigest string       `json:"outcome_digest"`
	Moves         []MoveResult `json:"moves"`
}

type executionOutcome struct {
	Settlement       string       `json:"settlement"`
	FailureClass     string       `json:"failure_class"`
	ProviderRequests []string     `json:"provider_requests"`
	Moves            []MoveResult `json:"moves"`
}

// QuarantineExecutor is the sole exported Drive quarantine mutation path. It
// verifies the sealed cleanup approval and an atomically consumed, signed
// owner-risk claim before the unexported HTTP primitive can issue any PATCH.
type QuarantineExecutor struct {
	provider           *quarantineHTTPClient
	authority          cleanup.OwnerRiskAuthority
	approvalPublicKey  ed25519.PublicKey
	authorityPublicKey ed25519.PublicKey
	expectedAuthority  string
	now                func() time.Time

	mu             sync.Mutex
	consumedClaims map[string]struct{}
}

func NewQuarantineExecutor(
	client *http.Client,
	accessToken string,
	authority cleanup.OwnerRiskAuthority,
	approvalPublicKey ed25519.PublicKey,
	authorityPublicKey ed25519.PublicKey,
	expectedAuthority string,
) (*QuarantineExecutor, error) {
	return newQuarantineExecutor(
		client,
		googleDriveAPIBaseURL,
		accessToken,
		authority,
		approvalPublicKey,
		authorityPublicKey,
		expectedAuthority,
		time.Now,
	)
}

func newQuarantineExecutor(
	client *http.Client,
	endpoint string,
	accessToken string,
	authority cleanup.OwnerRiskAuthority,
	approvalPublicKey ed25519.PublicKey,
	authorityPublicKey ed25519.PublicKey,
	expectedAuthority string,
	now func() time.Time,
) (*QuarantineExecutor, error) {
	if authority == nil {
		return nil, errors.New("owner-risk cleanup authority is required")
	}
	if len(approvalPublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("cleanup approval public key is invalid")
	}
	if len(authorityPublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("owner-risk authority public key is invalid")
	}
	if strings.TrimSpace(expectedAuthority) == "" || strings.ContainsAny(expectedAuthority, "/\\\x00\r\n\t") {
		return nil, errors.New("owner-risk authority identity is invalid")
	}
	if now == nil {
		return nil, errors.New("cleanup clock is required")
	}
	provider, err := newQuarantineHTTPClient(client, endpoint, accessToken)
	if err != nil {
		return nil, err
	}
	return &QuarantineExecutor{
		provider:           provider,
		authority:          authority,
		approvalPublicKey:  append(ed25519.PublicKey(nil), approvalPublicKey...),
		authorityPublicKey: append(ed25519.PublicKey(nil), authorityPublicKey...),
		expectedAuthority:  expectedAuthority,
		now:                now,
		consumedClaims:     make(map[string]struct{}),
	}, nil
}

func (executor *QuarantineExecutor) Execute(ctx context.Context, request QuarantineExecutionRequest) (QuarantineExecutionResult, error) {
	if executor == nil || executor.provider == nil || executor.authority == nil || executor.now == nil {
		return QuarantineExecutionResult{}, errors.New("Drive quarantine executor is not configured")
	}
	if ctx == nil {
		return QuarantineExecutionResult{}, errors.New("context is nil")
	}
	now := executor.now().UTC()
	validation, err := cleanup.ValidateManifest(request.Manifest, now)
	if err != nil {
		return QuarantineExecutionResult{}, fmt.Errorf("cleanup manifest is invalid: %w", err)
	}
	for _, object := range request.Manifest.Objects {
		if object.ObjectType == cleanup.ObjectTypeFolder {
			return QuarantineExecutionResult{}, errors.New("Drive folder mutation requires the separate fenced empty-folder protocol")
		}
	}
	if validation.ObjectCount != 1 {
		return QuarantineExecutionResult{}, errors.New("Drive cleanup requires exactly one leaf per owner-risk claim until journal checkpoint recovery exists")
	}
	if request.Intent.State != cleanup.ApprovalApproved || request.Intent.IntentDigest == "" {
		return QuarantineExecutionResult{}, errors.New("cleanup intent is not approved")
	}
	approvalCanonical, err := cleanup.CanonicalApproval(request.Intent.Approval)
	if err != nil {
		return QuarantineExecutionResult{}, fmt.Errorf("cleanup approval is invalid: %w", err)
	}
	if request.Intent.IntentDigest != cleanup.Digest(approvalCanonical) {
		return QuarantineExecutionResult{}, errors.New("cleanup intent digest does not match its approval")
	}
	approvalSignature, err := hex.DecodeString(request.Intent.SignatureHex)
	if err != nil {
		return QuarantineExecutionResult{}, errors.New("cleanup approval signature is invalid")
	}
	if err := cleanup.VerifyApproval(request.Intent.Approval, approvalSignature, executor.approvalPublicKey, now); err != nil {
		return QuarantineExecutionResult{}, fmt.Errorf("cleanup approval verification failed: %w", err)
	}
	if err := cleanup.ValidateApprovalForManifest(request.Intent.Approval, request.Manifest, now); err != nil {
		return QuarantineExecutionResult{}, fmt.Errorf("cleanup approval does not bind the manifest: %w", err)
	}
	if request.Intent.Approval.ManifestDigest != validation.ManifestDigest {
		return QuarantineExecutionResult{}, errors.New("cleanup approval does not bind the canonical manifest digest")
	}

	identityHash, err := cleanup.QuarantineIdentityHash(request.Manifest.QuarantineTarget)
	if err != nil {
		return QuarantineExecutionResult{}, err
	}
	claimRequest := cleanup.OwnerRiskClaimRequest{
		SchemaVersion:      cleanup.CurrentOwnerRiskSchemaVersion,
		Repository:         request.Repository,
		ApprovalID:         request.Intent.Approval.ApprovalID,
		ManifestDigest:     validation.ManifestDigest,
		IntentRef:          cleanup.IntentRef(request.Intent.Approval.ApprovalID),
		IntentOID:          request.IntentOID,
		StateRef:           cleanup.StateRef(request.Intent.Approval.ApprovalID),
		StateExpectedOID:   request.StateExpectedOID,
		JournalRef:         cleanup.JournalRef(request.Intent.Approval.ApprovalID),
		JournalExpectedOID: request.JournalExpectedOID,
		Operation:          cleanup.OwnerRiskOperationQuarantine,
		LeaseRef:           cleanup.LeaseRef(identityHash),
		LeaseExpectedOID:   request.LeaseExpectedOID,
		MutationSemantics:  request.Manifest.MutationSemantics,
		QuarantineTarget:   request.Manifest.QuarantineTarget,
		MaxObjects:         validation.ObjectCount,
		MaxBytes:           validation.ByteCount,
		ExpiresAt:          request.Intent.Approval.ExpiresAt,
		Nonce:              request.Intent.Approval.Nonce,
		Owner:              request.Owner,
		ExecutionID:        request.ExecutionID,
		RequestID:          request.RequestID,
	}
	if _, err := cleanup.CanonicalOwnerRiskClaimRequest(claimRequest); err != nil {
		return QuarantineExecutionResult{}, fmt.Errorf("owner-risk claim request is invalid: %w", err)
	}
	claim, err := executor.authority.ClaimOwnerRisk(ctx, claimRequest)
	if err != nil {
		return QuarantineExecutionResult{}, fmt.Errorf("owner-risk claim failed: %w", err)
	}
	if err := cleanup.VerifyOwnerRiskClaim(claim, claimRequest, executor.authorityPublicKey, now); err != nil {
		return QuarantineExecutionResult{}, fmt.Errorf(
			"%w: owner-risk claim verification failed: %w",
			ErrSettlementUnknown,
			err,
		)
	}
	if claim.Authority != executor.expectedAuthority {
		return QuarantineExecutionResult{}, fmt.Errorf(
			"%w: owner-risk claim authority does not match the enrolled authority",
			ErrSettlementUnknown,
		)
	}
	claimCanonical, err := cleanup.CanonicalOwnerRiskClaim(claim)
	if err != nil {
		return QuarantineExecutionResult{}, fmt.Errorf("%w: canonicalize owner-risk claim: %w", ErrSettlementUnknown, err)
	}
	claimDigest := cleanup.Digest(claimCanonical)
	if !executor.consumeLocally(claim.RequestDigest) {
		return QuarantineExecutionResult{}, fmt.Errorf(
			"%w: owner-risk claim request was already consumed by this executor",
			ErrSettlementUnknown,
		)
	}

	moves := make([]MoveResult, 0, len(request.Manifest.Objects))
	providerRequests := make([]string, 0, len(request.Manifest.Objects))
	settlementState := cleanup.OwnerRiskConsumed
	failureClass := "none"
	var executionErr error
	for index, object := range request.Manifest.Objects {
		batchNow := executor.now().UTC()
		if !batchNow.Before(claim.Request.ExpiresAt) {
			executionErr = errors.New("owner-risk claim expired before the next Drive mutation")
			failureClass = "claim_expired"
			settlementState = cleanup.OwnerRiskNeedsReconciliation
			break
		}
		if err := executor.recheckClaim(ctx, claim, claimDigest, batchNow); err != nil {
			executionErr = fmt.Errorf("owner-risk fence recheck failed: %w", err)
			failureClass = "fence_recheck_failed"
			settlementState = cleanup.OwnerRiskNeedsReconciliation
			break
		}
		attemptID := providerAttemptID(request.RequestID, index, object.ID)
		move, moveErr := executor.provider.move(ctx, moveRequest{
			Expected:            object,
			DestinationParentID: request.Manifest.QuarantineTarget.ParentID,
			AttemptID:           attemptID,
		})
		if move.MutationAttempted {
			providerRequests = append(providerRequests, attemptID)
		}
		if moveErr != nil {
			executionErr = moveErr
			if errors.Is(moveErr, ErrSettlementUnknown) {
				settlementState = cleanup.OwnerRiskNeedsReconciliation
				failureClass = "provider_settlement_unknown"
			} else if len(moves) > 0 {
				settlementState = cleanup.OwnerRiskNeedsReconciliation
				failureClass = "partial_batch"
			} else {
				failureClass = "rejected_before_mutation"
			}
			break
		}
		moves = append(moves, move)
	}

	outcome := executionOutcome{
		Settlement:       settlementState,
		FailureClass:     failureClass,
		ProviderRequests: providerRequests,
		Moves:            moves,
	}
	outcomeData, err := json.Marshal(outcome)
	if err != nil {
		return QuarantineExecutionResult{}, fmt.Errorf("%w: encode cleanup outcome: %w", ErrSettlementUnknown, err)
	}
	outcomeDigest := cleanup.Digest(outcomeData)
	result := QuarantineExecutionResult{
		ClaimID:       claim.ClaimID,
		Settlement:    settlementState,
		OutcomeDigest: outcomeDigest,
		Moves:         moves,
	}
	settlementRequest := cleanup.OwnerRiskSettlementRequest{
		SchemaVersion:      cleanup.CurrentOwnerRiskSchemaVersion,
		ClaimID:            claim.ClaimID,
		ClaimDigest:        claimDigest,
		ApprovalID:         claim.Request.ApprovalID,
		ExecutionID:        claim.Request.ExecutionID,
		RequestID:          claim.Request.RequestID,
		StateExpectedOID:   claim.StateOID,
		JournalExpectedOID: claim.JournalOID,
		LeaseExpectedOID:   claim.LeaseOID,
		Settlement:         settlementState,
		OutcomeDigest:      outcomeDigest,
		ProviderRequests:   providerRequests,
	}
	settlementContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	settlement, settlementErr := executor.authority.SettleOwnerRisk(settlementContext, settlementRequest)
	if settlementErr != nil {
		return result, fmt.Errorf("%w: owner-risk settlement failed: %w", ErrSettlementUnknown, settlementErr)
	}
	if err := cleanup.VerifyOwnerRiskSettlement(settlement, settlementRequest, executor.authorityPublicKey, executor.now().UTC()); err != nil {
		return result, fmt.Errorf("%w: owner-risk settlement verification failed: %w", ErrSettlementUnknown, err)
	}
	if settlement.Authority != executor.expectedAuthority {
		return result, fmt.Errorf("%w: owner-risk settlement authority mismatch", ErrSettlementUnknown)
	}

	if executionErr != nil {
		if settlementState == cleanup.OwnerRiskNeedsReconciliation && !errors.Is(executionErr, ErrSettlementUnknown) {
			return result, fmt.Errorf("%w: partial cleanup batch requires reconciliation", ErrSettlementUnknown)
		}
		return result, executionErr
	}
	return result, nil
}

func (executor *QuarantineExecutor) consumeLocally(requestDigest string) bool {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if _, exists := executor.consumedClaims[requestDigest]; exists {
		return false
	}
	executor.consumedClaims[requestDigest] = struct{}{}
	return true
}

func (executor *QuarantineExecutor) recheckClaim(ctx context.Context, claim cleanup.OwnerRiskClaim, claimDigest string, now time.Time) error {
	request := cleanup.OwnerRiskFenceRequest{
		SchemaVersion: cleanup.CurrentOwnerRiskSchemaVersion,
		ClaimID:       claim.ClaimID,
		ClaimDigest:   claimDigest,
		ApprovalID:    claim.Request.ApprovalID,
		ExecutionID:   claim.Request.ExecutionID,
		RequestID:     claim.Request.RequestID,
		StateOID:      claim.StateOID,
		JournalOID:    claim.JournalOID,
		LeaseOID:      claim.LeaseOID,
		Generation:    claim.Generation,
		Fence:         claim.Fence,
	}
	readback, err := executor.authority.RecheckOwnerRisk(ctx, request)
	if err != nil {
		return err
	}
	if err := cleanup.VerifyOwnerRiskFenceReadback(readback, request, executor.authorityPublicKey, now); err != nil {
		return err
	}
	if readback.Authority != executor.expectedAuthority {
		return errors.New("owner-risk fence authority does not match the enrolled authority")
	}
	return nil
}

func providerAttemptID(requestID string, index int, objectID string) string {
	material := []byte(fmt.Sprintf("%s\x00%d\x00%s", requestID, index, objectID))
	return "drive-" + cleanup.Digest(material)[:32]
}
