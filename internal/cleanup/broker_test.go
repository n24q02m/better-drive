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

func TestBrokerClaimScopesExactRefsAndReturnsSignedSecretFreeReadback(t *testing.T) {
	broker := &Broker{Now: func() time.Time { return time.Unix(100, 0).UTC() }}
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
	broker := &Broker{Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	bad := validClaimRequest()
	bad.Scope.DesiredRef = "drive:../foreign"
	if _, err := broker.Claim(bad); err == nil || !strings.Contains(err.Error(), "ref") {
		t.Fatalf("escape claim error = %v, want ref rejection", err)
	}
	claim, err := broker.Claim(validClaimRequest())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	complete := CompleteRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, TargetRef: claim.Scope.DesiredRef, TargetOID: "alternate-oid", Settlement: SettlementSettled}
	if _, err := broker.Complete(complete); err == nil || !strings.Contains(err.Error(), "OID") {
		t.Fatalf("alternate OID error = %v, want exact-OID rejection", err)
	}
}

func TestBrokerRejectsReplayAndStaleOwnerOrGeneration(t *testing.T) {
	broker := &Broker{Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	request := validClaimRequest()
	claim, err := broker.Claim(request)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if _, err := broker.Claim(request); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("replayed Claim error = %v, want replay rejection", err)
	}
	stale := CompleteRequest{ClaimID: claim.ClaimID, Owner: "other-owner", Generation: claim.Generation, Fence: claim.Fence, TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: SettlementSettled}
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

func TestBrokerUnknownSettlementRetainsFence(t *testing.T) {
	broker := &Broker{Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	claim, err := broker.Claim(validClaimRequest())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	unknown := CompleteRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: "provider-returned-something-new"}
	if _, err := broker.Complete(unknown); !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("unknown settlement error = %v, want ErrUnknownSettlement", err)
	}
	readback, err := broker.Complete(CompleteRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: SettlementSettled})
	if err != nil {
		t.Fatalf("settlement after unknown: %v", err)
	}
	if readback.Fence != claim.Fence {
		t.Fatalf("fence = %d, want retained %d", readback.Fence, claim.Fence)
	}
}

func TestBrokerReconcileUsesSameExactScopeAndRejectsAlternateAuthority(t *testing.T) {
	broker := &Broker{Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	claim, err := broker.Claim(validClaimRequest())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	request := ReconcileRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, TargetRef: "drive:other", TargetOID: claim.Scope.DesiredOID, Settlement: SettlementSettled}
	if _, err := broker.Reconcile(request); err == nil || !strings.Contains(err.Error(), "ref") {
		t.Fatalf("alternate reconcile ref error = %v, want exact-ref rejection", err)
	}
	request.TargetRef = claim.Scope.DesiredRef
	readback, err := broker.Reconcile(request)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if readback.Fence != claim.Fence || readback.State != BrokerStateReconciled {
		t.Fatalf("reconcile readback = %#v, want retained fence/reconciled state", readback)
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
	complete := CompleteRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: SettlementSettled}
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
	signer.fail = true
	reconcile := ReconcileRequest{ClaimID: claim.ClaimID, Owner: claim.Owner, Generation: claim.Generation, Fence: claim.Fence, TargetRef: claim.Scope.DesiredRef, TargetOID: claim.Scope.DesiredOID, Settlement: SettlementSettled}
	if _, err := broker.Reconcile(reconcile); err == nil || !strings.Contains(err.Error(), "sign broker readback") {
		t.Fatalf("signing Reconcile error = %v, want signer failure", err)
	}
	signer.fail = false
	if readback, err := broker.Reconcile(reconcile); err != nil || readback.State != BrokerStateReconciled || readback.Fence != claim.Fence {
		t.Fatalf("retry Reconcile readback=%+v err=%v, want claimed fence reusable", readback, err)
	}
}
