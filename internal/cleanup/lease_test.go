package cleanup

import (
	"strings"
	"testing"
)

func TestLeaseTransitionsAreSingleUseAndFenced(t *testing.T) {
	lease := Lease{ID: "lease-1", ManifestDigest: strings.Repeat("a", 64), State: LeaseApproved, Generation: 1}
	claimed, err := ClaimLease(lease, "exec-1")
	if err != nil {
		t.Fatalf("ClaimLease() error = %v", err)
	}
	if claimed.State != LeaseClaimed || claimed.ExecutionID != "exec-1" || claimed.Generation != 2 {
		t.Fatalf("unexpected claimed lease: %+v", claimed)
	}
	if _, err := ClaimLease(claimed, "exec-2"); err == nil || !strings.Contains(err.Error(), "claimed") {
		t.Fatalf("expected second claimant rejection, got %v", err)
	}
	consumed, err := ConsumeLease(claimed, "exec-1")
	if err != nil {
		t.Fatalf("ConsumeLease() error = %v", err)
	}
	if consumed.State != LeaseConsumed || consumed.Generation != 3 {
		t.Fatalf("unexpected consumed lease: %+v", consumed)
	}
	if _, err := ConsumeLease(consumed, "exec-1"); err == nil {
		t.Fatal("consumed lease unexpectedly reusable")
	}
}

func TestJournalHashChainRejectsTampering(t *testing.T) {
	journal := NewJournal()
	if err := journal.Append(JournalRecord{Action: "validate", ObjectID: "object-1", Before: "active", After: "selected"}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(JournalRecord{Action: "mutate", ObjectID: "object-1", Before: "selected", After: "quarantined"}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Verify(); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	journal.Records[1].After = "trash"
	if err := journal.Verify(); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("expected tamper rejection, got %v", err)
	}
}
