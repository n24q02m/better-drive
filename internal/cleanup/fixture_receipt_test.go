package cleanup

import (
	"strings"
	"testing"
	"time"
)

func TestGitFixtureLifecycleReceiptStoreIsCreateOnlyAndPhaseFenced(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	repo, err := NewGitRepo(protectedTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewGitFixtureLifecycleReceiptStore(repo, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewGitFixtureLifecycleReceiptStore(repo, func() time.Time { return now.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	sequence := []string{"quarantine", "restore", "requarantine"}
	receipt, oid, err := first.Begin(strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), sequence)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != FixtureReceiptClaimed || receipt.PhaseIndex != 0 {
		t.Fatalf("initial receipt = %+v", receipt)
	}
	if _, _, err := second.Begin(strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), sequence); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("duplicate begin error = %v", err)
	}

	attempting, attemptingOID, err := first.StartPhase(strings.Repeat("a", 64), oid, 0, "quarantine")
	if err != nil {
		t.Fatal(err)
	}
	if attempting.State != FixtureReceiptAttempting || attempting.Phase != "quarantine" {
		t.Fatalf("attempting receipt = %+v", attempting)
	}
	completed, completedOID, err := first.CompletePhase(strings.Repeat("a", 64), attemptingOID, 0, "quarantine", strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != FixtureReceiptClaimed || completed.PhaseIndex != 1 || len(completed.Completed) != 1 {
		t.Fatalf("completed phase receipt = %+v", completed)
	}
	if _, _, err := first.CompletePhase(strings.Repeat("a", 64), attemptingOID, 0, "quarantine", strings.Repeat("d", 64)); err == nil {
		t.Fatal("stale phase completion advanced the receipt")
	}

	currentOID := completedOID
	for index, phase := range sequence[1:] {
		phaseIndex := index + 1
		_, phaseOID, err := first.StartPhase(strings.Repeat("a", 64), currentOID, phaseIndex, phase)
		if err != nil {
			t.Fatal(err)
		}
		receipt, currentOID, err = first.CompletePhase(strings.Repeat("a", 64), phaseOID, phaseIndex, phase, strings.Repeat(string(rune('e'+index)), 64))
		if err != nil {
			t.Fatal(err)
		}
	}
	if receipt.State != FixtureReceiptConsumed || receipt.PhaseIndex != len(sequence) || len(receipt.Completed) != len(sequence) {
		t.Fatalf("final receipt = %+v", receipt)
	}
}
