package cleanup

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApprovalStoreIsCreateOnlyAndRejectsForeignDraft(t *testing.T) {
	root := t.TempDir()
	store := NewApprovalStore(root)
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
	refPath := filepath.Join(store.Root, "refs", "cleanup-drafts", approval.ApprovalID)
	if _, err := os.Stat(refPath); err != nil {
		t.Fatalf("expected draft ref file: %v", err)
	}
}

func TestApprovalStoreRejectsPathTraversalApprovalIDs(t *testing.T) {
	store := NewApprovalStore(t.TempDir())
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
	store := NewApprovalStore(t.TempDir())
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
	if err := store.Activate(intent); err != nil {
		t.Fatalf("idempotent Activate() error = %v", err)
	}
	intent.IntentDigest = strings.Repeat("d", 64)
	if err := store.Activate(intent); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("expected intent digest rejection, got %v", err)
	}
}

func TestApprovalStoreRejectsIntentDigestDrift(t *testing.T) {
	store := NewApprovalStore(t.TempDir())
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

func TestApprovalStoreTransportAuthorityDenial(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transport := NewLocalGitTransport(repo)

	// Draft transport cannot write outside refs/cleanup-drafts/
	if _, err := transport.CreateDraft("refs/cleanup-intents/test", []byte("data")); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Draft transport allowed writing outside draft namespace: %v", err)
	}

	// Activation transport cannot write outside cleanup-intents or cleanup-states
	if _, _, err := transport.CreateSealedIntentAndState("refs/cleanup-drafts/test", "data", "refs/cleanup-states/test", "data"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Activation transport allowed writing draft namespace: %v", err)
	}

	// Runtime transport cannot write outside cleanup-states, destination-leases, or cleanup-journals
	if _, err := transport.CASState("refs/cleanup-drafts/test", "", "data"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Runtime transport allowed state update in draft namespace: %v", err)
	}
	if _, err := transport.CASLease("refs/cleanup-states/test", "", "data"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Runtime transport allowed lease update in states namespace: %v", err)
	}
	if _, err := transport.AppendJournal("refs/destination-leases/test", "", "data"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Runtime transport allowed journal append in lease namespace: %v", err)
	}
}

func TestGitRepoCASAndConcurrency(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
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
	if err := repo.CAS(ref, "stale-oid", oid2); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale CAS expected error, got %v", err)
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
