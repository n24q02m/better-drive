package retention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/r2api"
)

func validRestoreReplica(id string) ReplicaEvidence {
	return ReplicaEvidence{ID: id, Provider: string(ProviderR2), AccountID: "acct-1", RootID: "root-1", Namespace: "backup", Required: true, Complete: true, Evidence: "evidence-" + id}
}

func optionalRestoreReplica(id string) ReplicaEvidence {
	return ReplicaEvidence{ID: id, Provider: string(ProviderR2), AccountID: "acct-1", RootID: "root-1", Namespace: "backup", Complete: true, Evidence: "evidence-" + id}
}
func retentionObject(id string, modifiedAt time.Time) Object {
	return Object{
		Provider: string(ProviderR2), AccountID: "acct-1", RootID: "root-1", Namespace: "backup",
		Bucket: "source", Key: id, ObjectID: id, Version: "v1", Generation: "g1",
		ETag: "etag-" + id, Size: 4, Hash: "h-" + id, ModifiedAt: modifiedAt,
	}
}

func retentionInventory(now time.Time) Inventory {
	return Inventory{AccountID: "acct-1", RootID: "root-1", Namespace: "backup", CapturedAt: now.Add(-time.Minute), Objects: []Object{retentionObject("one", now.Add(-time.Hour)), retentionObject("two", now.Add(-time.Hour))}, RestoreSets: []RestoreSet{{ID: "old", CreatedAt: now.Add(-2 * time.Hour), Complete: true, Replicas: []ReplicaEvidence{validRestoreReplica("r-old")}}, {ID: "new", CreatedAt: now.Add(-time.Hour), Complete: true, Replicas: []ReplicaEvidence{validRestoreReplica("r-new")}}}}
}

func setRestoreProvider(inventory *Inventory, provider Provider) {
	for setIndex := range inventory.RestoreSets {
		for replicaIndex := range inventory.RestoreSets[setIndex].Replicas {
			inventory.RestoreSets[setIndex].Replicas[replicaIndex].Provider = string(provider)
		}
	}
}

func ownerMarker() OwnershipMarker {
	return OwnershipMarker{OwnerID: "job-1", AccountID: "acct-1", RootID: "root-1", Namespace: "backup", Marker: "marker-1"}
}

func validR2Policy() Policy {
	return Policy{ID: "policy-1", Provider: ProviderR2, DeletePolicy: DeletePolicyQuarantine, MinCompleteRestoreSets: 2, MaxObjects: 10, MaxBytes: 100, MinimumObjectAge: time.Hour, QuarantineBucket: "quarantine", ActivatedAt: time.Unix(100, 0).UTC()}
}

func TestPlannerRequiresAtLeastTwoCompleteRestoreSets(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	for _, test := range []struct {
		name      string
		minimum   int
		wantError bool
	}{
		{name: "zero", minimum: 0, wantError: true},
		{name: "one", minimum: 1, wantError: true},
		{name: "exact floor", minimum: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := validR2Policy()
			policy.MinCompleteRestoreSets = test.minimum
			plan, err := PlanRetention(policy, retentionInventory(now), ownerMarker(), now)
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "min_complete_restore_sets") {
					t.Fatalf("PlanRetention() error = %v, want restore floor rejection", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("PlanRetention() error = %v", err)
			}
			if len(plan.Actions) != 2 {
				t.Fatalf("actions = %d, want one action per inventory object", len(plan.Actions))
			}
		})
	}
}

func TestPlannerBindsPolicyThresholdIntoDigest(t *testing.T) {
	policy := validR2Policy()
	changed := policy
	changed.MinimumObjectAge += time.Second
	if PolicyDigest(policy) == PolicyDigest(changed) {
		t.Fatal("policy digest did not change when minimum object age changed")
	}
}

func TestPlannerRejectsOwnershipScopeAndProviderDrift(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	for _, test := range []struct {
		name   string
		mutate func(*Inventory, *OwnershipMarker)
	}{
		{name: "inventory account", mutate: func(inventory *Inventory, _ *OwnershipMarker) { inventory.AccountID = "foreign-account" }},
		{name: "inventory account required", mutate: func(inventory *Inventory, _ *OwnershipMarker) { inventory.AccountID = "" }},
		{name: "inventory root", mutate: func(inventory *Inventory, _ *OwnershipMarker) { inventory.RootID = "foreign-root" }},
		{name: "inventory root required", mutate: func(inventory *Inventory, _ *OwnershipMarker) { inventory.RootID = "" }},
		{name: "inventory namespace", mutate: func(inventory *Inventory, _ *OwnershipMarker) { inventory.Namespace = "foreign-namespace" }},
		{name: "inventory namespace required", mutate: func(inventory *Inventory, _ *OwnershipMarker) { inventory.Namespace = "" }},
		{name: "object provider", mutate: func(inventory *Inventory, _ *OwnershipMarker) { inventory.Objects[0].Provider = string(ProviderDrive) }},
		{name: "object provider required", mutate: func(inventory *Inventory, _ *OwnershipMarker) { inventory.Objects[0].Provider = "" }},
		{name: "object account", mutate: func(inventory *Inventory, _ *OwnershipMarker) { inventory.Objects[0].AccountID = "foreign-account" }},
		{name: "object root", mutate: func(inventory *Inventory, _ *OwnershipMarker) { inventory.Objects[0].RootID = "foreign-root" }},
		{name: "object namespace", mutate: func(inventory *Inventory, _ *OwnershipMarker) { inventory.Objects[0].Namespace = "foreign-namespace" }},
		{name: "owner account", mutate: func(_ *Inventory, owner *OwnershipMarker) { owner.AccountID = "foreign-account" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			inventory := retentionInventory(now)
			owner := ownerMarker()
			test.mutate(&inventory, &owner)
			if _, err := PlanRetention(validR2Policy(), inventory, owner, now); err == nil || !strings.Contains(err.Error(), "ownership") {
				t.Fatalf("PlanRetention() error = %v, want ownership rejection", err)
			}
		})
	}
}

func TestPlannerRequiresObjectTimestampAndMinimumAge(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	for _, test := range []struct {
		name      string
		modified  time.Time
		wantError string
	}{
		{name: "missing timestamp", wantError: "timestamp"},
		{name: "future timestamp", modified: now.Add(time.Second), wantError: "future"},
		{name: "too young", modified: now.Add(-time.Hour + time.Second), wantError: "minimum object age"},
		{name: "boundary age", modified: now.Add(-time.Hour)},
	} {
		t.Run(test.name, func(t *testing.T) {
			inventory := retentionInventory(now)
			inventory.Objects[0].ModifiedAt = test.modified
			_, err := PlanRetention(validR2Policy(), inventory, ownerMarker(), now)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("PlanRetention() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("PlanRetention() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestQuarantineRequiresExplicitMinimumObjectAge(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	policy := validR2Policy()
	policy.MinimumObjectAge = 0
	if _, err := PlanRetention(policy, retentionInventory(now), ownerMarker(), now); err == nil || !strings.Contains(err.Error(), "minimum object age") {
		t.Fatalf("PlanRetention() error = %v, want explicit minimum object age rejection", err)
	}
}

func TestDeletePolicyNoneProducesNoActions(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	policy := validR2Policy()
	policy.DeletePolicy = DeletePolicyNone
	policy.MinimumObjectAge = 0
	plan, err := PlanRetention(policy, retentionInventory(now), ownerMarker(), now)
	if err != nil {
		t.Fatalf("PlanRetention() error = %v", err)
	}
	if len(plan.Actions) != 0 {
		t.Fatalf("DeletePolicyNone actions = %d, want zero", len(plan.Actions))
	}
}

func TestNewestCompleteRestoreSetsRejectIncompleteRequiredReplica(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	inventory := retentionInventory(now)
	inventory.RestoreSets[1].Replicas[0].Complete = false
	if _, err := NewestCompleteRestoreSets(inventory, 1, ProviderR2); err == nil || !strings.Contains(err.Error(), "required replica") {
		t.Fatalf("incomplete restore set error = %v", err)
	}
	inventory.RestoreSets[1].Replicas[0].Complete = true
	sets, err := NewestCompleteRestoreSets(inventory, 1, ProviderR2)
	if err != nil {
		t.Fatalf("NewestCompleteRestoreSets() error = %v", err)
	}
	if len(sets) != 1 || sets[0].ID != "new" {
		t.Fatalf("sets = %+v", sets)
	}
}

func TestNewestCompleteRestoreSetsRejectsEmptyCompleteRecords(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	inventory := retentionInventory(now)
	inventory.RestoreSets = []RestoreSet{
		{ID: "empty-old", CreatedAt: now.Add(-2 * time.Hour), Complete: true},
		{ID: "empty-new", CreatedAt: now.Add(-time.Hour), Complete: true},
	}
	if _, err := NewestCompleteRestoreSets(inventory, 2, ProviderR2); err == nil || !strings.Contains(err.Error(), "required replica") {
		t.Fatalf("empty complete restore sets error = %v, want required replica rejection", err)
	}
}

func TestNewestCompleteRestoreSetsRejectsOptionalOnlyRecords(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	inventory := retentionInventory(now)
	inventory.RestoreSets = []RestoreSet{
		{ID: "optional-old", CreatedAt: now.Add(-2 * time.Hour), Complete: true, Replicas: []ReplicaEvidence{optionalRestoreReplica("optional-old")}},
		{ID: "optional-new", CreatedAt: now.Add(-time.Hour), Complete: true, Replicas: []ReplicaEvidence{optionalRestoreReplica("optional-new")}},
	}
	if _, err := NewestCompleteRestoreSets(inventory, 2, ProviderR2); err == nil || !strings.Contains(err.Error(), "required replica") {
		t.Fatalf("optional-only restore sets error = %v, want required replica rejection", err)
	}
}

func TestNewestCompleteRestoreSetsRejectsMissingRequiredEvidence(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	for _, test := range []struct {
		name   string
		mutate func(*ReplicaEvidence)
	}{
		{name: "id", mutate: func(replica *ReplicaEvidence) { replica.ID = "" }},
		{name: "provider", mutate: func(replica *ReplicaEvidence) { replica.Provider = "" }},
		{name: "provider drift", mutate: func(replica *ReplicaEvidence) { replica.Provider = string(ProviderDrive) }},
		{name: "account", mutate: func(replica *ReplicaEvidence) { replica.AccountID = "" }},
		{name: "root", mutate: func(replica *ReplicaEvidence) { replica.RootID = "" }},
		{name: "namespace", mutate: func(replica *ReplicaEvidence) { replica.Namespace = "" }},
		{name: "evidence", mutate: func(replica *ReplicaEvidence) { replica.Evidence = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			inventory := retentionInventory(now)
			test.mutate(&inventory.RestoreSets[1].Replicas[0])
			if _, err := NewestCompleteRestoreSets(inventory, 2, ProviderR2); err == nil || !strings.Contains(err.Error(), "required replica") {
				t.Fatalf("missing %s error = %v, want required replica rejection", test.name, err)
			}
		})
	}
}

func TestNewestCompleteRestoreSetsRejectsRequiredReplicaScopeDrift(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	for _, test := range []struct {
		name   string
		mutate func(*ReplicaEvidence)
	}{
		{name: "account", mutate: func(replica *ReplicaEvidence) { replica.AccountID = "foreign-account" }},
		{name: "root", mutate: func(replica *ReplicaEvidence) { replica.RootID = "foreign-root" }},
		{name: "namespace", mutate: func(replica *ReplicaEvidence) { replica.Namespace = "foreign-namespace" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			inventory := retentionInventory(now)
			test.mutate(&inventory.RestoreSets[1].Replicas[0])
			if _, err := NewestCompleteRestoreSets(inventory, 2, ProviderR2); err == nil || !strings.Contains(err.Error(), "scope") {
				t.Fatalf("scope drift %s error = %v, want scope rejection", test.name, err)
			}
		})
	}
}

func TestNewestCompleteRestoreSetsAcceptsAuthenticatedRequiredEvidence(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	inventory := retentionInventory(now)
	inventory.RestoreSets[0].CreatedAt = inventory.RestoreSets[1].CreatedAt
	sets, err := NewestCompleteRestoreSets(inventory, 2, ProviderR2)
	if err != nil {
		t.Fatalf("NewestCompleteRestoreSets() error = %v", err)
	}
	if len(sets) != 2 || sets[0].ID != "old" || sets[1].ID != "new" {
		t.Fatalf("sets = %+v, want deterministic descending-ID tie order", sets)
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
	setRestoreProvider(&inventory, ProviderDrive)
	policy := Policy{ID: "drive-default", Provider: ProviderDrive, MinCompleteRestoreSets: 2, MaxObjects: 10, MaxBytes: 100, ActivatedAt: time.Unix(99, 0).UTC()}
	plan, err := PlanRetention(policy, inventory, ownerMarker(), now)
	if err != nil {
		t.Fatalf("PlanRetention() error = %v", err)
	}
	if len(plan.Actions) != 0 || plan.Policy.DeletePolicy != DeletePolicyNone {
		t.Fatalf("default Drive plan = %+v", plan)
	}
}

func TestDriveQuarantinePlanningEmitsNonAuthoritativeIntent(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	inventory := retentionInventory(now)
	for index := range inventory.Objects {
		inventory.Objects[index].Provider = string(ProviderDrive)
	}
	setRestoreProvider(&inventory, ProviderDrive)
	policy := Policy{ID: "drive-quarantine", Provider: ProviderDrive, DeletePolicy: DeletePolicyQuarantine, MinCompleteRestoreSets: 2, MaxObjects: 10, MaxBytes: 100, MinimumObjectAge: time.Hour, QuarantineBucket: "quarantine", ActivatedAt: time.Unix(99, 0).UTC()}
	plan, err := PlanRetention(policy, inventory, ownerMarker(), now)
	if err != nil {
		t.Fatalf("Drive quarantine PlanRetention() error = %v", err)
	}
	if len(plan.Actions) != len(inventory.Objects) || plan.Actions[0].Kind != ActionDriveQuarantineIntent || plan.Actions[0].DriveIntent == nil {
		t.Fatalf("Drive quarantine plan = %+v, want intent-only actions", plan)
	}
	engine := NewEngine(nil, NewJournal())
	if _, err := engine.Apply(context.Background(), plan, policy); err == nil || !strings.Contains(err.Error(), "protected provider broker") {
		t.Fatalf("Drive quarantine Apply() error = %v, want fail-closed broker rejection", err)
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
	engine := NewEngine(provider, NewJournal())
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
	engine := NewEngine(provider, NewJournal())
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
	engine := NewEngine(provider, journal)
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
	engine = NewEngine(provider, journal)
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
	engine := NewEngine(provider, journal)
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
	secondEngine := NewEngine(provider, journal)
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
