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
	BrokerStateClaimed     = "claimed"
	BrokerStateCompleted   = "completed"
	BrokerStateReconciled  = "reconciled"
	SettlementSettled      = "settled"
	SettlementFailed       = "failed"
	SettlementOptionalFail = "optional_failed"
)

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

type CompleteRequest struct {
	ClaimID    string `json:"claim_id"`
	Owner      string `json:"owner"`
	Generation uint64 `json:"generation"`
	Fence      uint64 `json:"fence"`
	TargetRef  string `json:"target_ref"`
	TargetOID  string `json:"target_oid"`
	Settlement string `json:"settlement"`
}

type CompleteReadback struct {
	ClaimID    string      `json:"claim_id"`
	Owner      string      `json:"owner"`
	Generation uint64      `json:"generation"`
	Fence      uint64      `json:"fence"`
	State      string      `json:"state"`
	Settlement string      `json:"settlement"`
	Scope      BrokerScope `json:"scope"`
	Signature  string      `json:"signature"`
}

type ReconcileRequest struct {
	ClaimID    string `json:"claim_id"`
	Owner      string `json:"owner"`
	Generation uint64 `json:"generation"`
	Fence      uint64 `json:"fence"`
	TargetRef  string `json:"target_ref"`
	TargetOID  string `json:"target_oid"`
	Settlement string `json:"settlement"`
}

type ReconcileReadback struct {
	ClaimID    string      `json:"claim_id"`
	Owner      string      `json:"owner"`
	Generation uint64      `json:"generation"`
	Fence      uint64      `json:"fence"`
	State      string      `json:"state"`
	Settlement string      `json:"settlement"`
	Scope      BrokerScope `json:"scope"`
	Signature  string      `json:"signature"`
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
	Request ClaimRequest
	Fence   uint64
	State   string
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
	if b.claims == nil {
		b.claims = make(map[string]brokerClaim)
	}
	if _, exists := b.claims[request.ClaimID]; exists {
		return ClaimReadback{}, fmt.Errorf("claim %q replay rejected", request.ClaimID)
	}
	b.nextFence++
	claim := brokerClaim{Request: request, Fence: b.nextFence, State: BrokerStateClaimed}
	b.claims[request.ClaimID] = claim
	readback := ClaimReadback{ClaimID: request.ClaimID, Owner: request.Owner, Generation: request.Generation, Fence: claim.Fence, State: claim.State, Scope: request.Scope}
	if err := b.sign(&readback); err != nil {
		delete(b.claims, request.ClaimID)
		return ClaimReadback{}, err
	}
	return readback, nil
}

func (b *Broker) Complete(request CompleteRequest) (CompleteReadback, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	claim, err := b.validateSettlementRequest(request.ClaimID, request.Owner, request.Generation, request.Fence, request.TargetRef, request.TargetOID)
	if err != nil {
		return CompleteReadback{}, err
	}
	if claim.State != BrokerStateClaimed {
		return CompleteReadback{}, fmt.Errorf("claim %q replay rejected from state %q", request.ClaimID, claim.State)
	}
	if err := validateSettlement(request.Settlement); err != nil {
		return CompleteReadback{}, err
	}
	claim.State = BrokerStateCompleted
	readback := CompleteReadback{ClaimID: request.ClaimID, Owner: request.Owner, Generation: request.Generation, Fence: claim.Fence, State: claim.State, Settlement: request.Settlement, Scope: claim.Request.Scope}
	if err := b.sign(&readback); err != nil {
		return CompleteReadback{}, err
	}
	b.claims[request.ClaimID] = claim
	return readback, nil
}

func (b *Broker) Reconcile(request ReconcileRequest) (ReconcileReadback, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	claim, err := b.validateSettlementRequest(request.ClaimID, request.Owner, request.Generation, request.Fence, request.TargetRef, request.TargetOID)
	if err != nil {
		return ReconcileReadback{}, err
	}
	if claim.State != BrokerStateClaimed {
		return ReconcileReadback{}, fmt.Errorf("claim %q replay rejected from state %q", request.ClaimID, claim.State)
	}
	if err := validateSettlement(request.Settlement); err != nil {
		return ReconcileReadback{}, err
	}
	claim.State = BrokerStateReconciled
	readback := ReconcileReadback{ClaimID: request.ClaimID, Owner: request.Owner, Generation: request.Generation, Fence: claim.Fence, State: claim.State, Settlement: request.Settlement, Scope: claim.Request.Scope}
	if err := b.sign(&readback); err != nil {
		return ReconcileReadback{}, err
	}
	b.claims[request.ClaimID] = claim
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
