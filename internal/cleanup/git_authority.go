package cleanup

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// OwnerRiskStateMetadata is persisted with the approval lifecycle state. The
// complete request is required to reconstruct the signed claim from current
// authoritative ref OIDs without trusting a client-provided claim body.
type OwnerRiskStateMetadata struct {
	ClaimID          string                `json:"claim_id"`
	Request          OwnerRiskClaimRequest `json:"request"`
	RequestDigest    string                `json:"request_digest"`
	ClaimDigest      string                `json:"claim_digest,omitempty"`
	OutcomeDigest    string                `json:"outcome_digest,omitempty"`
	ProviderRequests []string              `json:"provider_requests,omitempty"`
	Generation       uint64                `json:"generation"`
	Fence            uint64                `json:"fence"`
	Authority        string                `json:"authority"`
	IssuedAt         time.Time             `json:"issued_at"`
}

// OwnerRiskJournalRecord is the hash-linked runtime record for one atomic
// claim or settlement. It is deliberately separate from the preview journal,
// which does not carry claim, request, generation, or fence bindings.
type OwnerRiskJournalRecord struct {
	SchemaVersion    int       `json:"schema_version"`
	ApprovalID       string    `json:"approval_id"`
	ClaimID          string    `json:"claim_id"`
	RequestDigest    string    `json:"request_digest"`
	ClaimDigest      string    `json:"claim_digest,omitempty"`
	OutcomeDigest    string    `json:"outcome_digest,omitempty"`
	ProviderRequests []string  `json:"provider_requests,omitempty"`
	State            string    `json:"state"`
	Generation       uint64    `json:"generation"`
	Fence            uint64    `json:"fence"`
	Authority        string    `json:"authority"`
	Timestamp        time.Time `json:"timestamp"`
	PreviousOID      string    `json:"previous_oid,omitempty"`
}

// GitOwnerRiskAuthority is the production state machine behind the cleanup
// authority protocol. Every lifecycle mutation advances state, journal, and
// destination lease through one private-Git CAS transaction.
type GitOwnerRiskAuthority struct {
	store        *ApprovalStore
	approvalRoot TrustRoot
	privateKey   ed25519.PrivateKey
	authority    string
	repository   string
	now          func() time.Time
}

func NewGitOwnerRiskAuthority(
	store *ApprovalStore,
	approvalRoot TrustRoot,
	privateKey ed25519.PrivateKey,
	authority string,
	repository string,
	now func() time.Time,
) (*GitOwnerRiskAuthority, error) {
	if store == nil || store.RuntimeTransport == nil {
		return nil, errors.New("runtime approval store is required")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("owner-risk authority private key is invalid")
	}
	if err := validateOpaqueTransactionField(authority, "authority"); err != nil {
		return nil, err
	}
	if err := ValidateOwnerRiskRepository(repository); err != nil {
		return nil, fmt.Errorf("owner-risk authority repository is invalid: %w", err)
	}
	if now == nil {
		return nil, errors.New("owner-risk authority clock is required")
	}
	current := now().UTC()
	if current.IsZero() {
		return nil, errors.New("owner-risk authority clock returned zero time")
	}
	if _, err := approvalRoot.PublicKeyForPurpose(CleanupTrustPurpose, approvalRoot.Issuer, current); err != nil {
		return nil, fmt.Errorf("cleanup approval trust root is invalid: %w", err)
	}
	return &GitOwnerRiskAuthority{
		store:        store,
		approvalRoot: approvalRoot,
		privateKey:   append(ed25519.PrivateKey(nil), privateKey...),
		authority:    authority,
		repository:   repository,
		now:          now,
	}, nil
}

func (authority *GitOwnerRiskAuthority) SnapshotOwnerRisk(ctx context.Context, request OwnerRiskSnapshotRequest) (OwnerRiskSnapshot, error) {
	if err := authority.validateContext(ctx); err != nil {
		return OwnerRiskSnapshot{}, err
	}
	requestCanonical, err := CanonicalOwnerRiskSnapshotRequest(request)
	if err != nil {
		return OwnerRiskSnapshot{}, err
	}
	if request.Repository != authority.repository {
		return OwnerRiskSnapshot{}, errors.New("owner-risk snapshot repository does not match authority repository")
	}
	observedAt := authority.now().UTC()
	snapshot, err := authority.store.ReadSnapshot(request.ApprovalID)
	if err != nil {
		return OwnerRiskSnapshot{}, err
	}
	if snapshot.State.State != ApprovalApproved || snapshot.State.OwnerRisk != nil ||
		snapshot.Sealed.Approval.ManifestDigest != request.ManifestDigest ||
		snapshot.Sealed.Approval.QuarantineTarget != request.QuarantineTarget {
		return OwnerRiskSnapshot{}, errors.New("owner-risk snapshot request does not match an approved cleanup intent")
	}
	signature, err := hex.DecodeString(snapshot.Sealed.SignatureHex)
	if err != nil {
		return OwnerRiskSnapshot{}, errors.New("cleanup approval signature is invalid hex")
	}
	if err := VerifyApprovalAgainstTrustRoot(snapshot.Sealed.Approval, signature, authority.approvalRoot, observedAt); err != nil {
		return OwnerRiskSnapshot{}, fmt.Errorf("cleanup approval trust verification failed: %w", err)
	}
	readback := OwnerRiskSnapshot{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		Request:       request,
		RequestDigest: Digest(requestCanonical),
		Intent:        snapshot.Intent,
		IntentOID:     snapshot.IntentOID,
		StateOID:      snapshot.StateOID,
		JournalOID:    snapshot.JournalOID,
		LeaseOID:      snapshot.LeaseOID,
		Authority:     authority.authority,
		ObservedAt:    observedAt,
	}
	return SignOwnerRiskSnapshot(readback, authority.privateKey)
}

func (authority *GitOwnerRiskAuthority) ClaimOwnerRisk(ctx context.Context, request OwnerRiskClaimRequest) (OwnerRiskClaim, error) {
	if err := authority.validateContext(ctx); err != nil {
		return OwnerRiskClaim{}, err
	}
	requestCanonical, err := CanonicalOwnerRiskClaimRequest(request)
	if err != nil {
		return OwnerRiskClaim{}, err
	}
	now := authority.now().UTC()
	if request.Repository != authority.repository {
		return OwnerRiskClaim{}, errors.New("owner-risk request repository does not match authority repository")
	}
	if !now.Before(request.ExpiresAt) {
		return OwnerRiskClaim{}, errors.New("owner-risk request is expired")
	}
	snapshot, err := authority.store.ReadSnapshot(request.ApprovalID)
	if err != nil {
		return OwnerRiskClaim{}, err
	}
	if err := authority.validateInitialClaimSnapshot(snapshot, request, now); err != nil {
		return OwnerRiskClaim{}, err
	}
	generation, fence, err := authority.nextLeaseFence(snapshot, request)
	if err != nil {
		return OwnerRiskClaim{}, err
	}
	requestDigest := Digest(requestCanonical)
	claimID := "claim-" + requestDigest[:32]
	metadata := &OwnerRiskStateMetadata{
		ClaimID:       claimID,
		Request:       request,
		RequestDigest: requestDigest,
		Generation:    generation,
		Fence:         fence,
		Authority:     authority.authority,
		IssuedAt:      now,
	}
	identityHash, err := QuarantineIdentityHash(request.QuarantineTarget)
	if err != nil {
		return OwnerRiskClaim{}, err
	}
	state := StateRecord{
		SchemaVersion: uint64(CurrentApprovalSchemaVersion),
		ApprovalID:    request.ApprovalID,
		IntentRef:     request.IntentRef,
		IntentOID:     request.IntentOID,
		State:         OwnerRiskClaimed,
		ExecutionID:   request.ExecutionID,
		Timestamp:     now,
		PreviousOID:   request.StateExpectedOID,
		OwnerRisk:     metadata,
	}
	journal := OwnerRiskJournalRecord{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		ApprovalID:    request.ApprovalID,
		ClaimID:       claimID,
		RequestDigest: requestDigest,
		State:         OwnerRiskClaimed,
		Generation:    generation,
		Fence:         fence,
		Authority:     authority.authority,
		Timestamp:     now,
		PreviousOID:   request.JournalExpectedOID,
	}
	lease := LeaseRecord{
		SchemaVersion: uint64(CurrentApprovalSchemaVersion),
		IdentityHash:  identityHash,
		ApprovalID:    request.ApprovalID,
		Owner:         request.Owner,
		ExecutionID:   request.ExecutionID,
		ClaimID:       claimID,
		RequestDigest: requestDigest,
		Authority:     authority.authority,
		Generation:    generation,
		Fence:         fence,
		State:         LeaseClaimed,
		ExpiresAt:     request.ExpiresAt,
		Timestamp:     now,
		PreviousOID:   request.LeaseExpectedOID,
	}
	transition, err := runtimeTransition(request, state, journal, lease)
	if err != nil {
		return OwnerRiskClaim{}, err
	}
	if err := authority.validateContext(ctx); err != nil {
		return OwnerRiskClaim{}, err
	}
	result, err := authority.store.RuntimeTransport.CommitRuntimeTransition(transition)
	if err != nil {
		return OwnerRiskClaim{}, fmt.Errorf("atomic owner-risk claim: %w", err)
	}
	claim := OwnerRiskClaim{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		ClaimID:       claimID,
		Request:       request,
		RequestDigest: requestDigest,
		State:         OwnerRiskClaimed,
		StateOID:      result.StateOID,
		JournalOID:    result.JournalOID,
		LeaseOID:      result.LeaseOID,
		Generation:    generation,
		Fence:         fence,
		Atomic:        true,
		Authority:     authority.authority,
		IssuedAt:      now,
	}
	return SignOwnerRiskClaim(claim, authority.privateKey)
}

func (authority *GitOwnerRiskAuthority) RecheckOwnerRisk(ctx context.Context, request OwnerRiskFenceRequest) (OwnerRiskFenceReadback, error) {
	if err := authority.validateContext(ctx); err != nil {
		return OwnerRiskFenceReadback{}, err
	}
	requestCanonical, err := CanonicalOwnerRiskFenceRequest(request)
	if err != nil {
		return OwnerRiskFenceReadback{}, err
	}
	claim, claimDigest, err := authority.currentClaim(request.ApprovalID)
	if err != nil {
		return OwnerRiskFenceReadback{}, err
	}
	if !authority.now().UTC().Before(claim.Request.ExpiresAt) {
		return OwnerRiskFenceReadback{}, errors.New("owner-risk claim expired before fence recheck")
	}
	if request.ClaimID != claim.ClaimID || request.ClaimDigest != claimDigest ||
		request.ExecutionID != claim.Request.ExecutionID || request.RequestID != claim.Request.RequestID ||
		request.StateOID != claim.StateOID || request.JournalOID != claim.JournalOID || request.LeaseOID != claim.LeaseOID ||
		request.Generation != claim.Generation || request.Fence != claim.Fence {
		return OwnerRiskFenceReadback{}, errors.New("owner-risk fence request does not match current authoritative claim")
	}
	observedAt := authority.now().UTC()
	readback := OwnerRiskFenceReadback{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		Request:       request,
		RequestDigest: Digest(requestCanonical),
		State:         OwnerRiskClaimed,
		Authority:     authority.authority,
		ObservedAt:    observedAt,
	}
	return SignOwnerRiskFenceReadback(readback, authority.privateKey)
}

func (authority *GitOwnerRiskAuthority) SettleOwnerRisk(ctx context.Context, request OwnerRiskSettlementRequest) (OwnerRiskSettlement, error) {
	if err := authority.validateContext(ctx); err != nil {
		return OwnerRiskSettlement{}, err
	}
	requestCanonical, err := CanonicalOwnerRiskSettlementRequest(request)
	if err != nil {
		return OwnerRiskSettlement{}, err
	}
	claim, claimDigest, err := authority.currentClaim(request.ApprovalID)
	if err != nil {
		return OwnerRiskSettlement{}, err
	}
	if request.ClaimID != claim.ClaimID || request.ClaimDigest != claimDigest ||
		request.ExecutionID != claim.Request.ExecutionID || request.RequestID != claim.Request.RequestID ||
		request.StateExpectedOID != claim.StateOID || request.JournalExpectedOID != claim.JournalOID ||
		request.LeaseExpectedOID != claim.LeaseOID {
		return OwnerRiskSettlement{}, errors.New("owner-risk settlement request does not match current authoritative claim")
	}
	if claim.Fence == math.MaxUint64 {
		return OwnerRiskSettlement{}, errors.New("owner-risk settlement fence exhausted")
	}
	now := authority.now().UTC()
	metadata := &OwnerRiskStateMetadata{
		ClaimID:          claim.ClaimID,
		Request:          claim.Request,
		RequestDigest:    claim.RequestDigest,
		ClaimDigest:      claimDigest,
		OutcomeDigest:    request.OutcomeDigest,
		ProviderRequests: append([]string(nil), request.ProviderRequests...),
		Generation:       claim.Generation,
		Fence:            claim.Fence,
		Authority:        authority.authority,
		IssuedAt:         claim.IssuedAt,
	}
	state := StateRecord{
		SchemaVersion: uint64(CurrentApprovalSchemaVersion),
		ApprovalID:    claim.Request.ApprovalID,
		IntentRef:     claim.Request.IntentRef,
		IntentOID:     claim.Request.IntentOID,
		State:         request.Settlement,
		ExecutionID:   claim.Request.ExecutionID,
		Timestamp:     now,
		PreviousOID:   claim.StateOID,
		OwnerRisk:     metadata,
	}
	journal := OwnerRiskJournalRecord{
		SchemaVersion:    CurrentOwnerRiskSchemaVersion,
		ApprovalID:       claim.Request.ApprovalID,
		ClaimID:          claim.ClaimID,
		RequestDigest:    claim.RequestDigest,
		ClaimDigest:      claimDigest,
		OutcomeDigest:    request.OutcomeDigest,
		ProviderRequests: append([]string(nil), request.ProviderRequests...),
		State:            request.Settlement,
		Generation:       claim.Generation,
		Fence:            claim.Fence + 1,
		Authority:        authority.authority,
		Timestamp:        now,
		PreviousOID:      claim.JournalOID,
	}
	identityHash, err := QuarantineIdentityHash(claim.Request.QuarantineTarget)
	if err != nil {
		return OwnerRiskSettlement{}, err
	}
	lease := LeaseRecord{
		SchemaVersion:    uint64(CurrentApprovalSchemaVersion),
		IdentityHash:     identityHash,
		ApprovalID:       claim.Request.ApprovalID,
		Owner:            claim.Request.Owner,
		ExecutionID:      claim.Request.ExecutionID,
		ClaimID:          claim.ClaimID,
		RequestDigest:    claim.RequestDigest,
		ClaimDigest:      claimDigest,
		OutcomeDigest:    request.OutcomeDigest,
		ProviderRequests: append([]string(nil), request.ProviderRequests...),
		Authority:        authority.authority,
		Generation:       claim.Generation,
		Fence:            claim.Fence + 1,
		State:            request.Settlement,
		ExpiresAt:        claim.Request.ExpiresAt,
		Timestamp:        now,
		PreviousOID:      claim.LeaseOID,
	}
	transition, err := runtimeTransition(claim.Request, state, journal, lease)
	if err != nil {
		return OwnerRiskSettlement{}, err
	}
	transition.State.ExpectedOID = request.StateExpectedOID
	transition.Journal.ExpectedOID = request.JournalExpectedOID
	transition.Lease.ExpectedOID = request.LeaseExpectedOID
	if err := authority.validateContext(ctx); err != nil {
		return OwnerRiskSettlement{}, err
	}
	result, err := authority.store.RuntimeTransport.CommitRuntimeTransition(transition)
	if err != nil {
		return OwnerRiskSettlement{}, fmt.Errorf("atomic owner-risk settlement: %w", err)
	}
	settlement := OwnerRiskSettlement{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		Request:       request,
		RequestDigest: Digest(requestCanonical),
		State:         request.Settlement,
		StateOID:      result.StateOID,
		JournalOID:    result.JournalOID,
		LeaseOID:      result.LeaseOID,
		Authority:     authority.authority,
		ObservedAt:    now,
	}
	return SignOwnerRiskSettlement(settlement, authority.privateKey)
}

func (authority *GitOwnerRiskAuthority) validateContext(ctx context.Context) error {
	if authority == nil || authority.store == nil || authority.store.RuntimeTransport == nil || authority.now == nil {
		return errors.New("owner-risk authority is not configured")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (authority *GitOwnerRiskAuthority) validateInitialClaimSnapshot(snapshot ApprovalSnapshot, request OwnerRiskClaimRequest, now time.Time) error {
	if snapshot.State.State != ApprovalApproved || snapshot.State.OwnerRisk != nil {
		return fmt.Errorf("cleanup approval cannot be claimed from state %q", snapshot.State.State)
	}
	if snapshot.IntentOID != request.IntentOID || snapshot.StateOID != request.StateExpectedOID ||
		snapshot.JournalOID != request.JournalExpectedOID || snapshot.LeaseOID != request.LeaseExpectedOID {
		return errors.New("owner-risk claim expected refs do not match current private Git readback")
	}
	return authority.validateApprovalRequest(snapshot, request, now)
}

func (authority *GitOwnerRiskAuthority) validateApprovalRequest(snapshot ApprovalSnapshot, request OwnerRiskClaimRequest, verificationTime time.Time) error {
	if snapshot.IntentOID != request.IntentOID || snapshot.State.IntentOID != request.IntentOID ||
		snapshot.State.IntentRef != request.IntentRef || snapshot.Sealed.Approval.ApprovalID != request.ApprovalID {
		return errors.New("owner-risk request does not bind the sealed approval lineage")
	}
	signature, err := hex.DecodeString(snapshot.Sealed.SignatureHex)
	if err != nil {
		return errors.New("cleanup approval signature is invalid hex")
	}
	approval := snapshot.Sealed.Approval
	if err := VerifyApprovalAgainstTrustRoot(approval, signature, authority.approvalRoot, verificationTime); err != nil {
		return fmt.Errorf("cleanup approval trust verification failed: %w", err)
	}
	if approval.ManifestDigest != request.ManifestDigest ||
		approval.MutationSemantics != request.MutationSemantics ||
		approval.QuarantineTarget != request.QuarantineTarget ||
		approval.MaxObjects != request.MaxObjects || approval.MaxBytes != request.MaxBytes ||
		!approval.ExpiresAt.Equal(request.ExpiresAt) || approval.Nonce != request.Nonce {
		return errors.New("owner-risk request does not match approval scope, target, budget, expiry, and nonce")
	}
	return nil
}

func (authority *GitOwnerRiskAuthority) nextLeaseFence(snapshot ApprovalSnapshot, request OwnerRiskClaimRequest) (uint64, uint64, error) {
	if snapshot.LeaseOID == "" {
		return 1, 1, nil
	}
	lease, err := authority.readLease(snapshot.LeaseOID)
	if err != nil {
		return 0, 0, err
	}
	identityHash, err := QuarantineIdentityHash(request.QuarantineTarget)
	if err != nil {
		return 0, 0, err
	}
	if lease.SchemaVersion != uint64(CurrentApprovalSchemaVersion) || lease.IdentityHash != identityHash ||
		lease.Authority != authority.authority || lease.Generation == 0 || lease.Fence == 0 {
		return 0, 0, errors.New("existing destination lease lineage is invalid")
	}
	switch lease.State {
	case LeaseNeedsReconciliation:
		return 0, 0, errors.New("destination lease requires reconciliation before another claim")
	case LeaseClaimed:
		return 0, 0, errors.New("destination lease is already claimed")
	case LeaseConsumed:
		if err := authority.validateConsumedLeaseLineage(snapshot.LeaseOID, lease); err != nil {
			return 0, 0, err
		}
	default:
		return 0, 0, fmt.Errorf("destination lease state %q cannot be claimed", lease.State)
	}
	if lease.Generation == math.MaxUint64 || lease.Fence == math.MaxUint64 {
		return 0, 0, errors.New("destination lease generation or fence exhausted")
	}
	return lease.Generation + 1, lease.Fence + 1, nil
}

func (authority *GitOwnerRiskAuthority) validateConsumedLeaseLineage(leaseOID string, lease LeaseRecord) error {
	for name, value := range map[string]string{
		"approval ID": lease.ApprovalID,
		"owner":       lease.Owner,
		"execution":   lease.ExecutionID,
		"claim ID":    lease.ClaimID,
	} {
		if err := validateOpaqueTransactionField(value, name); err != nil {
			return errors.New("existing destination lease lineage is invalid")
		}
	}
	for name, digest := range map[string]string{
		"request digest": lease.RequestDigest,
		"claim digest":   lease.ClaimDigest,
		"outcome digest": lease.OutcomeDigest,
	} {
		if err := validateSHA256Hex(digest, name); err != nil {
			return errors.New("existing destination lease lineage is invalid")
		}
	}
	if !gitOIDPattern.MatchString(lease.PreviousOID) || lease.Timestamp.IsZero() || lease.ExpiresAt.IsZero() {
		return errors.New("existing destination lease lineage is invalid")
	}
	snapshot, err := authority.store.ReadSnapshot(lease.ApprovalID)
	if err != nil {
		return fmt.Errorf("read previous destination lease owner: %w", err)
	}
	metadata := snapshot.State.OwnerRisk
	if snapshot.LeaseOID != leaseOID || snapshot.State.State != OwnerRiskConsumed || metadata == nil ||
		snapshot.State.PreviousOID == "" || !gitOIDPattern.MatchString(snapshot.State.PreviousOID) ||
		metadata.ClaimID != lease.ClaimID || metadata.RequestDigest != lease.RequestDigest ||
		metadata.ClaimDigest != lease.ClaimDigest || metadata.OutcomeDigest != lease.OutcomeDigest ||
		metadata.Generation != lease.Generation || metadata.Fence == math.MaxUint64 ||
		metadata.Fence+1 != lease.Fence || metadata.Authority != authority.authority ||
		!sameStrings(metadata.ProviderRequests, lease.ProviderRequests) ||
		!snapshot.State.Timestamp.Equal(lease.Timestamp) {
		return errors.New("existing destination lease lineage is invalid")
	}
	requestCanonical, err := CanonicalOwnerRiskClaimRequest(metadata.Request)
	if err != nil || metadata.RequestDigest != Digest(requestCanonical) ||
		metadata.ClaimID != "claim-"+metadata.RequestDigest[:32] ||
		metadata.Request.Owner != lease.Owner || metadata.Request.ExecutionID != lease.ExecutionID ||
		!metadata.Request.ExpiresAt.Equal(lease.ExpiresAt) || !metadata.IssuedAt.Before(metadata.Request.ExpiresAt) {
		return errors.New("existing destination lease lineage is invalid")
	}
	if err := authority.validateApprovalRequest(snapshot, metadata.Request, metadata.IssuedAt); err != nil {
		return errors.New("existing destination lease lineage is invalid")
	}
	journal, err := authority.readJournal(snapshot.JournalOID)
	if err != nil {
		return errors.New("existing destination lease lineage is invalid")
	}
	if journal.SchemaVersion != CurrentOwnerRiskSchemaVersion || journal.State != OwnerRiskConsumed ||
		journal.ApprovalID != lease.ApprovalID || journal.ClaimID != lease.ClaimID ||
		journal.RequestDigest != lease.RequestDigest || journal.ClaimDigest != lease.ClaimDigest ||
		journal.OutcomeDigest != lease.OutcomeDigest || journal.Generation != lease.Generation ||
		journal.Fence != lease.Fence || journal.Authority != authority.authority ||
		!gitOIDPattern.MatchString(journal.PreviousOID) || !sameStrings(journal.ProviderRequests, lease.ProviderRequests) ||
		!journal.Timestamp.Equal(lease.Timestamp) {
		return errors.New("existing destination lease lineage is invalid")
	}
	priorClaim := OwnerRiskClaim{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		ClaimID:       metadata.ClaimID,
		Request:       metadata.Request,
		RequestDigest: metadata.RequestDigest,
		State:         OwnerRiskClaimed,
		StateOID:      snapshot.State.PreviousOID,
		JournalOID:    journal.PreviousOID,
		LeaseOID:      lease.PreviousOID,
		Generation:    metadata.Generation,
		Fence:         metadata.Fence,
		Atomic:        true,
		Authority:     authority.authority,
		IssuedAt:      metadata.IssuedAt,
	}
	claimCanonical, err := CanonicalOwnerRiskClaim(priorClaim)
	if err != nil || Digest(claimCanonical) != lease.ClaimDigest {
		return errors.New("existing destination lease lineage is invalid")
	}
	return nil
}

func (authority *GitOwnerRiskAuthority) currentClaim(approvalID string) (OwnerRiskClaim, string, error) {
	snapshot, err := authority.store.ReadSnapshot(approvalID)
	if err != nil {
		return OwnerRiskClaim{}, "", err
	}
	metadata := snapshot.State.OwnerRisk
	if snapshot.State.State != OwnerRiskClaimed || metadata == nil {
		return OwnerRiskClaim{}, "", fmt.Errorf("owner-risk approval is not currently claimed: %q", snapshot.State.State)
	}
	if metadata.Authority != authority.authority || metadata.ClaimID == "" || metadata.Generation == 0 || metadata.Fence == 0 ||
		metadata.IssuedAt.IsZero() || !metadata.IssuedAt.Before(metadata.Request.ExpiresAt) ||
		metadata.ClaimDigest != "" || metadata.OutcomeDigest != "" || len(metadata.ProviderRequests) != 0 {
		return OwnerRiskClaim{}, "", errors.New("claimed owner-risk state metadata is invalid")
	}
	requestCanonical, err := CanonicalOwnerRiskClaimRequest(metadata.Request)
	if err != nil || metadata.RequestDigest != Digest(requestCanonical) {
		return OwnerRiskClaim{}, "", errors.New("claimed owner-risk request digest is invalid")
	}
	if snapshot.State.PreviousOID != metadata.Request.StateExpectedOID || snapshot.State.ExecutionID != metadata.Request.ExecutionID ||
		snapshot.IntentOID != metadata.Request.IntentOID || !snapshot.State.Timestamp.Equal(metadata.IssuedAt) {
		return OwnerRiskClaim{}, "", errors.New("claimed owner-risk state lineage is invalid")
	}
	if err := authority.validateApprovalRequest(snapshot, metadata.Request, metadata.IssuedAt); err != nil {
		return OwnerRiskClaim{}, "", err
	}
	journal, err := authority.readJournal(snapshot.JournalOID)
	if err != nil {
		return OwnerRiskClaim{}, "", err
	}
	lease, err := authority.readLease(snapshot.LeaseOID)
	if err != nil {
		return OwnerRiskClaim{}, "", err
	}
	identityHash, err := QuarantineIdentityHash(metadata.Request.QuarantineTarget)
	if err != nil {
		return OwnerRiskClaim{}, "", err
	}
	if journal.SchemaVersion != CurrentOwnerRiskSchemaVersion || journal.State != OwnerRiskClaimed ||
		journal.ApprovalID != approvalID || journal.ClaimID != metadata.ClaimID ||
		journal.RequestDigest != metadata.RequestDigest || journal.ClaimDigest != "" || journal.OutcomeDigest != "" ||
		len(journal.ProviderRequests) != 0 || journal.Generation != metadata.Generation ||
		journal.Fence != metadata.Fence || journal.Authority != authority.authority ||
		journal.PreviousOID != metadata.Request.JournalExpectedOID || !journal.Timestamp.Equal(metadata.IssuedAt) {
		return OwnerRiskClaim{}, "", errors.New("claimed owner-risk journal lineage is invalid")
	}
	if lease.SchemaVersion != uint64(CurrentApprovalSchemaVersion) || lease.State != LeaseClaimed ||
		lease.IdentityHash != identityHash || lease.ApprovalID != approvalID || lease.Owner != metadata.Request.Owner ||
		lease.ExecutionID != metadata.Request.ExecutionID || lease.ClaimID != metadata.ClaimID ||
		lease.RequestDigest != metadata.RequestDigest || lease.ClaimDigest != "" || lease.OutcomeDigest != "" ||
		len(lease.ProviderRequests) != 0 || lease.Authority != authority.authority ||
		lease.Generation != metadata.Generation || lease.Fence != metadata.Fence ||
		lease.PreviousOID != metadata.Request.LeaseExpectedOID || !lease.ExpiresAt.Equal(metadata.Request.ExpiresAt) ||
		!lease.Timestamp.Equal(metadata.IssuedAt) {
		return OwnerRiskClaim{}, "", errors.New("claimed destination lease lineage is invalid")
	}
	claim := OwnerRiskClaim{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		ClaimID:       metadata.ClaimID,
		Request:       metadata.Request,
		RequestDigest: metadata.RequestDigest,
		State:         OwnerRiskClaimed,
		StateOID:      snapshot.StateOID,
		JournalOID:    snapshot.JournalOID,
		LeaseOID:      snapshot.LeaseOID,
		Generation:    metadata.Generation,
		Fence:         metadata.Fence,
		Atomic:        true,
		Authority:     authority.authority,
		IssuedAt:      metadata.IssuedAt,
	}
	canonical, err := CanonicalOwnerRiskClaim(claim)
	if err != nil {
		return OwnerRiskClaim{}, "", err
	}
	return claim, Digest(canonical), nil
}

func (authority *GitOwnerRiskAuthority) readLease(oid string) (LeaseRecord, error) {
	if !gitOIDPattern.MatchString(oid) {
		return LeaseRecord{}, errors.New("destination lease ref is absent or invalid")
	}
	data, err := authority.store.RuntimeTransport.ReadBlob(oid)
	if err != nil {
		return LeaseRecord{}, err
	}
	var lease LeaseRecord
	if err := decodeStrictJSONRecord(data, &lease); err != nil {
		return LeaseRecord{}, fmt.Errorf("decode destination lease: %w", err)
	}
	return lease, nil
}

func (authority *GitOwnerRiskAuthority) readJournal(oid string) (OwnerRiskJournalRecord, error) {
	if !gitOIDPattern.MatchString(oid) {
		return OwnerRiskJournalRecord{}, errors.New("owner-risk journal ref is absent or invalid")
	}
	data, err := authority.store.RuntimeTransport.ReadBlob(oid)
	if err != nil {
		return OwnerRiskJournalRecord{}, err
	}
	var journal OwnerRiskJournalRecord
	if err := decodeStrictJSONRecord(data, &journal); err != nil {
		return OwnerRiskJournalRecord{}, fmt.Errorf("decode owner-risk journal: %w", err)
	}
	return journal, nil
}

func runtimeTransition(
	request OwnerRiskClaimRequest,
	state StateRecord,
	journal OwnerRiskJournalRecord,
	lease LeaseRecord,
) (RuntimeAuthorityTransition, error) {
	stateData, err := marshalAuthorityRecord(state)
	if err != nil {
		return RuntimeAuthorityTransition{}, err
	}
	journalData, err := marshalAuthorityRecord(journal)
	if err != nil {
		return RuntimeAuthorityTransition{}, err
	}
	leaseData, err := marshalAuthorityRecord(lease)
	if err != nil {
		return RuntimeAuthorityTransition{}, err
	}
	return RuntimeAuthorityTransition{
		State: RuntimeRefMutation{
			Ref:         request.StateRef,
			ExpectedOID: request.StateExpectedOID,
			Data:        stateData,
		},
		Journal: RuntimeRefMutation{
			Ref:         request.JournalRef,
			ExpectedOID: request.JournalExpectedOID,
			Data:        journalData,
		},
		Lease: RuntimeRefMutation{
			Ref:         request.LeaseRef,
			ExpectedOID: request.LeaseExpectedOID,
			Data:        leaseData,
		},
	}, nil
}

func marshalAuthorityRecord(record any) (string, error) {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	return string(append(data, '\n')), nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
