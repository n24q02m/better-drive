package cleanup

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func validClaimRequest() ClaimRequest {
	return ClaimRequest{
		ClaimID:    "claim-1",
		Owner:      "scheduler-a",
		Generation: 1,
		Scope: BrokerScope{
			Role:        "role:backup",
			Intent:      "intent:cleanup",
			Budget:      Budget{MaxObjects: 10, MaxBytes: 1000},
			ExpiresAt:   time.Unix(200, 0).UTC(),
			ExpectedRef: "drive:inventory",
			ExpectedOID: "inventory-oid",
			DesiredRef:  "drive:quarantine",
			DesiredOID:  "quarantine-oid",
		},
	}
}
func validReconcileRequest(claim ClaimReadback, requestID, targetRef, settlement string) ReconcileRequest {
	return ReconcileRequest{
		ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence,
		TargetRef: targetRef, TargetOID: claim.Scope.DesiredOID, Settlement: settlement, RequestID: requestID,
		CancellationReceipt: "cancel-receipt-1",
		StableReadbacks: []StableReadback{
			{Digest: "stable-digest-1", Settlement: settlement, ObservedAt: time.Unix(100, 0).UTC()},
			{Digest: "stable-digest-1", Settlement: settlement, ObservedAt: time.Unix(101, 0).UTC()},
		},
		ConsistencyWindow: time.Second,
	}
}

func TestBrokerClaimScopesExactRefsAndReturnsSignedSecretFreeReadback(t *testing.T) {
	broker := &Broker{Signer: &brokerTestSigner{}, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	request := validClaimRequest()
	got, err := broker.Claim(request)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got.ClaimID != request.ClaimID || got.Owner != request.Owner || got.Generation != request.Generation || got.Fence == 0 || got.Signature == "" {
		t.Fatalf("claim readback = %#v, want exact signed scope and fence", got)
	}
	if got.Scope != request.Scope {
		t.Fatalf("scope = %#v, want %#v", got.Scope, request.Scope)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "secret") || strings.Contains(strings.ToLower(string(encoded)), "bearer") || strings.Contains(strings.ToLower(string(encoded)), "token") {
		t.Fatalf("claim readback contains bearer material: %s", encoded)
	}
}

func TestBrokerRejectsRefEscapeAndAlternateOID(t *testing.T) {
	broker := &Broker{Signer: &brokerTestSigner{}, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	bad := validClaimRequest()
	bad.Scope.DesiredRef = "drive:../foreign"
	if _, err := broker.Claim(bad); err == nil || !strings.Contains(err.Error(), "ref") {
		t.Fatalf("escape claim error = %v, want ref rejection", err)
	}
	claim, err := broker.Claim(validClaimRequest())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	complete := CompleteRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, RequestID: "complete-oid", TargetRef: claim.Scope.DesiredRef, TargetOID: "alternate-oid", Settlement: SettlementSettled}
	if _, err := broker.Complete(complete); err == nil || !strings.Contains(err.Error(), "OID") {
		t.Fatalf("alternate OID error = %v, want exact-OID rejection", err)
	}
}

func TestBrokerRejectsReplayAndStaleOwnerOrGeneration(t *testing.T) {
	broker := &Broker{Signer: &brokerTestSigner{}, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	request := validClaimRequest()
	claim, err := broker.Claim(request)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if _, err := broker.Claim(request); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("replayed Claim error = %v, want replay rejection", err)
	}
	stale := CompleteRequest{ClaimID: claim.ClaimID, Owner: "other-owner", Generation: claim.Generation, Fence: claim.Fence, RequestID: "complete-replay", TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: SettlementSettled}
	if _, err := broker.Complete(CompleteRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: SettlementSettled}); err == nil || !strings.Contains(err.Error(), "request ID") {
		t.Fatalf("empty Complete request ID error = %v, want request-ID rejection", err)
	}
	if _, err := broker.Complete(stale); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("stale owner error = %v, want owner rejection", err)
	}
	stale.Owner = claim.Owner
	stale.Generation++
	if _, err := broker.Complete(stale); err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("stale generation error = %v, want generation rejection", err)
	}
	valid := stale
	valid.Generation = claim.Generation
	if _, err := broker.Complete(valid); err != nil {
		t.Fatalf("valid Complete: %v", err)
	}
	if _, err := broker.Complete(valid); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("replayed Complete error = %v, want replay rejection", err)
	}
}

func TestBrokerUnknownSettlementRequiresReconciliationBeforeComplete(t *testing.T) {
	broker := &Broker{Signer: &brokerTestSigner{}, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	claim, err := broker.Claim(validClaimRequest())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	unknown := CompleteRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, RequestID: "operation-1", TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: "provider-returned-something-new"}
	if _, err := broker.Complete(unknown); !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("unknown settlement error = %v, want ErrUnknownSettlement", err)
	}
	if got := broker.claims[claim.ClaimID]; got.State != BrokerStateNeedsReconciliation || got.Fence != claim.Fence || got.RequestID != unknown.RequestID || got.Signature == "" {
		t.Fatalf("claim after unknown settlement = %+v, want signed reconciliation-required state with retained request/fence", got)
	}
	known := unknown
	known.Settlement = SettlementSettled
	if _, err := broker.Complete(known); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("Complete after unknown settlement error = %v, want replay rejection", err)
	}
}

func TestBrokerUnknownSettlementReconcileUsesExactEvidence(t *testing.T) {
	broker := &Broker{Signer: &brokerTestSigner{}, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	claim, err := broker.Claim(validClaimRequest())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	unknown := CompleteRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, RequestID: "operation-2", TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: "provider-returned-something-new"}
	if _, err := broker.Complete(unknown); !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("unknown settlement error = %v, want ErrUnknownSettlement", err)
	}
	request := validReconcileRequest(claim, unknown.RequestID, "drive:other", SettlementSettled)
	if _, err := broker.Reconcile(request); err == nil || !strings.Contains(err.Error(), "ref") {
		t.Fatalf("alternate reconcile ref error = %v, want exact-ref rejection", err)
	}
	request.TargetRef = claim.Scope.DesiredRef
	request.StableReadbacks = request.StableReadbacks[:1]
	if _, err := broker.Reconcile(request); err == nil || !strings.Contains(err.Error(), "stable readbacks") {
		t.Fatalf("short stable-readback evidence error = %v, want exact-two rejection", err)
	}
	request = validReconcileRequest(claim, unknown.RequestID, claim.Scope.DesiredRef, SettlementSettled)
	readback, err := broker.Reconcile(request)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if readback.Fence != claim.Fence || readback.State != BrokerStateReconciled || readback.Settlement != SettlementSettled || readback.RequestID != unknown.RequestID || readback.Signature == "" {
		t.Fatalf("reconcile readback = %#v, want retained fence/reconciled signed state", readback)
	}
	if _, err := broker.Complete(CompleteRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, RequestID: unknown.RequestID, TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: SettlementSettled}); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("Complete after Reconcile error = %v, want replay rejection", err)
	}
}

func TestBrokerReconcileRejectsRequestIDEvidenceDrift(t *testing.T) {
	broker := &Broker{Signer: &brokerTestSigner{}, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	claim, err := broker.Claim(validClaimRequest())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	unknown := CompleteRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, RequestID: "operation-3", TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: "provider-returned-something-new"}
	if _, err := broker.Complete(unknown); !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("unknown settlement error = %v, want ErrUnknownSettlement", err)
	}
	request := validReconcileRequest(claim, "different-operation", claim.Scope.DesiredRef, SettlementSettled)
	if _, err := broker.Reconcile(request); err == nil || !strings.Contains(err.Error(), "request ID") {
		t.Fatalf("request ID drift error = %v, want exact-request rejection", err)
	}
	if got := broker.claims[claim.ClaimID]; got.State != BrokerStateNeedsReconciliation {
		t.Fatalf("claim after request ID drift = %+v, want reconciliation-required state", got)
	}
}

type brokerTestSigner struct {
	fail bool
}

func (signer *brokerTestSigner) Sign([]byte) ([]byte, error) {
	if signer.fail {
		return nil, errors.New("signer unavailable")
	}
	return []byte("signed"), nil
}

func TestBrokerSettlementSignerFailureLeavesClaimClaimedForRetry(t *testing.T) {
	signer := &brokerTestSigner{}
	broker := &Broker{Signer: signer, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	claimRequest := validClaimRequest()
	claim, err := broker.Claim(claimRequest)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	signer.fail = true
	complete := CompleteRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, RequestID: "operation-complete", TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: SettlementSettled}
	if _, err := broker.Complete(complete); err == nil || !strings.Contains(err.Error(), "sign broker readback") {
		t.Fatalf("signing Complete error = %v, want signer failure", err)
	}
	signer.fail = false
	if readback, err := broker.Complete(complete); err != nil || readback.State != BrokerStateCompleted || readback.Fence != claim.Fence {
		t.Fatalf("retry Complete readback=%+v err=%v, want claimed fence reusable", readback, err)
	}

	claimRequest.ClaimID = "claim-reconcile"
	claim, err = broker.Claim(claimRequest)
	if err != nil {
		t.Fatalf("Claim reconcile: %v", err)
	}
	unknown := CompleteRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, RequestID: "operation-reconcile", TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: "provider-returned-something-new"}
	if _, err := broker.Complete(unknown); !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("unknown reconcile settlement error = %v, want ErrUnknownSettlement", err)
	}
	signer.fail = true
	reconcile := validReconcileRequest(claim, unknown.RequestID, claim.Scope.DesiredRef, SettlementSettled)
	if _, err := broker.Reconcile(reconcile); err == nil || !strings.Contains(err.Error(), "sign broker readback") {
		t.Fatalf("signing Reconcile error = %v, want signer failure", err)
	}
	if got := broker.claims[claim.ClaimID]; got.State != BrokerStateNeedsReconciliation {
		t.Fatalf("claim after failed Reconcile = %+v, want reconciliation-required state", got)
	}
	signer.fail = false
	if readback, err := broker.Reconcile(reconcile); err != nil || readback.State != BrokerStateReconciled || readback.Fence != claim.Fence {
		t.Fatalf("retry Reconcile readback=%+v err=%v, want reconciliation-required fence reusable", readback, err)
	}
}

func TestBrokerClaimRequiresSignerWithoutStateChange(t *testing.T) {
	broker := &Broker{Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	if _, err := broker.Claim(validClaimRequest()); !errors.Is(err, ErrBrokerSignerRequired) {
		t.Fatalf("Claim error = %v, want ErrBrokerSignerRequired", err)
	}
	if broker.nextFence != 0 || len(broker.claims) != 0 {
		t.Fatalf("broker state after unsigned Claim = nextFence=%d claims=%d, want unchanged", broker.nextFence, len(broker.claims))
	}
}

func TestBrokerCompleteRequiresSignerWithoutStateChange(t *testing.T) {
	broker := &Broker{Signer: &brokerTestSigner{}, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	claim, err := broker.Claim(validClaimRequest())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	before := broker.claims[claim.ClaimID]
	broker.Signer = nil
	request := CompleteRequest{
		ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence,
		RequestID: "operation-complete", TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID,
		Settlement: SettlementSettled,
	}
	if _, err := broker.Complete(request); !errors.Is(err, ErrBrokerSignerRequired) {
		t.Fatalf("Complete error = %v, want ErrBrokerSignerRequired", err)
	}
	if got := broker.claims[claim.ClaimID]; got != before {
		t.Fatalf("claim after unsigned Complete = %+v, want unchanged %+v", got, before)
	}
}

func TestBrokerUnknownCompleteRequiresSignerWithoutStateChange(t *testing.T) {
	broker := &Broker{Signer: &brokerTestSigner{}, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	claim, err := broker.Claim(validClaimRequest())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	before := broker.claims[claim.ClaimID]
	broker.Signer = nil
	request := CompleteRequest{
		ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence,
		RequestID: "operation-unknown", TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID,
		Settlement: "provider-returned-something-new",
	}
	if _, err := broker.Complete(request); !errors.Is(err, ErrBrokerSignerRequired) {
		t.Fatalf("unknown Complete error = %v, want ErrBrokerSignerRequired", err)
	}
	if got := broker.claims[claim.ClaimID]; got != before {
		t.Fatalf("claim after unsigned unknown Complete = %+v, want unchanged %+v", got, before)
	}
}

func TestBrokerReconcileRequiresSignerWithoutStateChange(t *testing.T) {
	broker := &Broker{Signer: &brokerTestSigner{}, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	claim, err := broker.Claim(validClaimRequest())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	unknown := CompleteRequest{
		ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence,
		RequestID: "operation-reconcile", TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID,
		Settlement: "provider-returned-something-new",
	}
	if _, err := broker.Complete(unknown); !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("unknown Complete: %v", err)
	}
	before := broker.claims[claim.ClaimID]
	broker.Signer = nil
	if _, err := broker.Reconcile(validReconcileRequest(claim, unknown.RequestID, claim.Scope.DesiredRef, SettlementSettled)); !errors.Is(err, ErrBrokerSignerRequired) {
		t.Fatalf("Reconcile error = %v, want ErrBrokerSignerRequired", err)
	}
	if got := broker.claims[claim.ClaimID]; got != before {
		t.Fatalf("claim after unsigned Reconcile = %+v, want unchanged %+v", got, before)
	}
}
