package cleanup

import (
	"os"
	"strings"
	"testing"
)

func TestFileJournalPersistsAndVerifiesHashChain(t *testing.T) {
	path := t.TempDir() + "/journal.jsonl"
	journal, err := OpenFileJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(JournalRecord{Action: "validate", ObjectID: "object-1", Before: "active", After: "selected"}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(JournalRecord{Action: "mutate", ObjectID: "object-1", Before: "selected", After: "quarantined"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := OpenFileJournal(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	if len(loaded.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(loaded.Records))
	}
	if err := loaded.Verify(); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestFileJournalRejectsMalformedLine(t *testing.T) {
	path := t.TempDir() + "/journal.jsonl"
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileJournal(path); err == nil || !strings.Contains(err.Error(), "journal") {
		t.Fatalf("expected malformed journal rejection, got %v", err)
	}
}
