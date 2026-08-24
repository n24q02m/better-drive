package retention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/driveapi"
	"github.com/n24q02m/better-drive/internal/r2api"
)

func retentionObject(key string, modified time.Time) Object {
	return Object{Provider: string(ProviderR2), AccountID: "acct-1", RootID: "root-1", Namespace: "backup", Bucket: "source", Key: key, ObjectID: key, Version: "v1", Generation: "g1", ETag: "e-" + key, ParentID: "parent", Size: 4, Hash: "h-" + key, ModifiedAt: modified}
}

func retentionInventory(now time.Time) Inventory {
	return Inventory{AccountID: "acct-1", RootID: "root-1", Namespace: "backup", CapturedAt: now.Add(-time.Minute), Objects: []Object{retentionObject("one", now.Add(-time.Hour)), retentionObject("two", now.Add(-time.Hour))}, RestoreSets: []RestoreSet{{ID: "old", CreatedAt: now.Add(-2 * time.Hour), Complete: true, Replicas: []ReplicaEvidence{{ID: "r-old", Required: true, Complete: true}}}, {ID: "new", CreatedAt: now.Add(-time.Hour), Complete: true, Replicas: []ReplicaEvidence{{ID: "r-new", Required: true, Complete: true}}}}}
}

func ownerMarker() OwnershipMarker {
	return OwnershipMarker{OwnerID: "job-1", AccountID: "acct-1", RootID: "root-1", Namespace: "backup", Marker: "marker-1"}
}

func validR2Policy() Policy {
	return Policy{ID: "policy-1", Provider: ProviderR2, DeletePolicy: DeletePolicyQuarantine, MinCompleteRestoreSets: 1, MaxObjects: 10, MaxBytes: 100, QuarantineBucket: "quarantine", ActivatedAt: time.Unix(100, 0).UTC()}
}

func TestNewestCompleteRestoreSetsRejectIncompleteRequiredReplica(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	inventory := retentionInventory(now)
	inventory.RestoreSets[1].Replicas[0].Complete = false
	if _, err := NewestCompleteRestoreSets(inventory, 1); err == nil || !strings.Contains(err.Error(), "required replica") {
		t.Fatalf("incomplete restore set error = %v", err)
	}
	inventory.RestoreSets[1].Replicas[0].Complete = true
	sets, err := NewestCompleteRestoreSets(inventory, 1)
	if err != nil {
		t.Fatalf("NewestCompleteRestoreSets() error = %v", err)
	}
	if len(sets) != 1 || sets[0].ID != "new" {
		t.Fatalf("sets = %+v", sets)
	}
}

func TestPlannerRejectsFutureTimeBudgetAndQuarantineOverlap(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	inventory := retentionInventory(now)
	inventory.Objects[0].ModifiedAt = now.Add(time.Second)
	policy := validR2Policy()
	if _, err := PlanRetention(policy, inventory, ownerMarker(), now); err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("future timestamp error = %v", err)
	}
	inventory.Objects[0].ModifiedAt = now.Add(-time.Hour)
	policy.MaxObjects = 1
	if _, err := PlanRetention(policy, inventory, ownerMarker(), now); err == nil || !strings.Contains(err.Error(), "object budget") {
		t.Fatalf("object budget error = %v", err)
	}
	policy.MaxObjects = 10
	inventory.Objects[0].Bucket = policy.QuarantineBucket
	if _, err := PlanRetention(policy, inventory, ownerMarker(), now); err == nil || !strings.Contains(err.Error(), "quarantine overlap") {
		t.Fatalf("quarantine overlap error = %v", err)
	}
}

func TestDriveDeletePolicyDefaultsToNoOp(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	inventory := retentionInventory(now)
	for index := range inventory.Objects {
		inventory.Objects[index].Provider = string(ProviderDrive)
	}
	policy := Policy{ID: "drive-default", Provider: ProviderDrive, MaxObjects: 10, MaxBytes: 100, ActivatedAt: time.Unix(99, 0).UTC()}
	plan, err := PlanRetention(policy, inventory, ownerMarker(), now)
	if err != nil {
		t.Fatalf("PlanRetention() error = %v", err)
	}
	if len(plan.Actions) != 0 || plan.Policy.DeletePolicy != DeletePolicyNone {
		t.Fatalf("default Drive plan = %+v", plan)
	}
}

func TestDriveQuarantinePlanningFailsClosedWithoutInjectedCapability(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	inventory := retentionInventory(now)
	for index := range inventory.Objects {
		inventory.Objects[index].Provider = string(ProviderDrive)
	}
	policy := Policy{ID: "drive-quarantine", Provider: ProviderDrive, DeletePolicy: DeletePolicyQuarantine, MinCompleteRestoreSets: 1, MaxObjects: 10, MaxBytes: 100, QuarantineBucket: "quarantine", ActivatedAt: time.Unix(99, 0).UTC()}
	if _, err := PlanRetention(policy, inventory, ownerMarker(), now); err == nil || !strings.Contains(strings.ToLower(err.Error()), "capability") {
		t.Fatalf("Drive quarantine plan error = %v, want fail-closed capability rejection", err)
	}
}

func TestEngineRetainsLeaseOnUnknownSettlementAndNeverRetriesAmbiguous(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	policy := validR2Policy()
	inventory := retentionInventory(now)
	plan, err := PlanRetention(policy, inventory, ownerMarker(), now)
	if err != nil {
		t.Fatal(err)
	}
	plan.Actions = plan.Actions[:1]
	plan.Actions[0].R2CopyCapability = r2api.NewCopyCapability(plan.Actions[0].R2Copy.Source, plan.Actions[0].R2Copy.Destination, plan.Actions[0].RequestID, now.Add(time.Hour), "signed")
	provider := &fakeRetentionProvider{unknownCopy: true}
	engine := NewEngine(provider, provider, NewJournal())
	engine.Now = func() time.Time { return now }
	if _, err := engine.Apply(context.Background(), plan, policy); !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("unknown settlement error = %v", err)
	}
	if engine.Lease.State != LeaseClaimed || engine.Lease.Fence == "" || provider.copyCalls != 1 {
		t.Fatalf("lease = %+v calls = %d", engine.Lease, provider.copyCalls)
	}
	provider.unknownCopy = false
	if _, err := engine.Apply(context.Background(), plan, policy); !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("ambiguous retry error = %v", err)
	}
	if provider.copyCalls != 1 {
		t.Fatalf("ambiguous action retried, calls = %d", provider.copyCalls)
	}
}

func TestEngineClassifiesOptionalFailureAndRejectsPolicyActivationDrift(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	policy := validR2Policy()
	inventory := retentionInventory(now)
	inventory.Objects[0].Optional = true
	plan, err := PlanRetention(policy, inventory, ownerMarker(), now)
	if err != nil {
		t.Fatal(err)
	}
	plan.Actions = plan.Actions[:1]
	plan.Actions[0].R2CopyCapability = r2api.NewCopyCapability(plan.Actions[0].R2Copy.Source, plan.Actions[0].R2Copy.Destination, plan.Actions[0].RequestID, now.Add(time.Hour), "signed")
	provider := &fakeRetentionProvider{copyErr: errors.New("optional unavailable")}
	engine := NewEngine(provider, provider, NewJournal())
	engine.Now = func() time.Time { return now }
	report, err := engine.Apply(context.Background(), plan, policy)
	if err != nil || len(report.OptionalFailures) != 1 {
		t.Fatalf("optional report=%+v err=%v", report, err)
	}
	changed := policy
	changed.ID = "new-policy"
	if _, err := engine.Apply(context.Background(), plan, changed); err == nil || !strings.Contains(err.Error(), "policy activation drift") {
		t.Fatalf("activation drift error = %v", err)
	}
}

func TestEngineDoesNotSwallowUnknownOrOptionalJournalAppendErrors(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	policy := validR2Policy()
	inventory := retentionInventory(now)
	plan, err := PlanRetention(policy, inventory, ownerMarker(), now)
	if err != nil {
		t.Fatal(err)
	}
	plan.Actions = plan.Actions[:1]
	plan.Actions[0].R2CopyCapability = r2api.NewCopyCapability(plan.Actions[0].R2Copy.Source, plan.Actions[0].R2Copy.Destination, plan.Actions[0].RequestID, now.Add(time.Hour), "signed")

	journalErr := errors.New("journal unavailable")
	provider := &fakeRetentionProvider{unknownCopy: true}
	journal := NewJournal()
	journal.appendHook = func(JournalRecord) error { return journalErr }
	engine := NewEngine(provider, provider, journal)
	engine.Now = func() time.Time { return now }
	if _, err := engine.Apply(context.Background(), plan, policy); err == nil || !errors.Is(err, ErrUnknownSettlement) || !errors.Is(err, journalErr) {
		t.Fatalf("unknown journal append error = %v, want combined provider and append failure", err)
	}
	if provider.copyCalls != 1 || engine.Lease.State != LeaseClaimed || engine.Lease.Fence == "" {
		t.Fatalf("unknown append failure lease=%+v copy calls=%d", engine.Lease, provider.copyCalls)
	}
	if _, retryErr := engine.Apply(context.Background(), plan, policy); !errors.Is(retryErr, ErrUnknownSettlement) || provider.copyCalls != 1 {
		t.Fatalf("unknown append failure retried: err=%v calls=%d", retryErr, provider.copyCalls)
	}

	optionalErr := errors.New("optional provider unavailable")
	provider = &fakeRetentionProvider{copyErr: optionalErr}
	journal = NewJournal()
	journal.appendHook = func(JournalRecord) error { return journalErr }
	engine = NewEngine(provider, provider, journal)
	engine.Now = func() time.Time { return now }
	plan.Actions[0].Optional = true
	if _, err := engine.Apply(context.Background(), plan, policy); err == nil || !errors.Is(err, optionalErr) || !errors.Is(err, journalErr) {
		t.Fatalf("optional journal append error = %v, want combined provider and append failure", err)
	}
	if engine.Lease.State != LeaseClaimed || engine.Lease.Fence == "" || provider.copyCalls != 1 {
		t.Fatalf("optional append failure lease=%+v copy calls=%d", engine.Lease, provider.copyCalls)
	}
}

func TestEngineJournalLookupBindsPlanPolicyAndQuarantineTarget(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	inventory := retentionInventory(now)
	policyOne := validR2Policy()
	planOne, err := PlanRetention(policyOne, inventory, ownerMarker(), now)
	if err != nil {
		t.Fatal(err)
	}
	planOne.Actions = planOne.Actions[:1]
	planOne.Actions[0].R2CopyCapability = r2api.NewCopyCapability(planOne.Actions[0].R2Copy.Source, planOne.Actions[0].R2Copy.Destination, planOne.Actions[0].RequestID, now.Add(time.Hour), "signed")
	provider := &fakeRetentionProvider{}
	journal := NewJournal()
	engine := NewEngine(provider, provider, journal)
	engine.Now = func() time.Time { return now }
	if _, err := engine.Apply(context.Background(), planOne, policyOne); err != nil {
		t.Fatalf("first plan Apply() error = %v", err)
	}

	policyTwo := policyOne
	policyTwo.ID = "policy-two"
	policyTwo.QuarantinePrefix = "other/"
	planTwo, err := PlanRetention(policyTwo, inventory, ownerMarker(), now)
	if err != nil {
		t.Fatal(err)
	}
	planTwo.Actions = planTwo.Actions[:1]
	planTwo.Actions[0].R2CopyCapability = r2api.NewCopyCapability(planTwo.Actions[0].R2Copy.Source, planTwo.Actions[0].R2Copy.Destination, planTwo.Actions[0].RequestID, now.Add(time.Hour), "signed")
	if planOne.ID == planTwo.ID || planOne.Actions[0].ID == planTwo.Actions[0].ID || planOne.Actions[0].RequestID == planTwo.Actions[0].RequestID {
		t.Fatalf("cross-plan action identity collision: plan1=%+v plan2=%+v", planOne.Actions[0], planTwo.Actions[0])
	}
	secondEngine := NewEngine(provider, provider, journal)
	secondEngine.Now = func() time.Time { return now }
	if _, err := secondEngine.Apply(context.Background(), planTwo, policyTwo); err != nil {
		t.Fatalf("second plan Apply() error = %v", err)
	}
	if provider.copyCalls != 2 {
		t.Fatalf("cross-plan journal record suppressed action, copy calls=%d", provider.copyCalls)
	}
}

func TestJournalIsAppendOnlyAndVerifiable(t *testing.T) {
	journal := NewJournal()
	if err := journal.Append(JournalRecord{TransactionID: "tx-1", ActionID: "a-1", RequestID: "r-1", Before: "planned", After: "settled", Outcome: OutcomeSettled, Timestamp: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Verify(); err != nil {
		t.Fatal(err)
	}
	if len(journal.Records) != 1 || journal.Records[0].Hash == "" {
		t.Fatalf("records = %+v", journal.Records)
	}
}

type fakeRetentionProvider struct {
	unknownCopy bool
	copyErr     error
	copyCalls   int
}

func (provider *fakeRetentionProvider) Head(_ context.Context, identity r2api.ObjectIdentity) (r2api.Object, error) {
	return r2api.Object{Identity: identity, Size: 4, SHA256: "h-one"}, nil
}

func (provider *fakeRetentionProvider) Copy(_ context.Context, request r2api.CopyRequest, _ r2api.CopyCapability) (r2api.CopyReceipt, error) {
	provider.copyCalls++
	if provider.unknownCopy {
		return r2api.CopyReceipt{}, ErrUnknownSettlement
	}
	if provider.copyErr != nil {
		return r2api.CopyReceipt{}, provider.copyErr
	}
	return r2api.CopyReceipt{Source: request.Source, Destination: request.Destination, Size: request.ExpectedSize, SHA256: request.ExpectedSHA256, ReadbackVerified: true, RequestID: request.RequestID}, nil
}

func (provider *fakeRetentionProvider) Delete(context.Context, r2api.DeleteRequest, r2api.DeleteCapability) (r2api.MutationReceipt, error) {
	return r2api.MutationReceipt{}, nil
}

func (provider *fakeRetentionProvider) Purge(context.Context, r2api.PurgeRequest, r2api.PurgeCapability) (r2api.MutationReceipt, error) {
	return r2api.MutationReceipt{}, nil
}

func (provider *fakeRetentionProvider) Mutate(_ context.Context, request driveapi.MutationRequest) (driveapi.MutationResult, error) {
	return driveapi.MutationResult{ProviderID: request.ObjectID, ObjectID: request.ObjectID, AccountID: request.AccountID, RootID: request.RootID, Namespace: request.Namespace, ParentID: request.ParentID, ETag: request.ExpectedETag, Version: request.Version, Generation: request.Generation, Size: request.Size, Hash: request.Hash, RequestID: request.RequestID, State: "quarantined"}, nil
}
