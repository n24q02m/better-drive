package cleanup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovalStoreIsCreateOnlyAndRejectsForeignDraft(t *testing.T) {
	store := NewApprovalStore(t.TempDir())
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
	if _, err := os.Stat(filepath.Join(store.Root, "cleanup-drafts", approval.ApprovalID+".json")); err != nil {
		t.Fatalf("expected draft file: %v", err)
	}
}

func TestApprovalStoreActivateAndReadState(t *testing.T) {
	store := NewApprovalStore(t.TempDir())
	approval := validApproval()
	if _, err := store.Prepare(approval); err != nil {
		t.Fatal(err)
	}
	intent := Intent{SchemaVersion: CurrentApprovalSchemaVersion, IntentDigest: strings.Repeat("c", 64), Approval: approval, SignatureHex: "sig", State: ApprovalApproved}
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
	if err := store.Activate(intent); err == nil || !strings.Contains(err.Error(), "foreign") {
		t.Fatalf("expected foreign intent rejection, got %v", err)
	}
}
