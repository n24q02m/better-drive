package cleanup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	BrokerStateClaimed             = "claimed"
	BrokerStateCompleted           = "completed"
	BrokerStateNeedsReconciliation = "needs_reconciliation"
	BrokerStateReconciled          = "reconciled"
	SettlementSettled              = "settled"
	SettlementFailed               = "failed"
	SettlementOptionalFail         = "optional_failed"
)

// State transitions are one-way: Claim -> claimed; Complete with a known
// settlement -> completed; Complete with an unknown settlement ->
// needs_reconciliation; Reconcile with exact request, scope/fence, cancellation
// or horizon, and two stable readbacks -> reconciled. Complete is rejected from
// every state other than claimed, and Reconcile is accepted only from
// needs_reconciliation.

var ErrUnknownSettlement = errors.New("unknown settlement; broker fence retained")

// BrokerSigner signs canonical, non-secret readback bytes. The broker has no
// provider transport and never carries bearer material.
type BrokerSigner interface {
	Sign(message []byte) ([]byte, error)
}

type BrokerScope struct {
	Role        string    `json:"role"`
	Intent      string    `json:"intent"`
	Budget      Budget    `json:"budget"`
	ExpiresAt   time.Time `json:"expires_at"`
	ExpectedRef string    `json:"expected_ref"`
	ExpectedOID string    `json:"expected_oid"`
	DesiredRef  string    `json:"desired_ref"`
	DesiredOID  string    `json:"desired_oid"`
}

type ClaimRequest struct {
	ClaimID    string      `json:"claim_id"`
	Owner      string      `json:"owner"`
	Generation uint64      `json:"generation"`
	Scope      BrokerScope `json:"scope"`
}

type ClaimReadback struct {
	ClaimID    string      `json:"claim_id"`
	Owner      string      `json:"owner"`
	Generation uint64      `json:"generation"`
	Fence      uint64      `json:"fence"`
	State      string      `json:"state"`
	Scope      BrokerScope `json:"scope"`
	Signature  string      `json:"signature"`
}

type StableReadback struct {
	Digest     string    `json:"digest"`
	Settlement string    `json:"settlement"`
	ObservedAt time.Time `json:"observed_at"`
}

type CompleteRequest struct {
	ClaimID    string `json:"claim_id"`
	Owner      string `json:"owner"`
	Generation uint64 `json:"generation"`
	Fence      uint64 `json:"fence"`
	RequestID  string `json:"request_id"`
	TargetRef  string `json:"target_ref"`
	TargetOID  string `json:"target_oid"`
	Settlement string `json:"settlement"`
}

type CompleteReadback struct {
	ClaimID    string      `json:"claim_id"`
	Owner      string      `json:"owner"`
	Generation uint64      `json:"generation"`
	Fence      uint64      `json:"fence"`
	RequestID  string      `json:"request_id"`
	State      string      `json:"state"`
	Settlement string      `json:"settlement"`
	Scope      BrokerScope `json:"scope"`
	Signature  string      `json:"signature"`
}

type ReconcileRequest struct {
	ClaimID                string           `json:"claim_id"`
	Owner                  string           `json:"owner"`
	Generation             uint64           `json:"generation"`
	Fence                  uint64           `json:"fence"`
	RequestID              string           `json:"request_id"`
	TargetRef              string           `json:"target_ref"`
	TargetOID              string           `json:"target_oid"`
	Settlement             string           `json:"settlement"`
	CancellationReceipt    string           `json:"cancellation_receipt"`
	ProviderHorizonElapsed bool             `json:"provider_horizon_elapsed"`
	StableReadbacks        []StableReadback `json:"stable_readbacks"`
	ConsistencyWindow      time.Duration    `json:"consistency_window"`
}

type ReconcileReadback struct {
	ClaimID                string           `json:"claim_id"`
	Owner                  string           `json:"owner"`
	Generation             uint64           `json:"generation"`
	Fence                  uint64           `json:"fence"`
	RequestID              string           `json:"request_id"`
	State                  string           `json:"state"`
	Settlement             string           `json:"settlement"`
	Scope                  BrokerScope      `json:"scope"`
	CancellationReceipt    string           `json:"cancellation_receipt"`
	ProviderHorizonElapsed bool             `json:"provider_horizon_elapsed"`
	StableReadbacks        []StableReadback `json:"stable_readbacks"`
	ConsistencyWindow      time.Duration    `json:"consistency_window"`
	Signature              string           `json:"signature"`
}

// These interfaces separate draft, activation, runtime, and reconciliation
// authorities without selecting a transport or provider implementation.
type DraftAuthority interface {
	Draft(ClaimRequest) (ClaimReadback, error)
}

type ActivationAuthority interface {
	Claim(ClaimRequest) (ClaimReadback, error)
}

type RuntimeAuthority interface {
	Complete(CompleteRequest) (CompleteReadback, error)
}

type ReconcileAuthority interface {
	Reconcile(ReconcileRequest) (ReconcileReadback, error)
}

type brokerClaim struct {
	Request    ClaimRequest
	Fence      uint64
	State      string
	RequestID  string
	Settlement string
	Signature  string
}

// Broker is a local authority state machine. It validates exact references and
// monotonic fences; callers provide any external transport or provider readback.
type Broker struct {
	Signer BrokerSigner
	Now    func() time.Time

	mu        sync.Mutex
	claims    map[string]brokerClaim
	nextFence uint64
}

func (b *Broker) Claim(request ClaimRequest) (ClaimReadback, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.validateClaimRequest(request); err != nil {
		return ClaimReadback{}, err
	}
	if _, exists := b.claims[request.ClaimID]; exists {
		return ClaimReadback{}, fmt.Errorf("claim %q replay rejected", request.ClaimID)
	}
	if b.nextFence == ^uint64(0) {
		return ClaimReadback{}, errors.New("broker fence exhausted")
	}
	fence := b.nextFence + 1
	claim := brokerClaim{Request: request, Fence: fence, State: BrokerStateClaimed}
	readback := ClaimReadback{ClaimID: request.ClaimID, Owner: request.Owner, Generation: request.Generation, Fence: fence, State: claim.State, Scope: request.Scope}
	if err := b.sign(&readback); err != nil {
		return ClaimReadback{}, err
	}
	claim.Signature = readback.Signature
	if b.claims == nil {
		b.claims = make(map[string]brokerClaim)
	}
	b.nextFence = fence
	b.claims[request.ClaimID] = claim
	return readback, nil
}

func (b *Broker) Complete(request CompleteRequest) (CompleteReadback, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := validateRequestID(request.RequestID); err != nil {
		return CompleteReadback{}, err
	}
	claim, err := b.validateSettlementRequest(request.ClaimID, request.Owner, request.Generation, request.Fence, request.TargetRef, request.TargetOID)
	if err != nil {
		return CompleteReadback{}, err
	}
	if claim.State != BrokerStateClaimed {
		return CompleteReadback{}, fmt.Errorf("claim %q replay rejected from state %q", request.ClaimID, claim.State)
	}
	settlementErr := validateSettlement(request.Settlement)
	if settlementErr != nil && !errors.Is(settlementErr, ErrUnknownSettlement) {
		return CompleteReadback{}, settlementErr
	}

	candidate := claim
	candidate.RequestID = request.RequestID
	candidate.Settlement = request.Settlement
	if errors.Is(settlementErr, ErrUnknownSettlement) {
		candidate.State = BrokerStateNeedsReconciliation
		readback := CompleteReadback{
			ClaimID: request.ClaimID, Owner: request.Owner, Generation: request.Generation,
			Fence: claim.Fence, RequestID: request.RequestID, State: candidate.State,
			Settlement: request.Settlement, Scope: claim.Request.Scope,
		}
		if err := b.sign(&readback); err != nil {
			return CompleteReadback{}, err
		}
		candidate.Signature = readback.Signature
		b.claims[request.ClaimID] = candidate
		return CompleteReadback{}, ErrUnknownSettlement
	}

	candidate.State = BrokerStateCompleted
	readback := CompleteReadback{
		ClaimID: request.ClaimID, Owner: request.Owner, Generation: request.Generation,
		Fence: claim.Fence, RequestID: request.RequestID, State: candidate.State,
		Settlement: request.Settlement, Scope: claim.Request.Scope,
	}
	if err := b.sign(&readback); err != nil {
		return CompleteReadback{}, err
	}
	candidate.Signature = readback.Signature
	b.claims[request.ClaimID] = candidate
	return readback, nil
}

func (b *Broker) Reconcile(request ReconcileRequest) (ReconcileReadback, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := validateRequestID(request.RequestID); err != nil {
		return ReconcileReadback{}, err
	}
	claim, err := b.validateSettlementRequest(request.ClaimID, request.Owner, request.Generation, request.Fence, request.TargetRef, request.TargetOID)
	if err != nil {
		return ReconcileReadback{}, err
	}
	if claim.State != BrokerStateNeedsReconciliation {
		return ReconcileReadback{}, fmt.Errorf("claim %q replay rejected from state %q", request.ClaimID, claim.State)
	}
	if claim.RequestID != request.RequestID {
		return ReconcileReadback{}, errors.New("broker request ID fence mismatch")
	}
	if err := validateReconcileRequest(request); err != nil {
		return ReconcileReadback{}, err
	}

	candidate := claim
	candidate.Settlement = request.Settlement
	readback := ReconcileReadback{
		ClaimID: request.ClaimID, Owner: request.Owner, Generation: request.Generation,
		Fence: claim.Fence, RequestID: request.RequestID, State: BrokerStateReconciled,
		Settlement: request.Settlement, Scope: claim.Request.Scope,
		CancellationReceipt:    request.CancellationReceipt,
		ProviderHorizonElapsed: request.ProviderHorizonElapsed,
		StableReadbacks:        cloneStableReadbacks(request.StableReadbacks),
		ConsistencyWindow:      request.ConsistencyWindow,
	}
	if err := b.sign(&readback); err != nil {
		return ReconcileReadback{}, err
	}
	candidate.State = BrokerStateReconciled
	candidate.Signature = readback.Signature
	b.claims[request.ClaimID] = candidate
	return readback, nil
}

func (b *Broker) validateClaimRequest(request ClaimRequest) error {
	if strings.TrimSpace(request.ClaimID) == "" || strings.TrimSpace(request.Owner) == "" {
		return errors.New("claim ID and owner are required")
	}
	if request.Generation == 0 {
		return errors.New("claim generation is required")
	}
	return validateScope(request.Scope, b.now())
}

func validateScope(scope BrokerScope, now time.Time) error {
	if strings.TrimSpace(scope.Role) == "" || strings.TrimSpace(scope.Intent) == "" {
		return errors.New("broker role and intent are required")
	}
	if scope.Budget.MaxObjects <= 0 || scope.Budget.MaxBytes <= 0 {
		return errors.New("broker budget must be positive")
	}
	if scope.ExpiresAt.IsZero() || (!now.IsZero() && !now.Before(scope.ExpiresAt)) {
		return errors.New("broker expiry is required and must be in the future")
	}
	for name, value := range map[string]string{"expected_ref": scope.ExpectedRef, "expected_oid": scope.ExpectedOID, "desired_ref": scope.DesiredRef, "desired_oid": scope.DesiredOID} {
		if err := validateSafeReference(name, value); err != nil {
			return err
		}
	}
	return nil
}

func validateSafeReference(kind, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, '\x00') || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("broker %s is required and must be safe", kind)
	}
	pathPart := value
	if scheme, remainder, found := strings.Cut(value, ":"); found {
		if scheme == "" || strings.ContainsAny(scheme, `/\`) {
			return fmt.Errorf("broker %s contains invalid ref scheme", kind)
		}
		pathPart = remainder
	}
	pathPart = strings.ReplaceAll(pathPart, "\\", "/")
	if pathPart == "" || strings.HasPrefix(pathPart, "/") {
		return fmt.Errorf("broker %s contains ref escape", kind)
	}
	for _, part := range strings.Split(pathPart, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("broker %s contains ref escape", kind)
		}
	}
	return nil
}

func (b *Broker) validateSettlementRequest(claimID, owner string, generation, fence uint64, targetRef, targetOID string) (brokerClaim, error) {
	if strings.TrimSpace(claimID) == "" || strings.TrimSpace(owner) == "" {
		return brokerClaim{}, errors.New("claim ID and owner are required")
	}
	claim, exists := b.claims[claimID]
	if !exists {
		return brokerClaim{}, fmt.Errorf("claim %q is unknown", claimID)
	}
	if claim.Request.Owner != owner {
		return brokerClaim{}, errors.New("broker owner fence mismatch")
	}
	if claim.Request.Generation != generation {
		return brokerClaim{}, errors.New("broker generation fence mismatch")
	}
	if claim.Fence != fence {
		return brokerClaim{}, errors.New("broker fence mismatch")
	}
	if targetRef != claim.Request.Scope.DesiredRef {
		return brokerClaim{}, errors.New("broker target ref mismatch")
	}
	if targetOID != claim.Request.Scope.DesiredOID {
		return brokerClaim{}, errors.New("broker target OID mismatch")
	}
	return claim, nil
}

func validateSettlement(value string) error {
	switch value {
	case SettlementSettled, SettlementFailed, SettlementOptionalFail:
		return nil
	default:
		return ErrUnknownSettlement
	}
}
func validateRequestID(value string) error {
	if strings.TrimSpace(value) == "" || strings.ContainsRune(value, '\x00') || strings.ContainsAny(value, "\r\n") {
		return errors.New("broker request ID is required and must be safe")
	}
	return nil
}

func validateReconcileRequest(request ReconcileRequest) error {
	if err := validateRequestID(request.RequestID); err != nil {
		return err
	}
	if strings.TrimSpace(request.CancellationReceipt) == "" && !request.ProviderHorizonElapsed {
		return errors.New("reconcile requires cancellation receipt or provider horizon evidence")
	}
	if strings.ContainsRune(request.CancellationReceipt, '\x00') || strings.ContainsAny(request.CancellationReceipt, "\r\n") {
		return errors.New("reconcile cancellation receipt contains control characters")
	}
	if request.ConsistencyWindow <= 0 {
		return errors.New("reconcile consistency window must be positive")
	}
	if len(request.StableReadbacks) != 2 {
		return errors.New("reconcile requires exactly two stable readbacks")
	}
	if err := validateSettlement(request.Settlement); err != nil {
		return err
	}
	for index, readback := range request.StableReadbacks {
		if strings.TrimSpace(readback.Digest) == "" || strings.ContainsRune(readback.Digest, '\x00') || strings.ContainsAny(readback.Digest, "\r\n") {
			return fmt.Errorf("reconcile stable readback %d digest is required and must be safe", index+1)
		}
		if readback.ObservedAt.IsZero() {
			return fmt.Errorf("reconcile stable readback %d timestamp is required", index+1)
		}
		if err := validateSettlement(readback.Settlement); err != nil {
			return fmt.Errorf("reconcile stable readback %d settlement: %w", index+1, err)
		}
		if readback.Settlement != request.Settlement {
			return fmt.Errorf("reconcile stable readback %d settlement mismatch", index+1)
		}
	}
	first, second := request.StableReadbacks[0], request.StableReadbacks[1]
	if first.Digest != second.Digest || first.Settlement != second.Settlement {
		return errors.New("reconcile stable readbacks do not agree")
	}
	if !second.ObservedAt.After(first.ObservedAt) || second.ObservedAt.Sub(first.ObservedAt) < request.ConsistencyWindow {
		return errors.New("reconcile stable readbacks are not separated by the consistency window")
	}
	return nil
}

func cloneStableReadbacks(readbacks []StableReadback) []StableReadback {
	if readbacks == nil {
		return nil
	}
	return append([]StableReadback(nil), readbacks...)
}

func (b *Broker) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

func (b *Broker) sign(value any) error {
	canonical, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("canonical broker readback: %w", err)
	}
	var signature []byte
	if b.Signer != nil {
		signature, err = b.Signer.Sign(canonical)
		if err != nil {
			return fmt.Errorf("sign broker readback: %w", err)
		}
	} else {
		sum := sha256.Sum256(canonical)
		signature = sum[:]
	}
	if len(signature) == 0 {
		return errors.New("broker signer returned empty signature")
	}
	switch readback := value.(type) {
	case *ClaimReadback:
		readback.Signature = hex.EncodeToString(signature)
	case *CompleteReadback:
		readback.Signature = hex.EncodeToString(signature)
	case *ReconcileReadback:
		readback.Signature = hex.EncodeToString(signature)
	default:
		return errors.New("unsupported broker readback")
	}
	return nil
}
