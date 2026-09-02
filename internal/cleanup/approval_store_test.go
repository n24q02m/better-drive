package cleanup

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestApprovalStore(t *testing.T, root string) *ApprovalStore {
	t.Helper()
	store, err := NewApprovalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestNewApprovalStoreReturnsInitializationErrors(t *testing.T) {
	if _, err := NewApprovalStore(""); err == nil {
		t.Fatal("empty approval store root was accepted")
	}
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewApprovalStore(path); err == nil {
		t.Fatal("approval store initialization error was swallowed")
	}
}

func TestApprovalStoreIsCreateOnlyAndRejectsForeignDraft(t *testing.T) {
	root := protectedTestDir(t)
	store := newTestApprovalStore(t, root)
	approval := validApproval()
	draft, err := store.Prepare(approval)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if draft.DraftDigest == "" {
		t.Fatal("expected draft digest")
	}
	if _, err := store.Prepare(approval); err != nil {
		t.Fatalf("idempotent Prepare() error = %v", err)
	}
	changed := approval
	changed.MaxBytes++
	if _, err := store.Prepare(changed); err == nil || !strings.Contains(err.Error(), "foreign") {
		t.Fatalf("expected foreign draft rejection, got %v", err)
	}
	draftOID, exists, err := store.DraftTransport.ReadRef(DraftRef(approval.ApprovalID))
	if err != nil || !exists || !gitOIDPattern.MatchString(draftOID) {
		t.Fatalf("expected draft ref to resolve to an exact Git OID: oid=%q exists=%v err=%v", draftOID, exists, err)
	}
}

func TestApprovalStoreRejectsPathTraversalApprovalIDs(t *testing.T) {
	store := newTestApprovalStore(t, protectedTestDir(t))
	for _, approvalID := range []string{"../escape", `..\escape`, "..", `C:\escape`, "C:escape", "approval/child", "approval\\child", "approval\x00child", "approval\nchild"} {
		t.Run(strings.ReplaceAll(approvalID, "\x00", "nul"), func(t *testing.T) {
			approval := validApproval()
			approval.ApprovalID = approvalID
			if _, err := store.Prepare(approval); err == nil {
				t.Fatalf("Prepare(%q) unexpectedly succeeded", approvalID)
			}
			if _, err := store.ReadState(approvalID); err == nil {
				t.Fatalf("ReadState(%q) unexpectedly succeeded", approvalID)
			}
		})
	}
}

func TestApprovalStoreActivateAndReadState(t *testing.T) {
	store := newTestApprovalStore(t, protectedTestDir(t))
	approval := validApproval()
	if _, err := store.Prepare(approval); err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalApproval(approval)
	if err != nil {
		t.Fatal(err)
	}
	intent := Intent{
		SchemaVersion: CurrentApprovalSchemaVersion,
		IntentDigest:  Digest(canonical),
		Approval:      approval,
		SignatureHex:  strings.Repeat("00", ed25519.SignatureSize),
		State:         ApprovalApproved,
	}
	if err := store.Activate(intent); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	got, err := store.ReadState(approval.ApprovalID)
	if err != nil {
		t.Fatalf("ReadState() error = %v", err)
	}
	if got.State != ApprovalApproved || got.IntentDigest != intent.IntentDigest {
		t.Fatalf("unexpected state: %+v", got)
	}

	intentOID, exists, err := store.RuntimeTransport.ReadRef(IntentRef(approval.ApprovalID))
	if err != nil || !exists {
		t.Fatalf("read intent ref: oid=%q exists=%v err=%v", intentOID, exists, err)
	}
	stateOID, exists, err := store.RuntimeTransport.ReadRef(StateRef(approval.ApprovalID))
	if err != nil || !exists {
		t.Fatalf("read state ref: oid=%q exists=%v err=%v", stateOID, exists, err)
	}
	stateData, err := store.RuntimeTransport.ReadBlob(stateOID)
	if err != nil {
		t.Fatal(err)
	}
	var stateRecord StateRecord
	if err := json.Unmarshal(stateData, &stateRecord); err != nil {
		t.Fatal(err)
	}
	if stateRecord.IntentOID != intentOID {
		t.Fatalf("state intent OID = %q, want authoritative ref OID %q", stateRecord.IntentOID, intentOID)
	}
	snapshot, err := store.ReadSnapshot(approval.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.IntentOID != intentOID || snapshot.StateOID != stateOID ||
		snapshot.JournalOID != "" || snapshot.LeaseOID != "" ||
		snapshot.State.State != ApprovalApproved {
		t.Fatalf("unexpected execution snapshot: %+v", snapshot)
	}
	if err := store.Activate(intent); err != nil {
		t.Fatalf("idempotent Activate() error = %v", err)
	}
	intent.IntentDigest = strings.Repeat("d", 64)
	if err := store.Activate(intent); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("expected intent digest rejection, got %v", err)
	}

	repo, err := NewGitRepo(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	intentData, err := repo.ReadBlob(intentOID)
	if err != nil {
		t.Fatal(err)
	}
	foreignOID, err := repo.WriteBlob(append(intentData, '\n'))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CAS(IntentRef(approval.ApprovalID), intentOID, foreignOID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadState(approval.ApprovalID); err == nil || !strings.Contains(err.Error(), "bound intent OID") {
		t.Fatalf("expected moved intent ref rejection, got %v", err)
	}
}

func TestApprovalStoreRejectsIntentDigestDrift(t *testing.T) {
	store := newTestApprovalStore(t, protectedTestDir(t))
	approval := validApproval()
	if _, err := store.Prepare(approval); err != nil {
		t.Fatal(err)
	}
	intent := Intent{
		SchemaVersion: CurrentApprovalSchemaVersion,
		IntentDigest:  strings.Repeat("c", 64),
		Approval:      approval,
		SignatureHex:  strings.Repeat("00", ed25519.SignatureSize),
		State:         ApprovalApproved,
	}
	if err := store.Activate(intent); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("expected intent digest rejection, got %v", err)
	}
}

type activationOnlyTransport struct{}

func (activationOnlyTransport) CreateSealedIntentAndState(string, string, string, StateRecord) (string, string, error) {
	return "", "", nil
}

func (activationOnlyTransport) ReadRef(string) (string, bool, error) {
	return "", false, nil
}

func (activationOnlyTransport) ReadBlob(string) ([]byte, error) {
	return nil, nil
}

func TestApprovalStoreReadStateRequiresRuntimeAuthority(t *testing.T) {
	store := &ApprovalStore{ActivationTransport: activationOnlyTransport{}}
	if _, err := store.ReadState("approval-test"); err == nil || !strings.Contains(err.Error(), "runtime authority") {
		t.Fatalf("expected runtime authority error, got %v", err)
	}
}

func TestApprovalStoreTransportAuthorityDenial(t *testing.T) {
	repo, err := NewGitRepo(protectedTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	transport := NewLocalGitTransport(repo)

	// Draft transport cannot write outside refs/cleanup-drafts/
	if _, err := transport.CreateDraft("refs/cleanup-intents/test", []byte("data")); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Draft transport allowed writing outside draft namespace: %v", err)
	}

	// Activation transport cannot write outside cleanup-intents or cleanup-states
	if _, _, err := transport.CreateSealedIntentAndState("refs/cleanup-drafts/test", "data", "refs/cleanup-states/test", StateRecord{}); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Activation transport allowed writing draft namespace: %v", err)
	}

	_, err = transport.CommitRuntimeTransition(RuntimeAuthorityTransition{
		State: RuntimeRefMutation{
			Ref:  "refs/cleanup-drafts/test",
			Data: "state",
		},
		Journal: RuntimeRefMutation{
			Ref:  "refs/cleanup-journals/test",
			Data: "journal",
		},
		Lease: RuntimeRefMutation{
			Ref:  "refs/destination-leases/test",
			Data: "lease",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Runtime transport allowed an atomic update outside its namespace: %v", err)
	}
}

func TestGitRepoCASAndConcurrency(t *testing.T) {
	repo, err := NewGitRepo(protectedTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/destination-leases/lease-1"
	oid1, err := repo.WriteBlob([]byte("content-1"))
	if err != nil {
		t.Fatal(err)
	}
	oid2, err := repo.WriteBlob([]byte("content-2"))
	if err != nil {
		t.Fatal(err)
	}
	oid3, err := repo.WriteBlob([]byte("content-3"))
	if err != nil {
		t.Fatal(err)
	}

	// Initial create with expectedOID=""
	if err := repo.CAS(ref, "", oid1); err != nil {
		t.Fatalf("initial CAS create error = %v", err)
	}

	// Stale CAS fails
	if err := repo.CAS(ref, strings.Repeat("f", 40), oid2); err == nil || !strings.Contains(err.Error(), "transaction") {
		t.Fatalf("stale CAS expected transaction error, got %v", err)
	}

	// Valid CAS succeeds
	if err := repo.CAS(ref, oid1, oid2); err != nil {
		t.Fatalf("valid CAS error = %v", err)
	}

	// Readback matches
	readOID, exists, err := repo.ReadRef(ref)
	if err != nil || !exists || readOID != oid2 {
		t.Fatalf("readback OID = %q (exists=%v, err=%v), want %q", readOID, exists, err, oid2)
	}

	// Advance once more
	if err := repo.CAS(ref, oid2, oid3); err != nil {
		t.Fatalf("second CAS error = %v", err)
	}
}

func TestGitRepoAtomicUpdateRejectsStaleMemberWithoutMovingAnyRef(t *testing.T) {
	repo, err := NewGitRepo(protectedTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	refs := []string{
		"refs/cleanup-states/approval-1",
		"refs/cleanup-journals/approval-1",
		"refs/destination-leases/target-1",
	}
	oldOIDs := make([]string, len(refs))
	newOIDs := make([]string, len(refs))
	for i := range refs {
		oldOIDs[i], err = repo.WriteBlob([]byte("old-" + refs[i]))
		if err != nil {
			t.Fatal(err)
		}
		newOIDs[i], err = repo.WriteBlob([]byte("new-" + refs[i]))
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CAS(refs[i], "", oldOIDs[i]); err != nil {
			t.Fatal(err)
		}
	}

	err = repo.AtomicUpdateRefs(
		GitRefUpdate{Ref: refs[0], ExpectedOID: oldOIDs[0], NewOID: newOIDs[0]},
		GitRefUpdate{Ref: refs[1], ExpectedOID: strings.Repeat("f", 40), NewOID: newOIDs[1]},
		GitRefUpdate{Ref: refs[2], ExpectedOID: oldOIDs[2], NewOID: newOIDs[2]},
	)
	if err == nil || !strings.Contains(err.Error(), "transaction") {
		t.Fatalf("expected stale transaction rejection, got %v", err)
	}
	for i, ref := range refs {
		got, exists, readErr := repo.ReadRef(ref)
		if readErr != nil || !exists || got != oldOIDs[i] {
			t.Fatalf("%s moved after rejected transaction: got=%q exists=%v err=%v want=%q", ref, got, exists, readErr, oldOIDs[i])
		}
	}
}

func TestActivationTransactionDoesNotCreateIntentWhenStateAlreadyExists(t *testing.T) {
	repo, err := NewGitRepo(protectedTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	transport := NewLocalGitTransport(repo)
	stateRef := StateRef("approval-atomic")
	existingStateOID, err := repo.WriteBlob([]byte("existing state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CAS(stateRef, "", existingStateOID); err != nil {
		t.Fatal(err)
	}

	intentRef := IntentRef("approval-atomic")
	_, _, err = transport.CreateSealedIntentAndState(intentRef, "sealed intent", stateRef, StateRecord{
		SchemaVersion: CurrentApprovalSchemaVersion,
		ApprovalID:    "approval-atomic",
		IntentRef:     intentRef,
		State:         ApprovalApproved,
	})
	if err == nil {
		t.Fatal("expected activation transaction conflict")
	}
	if oid, exists, readErr := repo.ReadRef(intentRef); readErr != nil || exists {
		t.Fatalf("intent ref was created by rejected activation: oid=%q exists=%v err=%v", oid, exists, readErr)
	}
	if oid, exists, readErr := repo.ReadRef(stateRef); readErr != nil || !exists || oid != existingStateOID {
		t.Fatalf("existing state moved during rejected activation: oid=%q exists=%v err=%v", oid, exists, readErr)
	}
}

func TestRuntimeTransitionCommitsStateJournalAndLeaseAtomically(t *testing.T) {
	repo, err := NewGitRepo(protectedTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	transport := NewLocalGitTransport(repo)
	transition := RuntimeAuthorityTransition{
		State: RuntimeRefMutation{
			Ref:  StateRef("approval-runtime"),
			Data: "claimed state",
		},
		Journal: RuntimeRefMutation{
			Ref:  JournalRef("approval-runtime"),
			Data: "claim journal",
		},
		Lease: RuntimeRefMutation{
			Ref:  LeaseRef("target-runtime"),
			Data: "claimed lease",
		},
	}
	first, err := transport.CommitRuntimeTransition(transition)
	if err != nil {
		t.Fatalf("initial runtime transition: %v", err)
	}
	for ref, want := range map[string]string{
		transition.State.Ref:   first.StateOID,
		transition.Journal.Ref: first.JournalOID,
		transition.Lease.Ref:   first.LeaseOID,
	} {
		got, exists, readErr := repo.ReadRef(ref)
		if readErr != nil || !exists || got != want {
			t.Fatalf("%s = %q exists=%v err=%v, want %q", ref, got, exists, readErr, want)
		}
	}

	second := RuntimeAuthorityTransition{
		State: RuntimeRefMutation{
			Ref:         transition.State.Ref,
			ExpectedOID: first.StateOID,
			Data:        "consumed state",
		},
		Journal: RuntimeRefMutation{
			Ref:         transition.Journal.Ref,
			ExpectedOID: strings.Repeat("e", 40),
			Data:        "settlement journal",
		},
		Lease: RuntimeRefMutation{
			Ref:         transition.Lease.Ref,
			ExpectedOID: first.LeaseOID,
			Data:        "consumed lease",
		},
	}
	if _, err := transport.CommitRuntimeTransition(second); err == nil {
		t.Fatal("expected stale runtime transition rejection")
	}
	for ref, want := range map[string]string{
		transition.State.Ref:   first.StateOID,
		transition.Journal.Ref: first.JournalOID,
		transition.Lease.Ref:   first.LeaseOID,
	} {
		got, exists, readErr := repo.ReadRef(ref)
		if readErr != nil || !exists || got != want {
			t.Fatalf("%s moved after rejected runtime transition: got=%q exists=%v err=%v want=%q", ref, got, exists, readErr, want)
		}
	}
}

func TestGitRepoIgnoresAmbientObjectDirectoryOverride(t *testing.T) {
	ambientObjects := t.TempDir()
	t.Setenv("GIT_OBJECT_DIRECTORY", ambientObjects)

	repo, err := NewGitRepo(protectedTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	oid, err := repo.WriteBlob([]byte("authority record"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReadBlob(oid); err != nil {
		t.Fatalf("read native authority blob: %v", err)
	}
	entries, err := os.ReadDir(ambientObjects)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("ambient GIT_OBJECT_DIRECTORY captured cleanup authority objects: %v", entries)
	}
}
