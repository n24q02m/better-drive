package restore

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func digestBytes(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestBuildPlanRejectsTraversalAbsoluteAndDuplicatePaths(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{"C:/escape", "/absolute", "../escape", "a/../../escape", "secret:stream"} {
		if _, err := BuildPlan(root, []Entry{{RelativePath: relative}}); err == nil {
			t.Errorf("BuildPlan accepted unsafe path %q", relative)
		}
	}
	if _, err := BuildPlan(root, []Entry{{RelativePath: "a.txt"}, {RelativePath: "a.txt"}}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatal("BuildPlan accepted duplicate destination path")
	}
}

func TestStageFileWritesIsolatedNoOverwriteAndVerifiesDigest(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("restore-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := Entry{RelativePath: "category/state.txt", SourcePath: source, SourceDigest: digestBytes("restore-data"), Size: int64(len("restore-data"))}
	plan, err := BuildPlan(root, []Entry{entry})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if err := StageFile(plan, entry); err != nil {
		t.Fatalf("StageFile: %v", err)
	}
	if err := StageFile(plan, entry); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatal("StageFile overwrote an existing destination")
	}
	got, err := os.ReadFile(filepath.Join(root, "category", "state.txt"))
	if err != nil || string(got) != "restore-data" {
		t.Fatalf("staged data = %q err=%v", got, err)
	}
}

func TestApplyJournalRoundTripAndRecoveryMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apply.jsonl")
	journal := Journal{Path: path}
	record := JournalRecord{TransactionID: "tx-1", Entry: "category/state.txt", Action: "create", Before: "absent", After: "created", SourceDigest: digestBytes("restore-data")}
	if err := journal.Append(record); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := journal.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].After != "created" {
		t.Fatalf("journal = %#v", got)
	}
	if err := journal.ValidateRecovery(got); err != nil {
		t.Fatalf("ValidateRecovery: %v", err)
	}
}

func TestRecoverCreateOnlyRemovesOnlyMatchingCreatedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "category", "state.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("restore-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	records := []JournalRecord{{TransactionID: "tx-1", Entry: "category/state.txt", Action: "create", Before: "absent", After: "created", SourceDigest: digestBytes("restore-data")}}
	if err := RecoverCreateOnly(root, records); err != nil {
		t.Fatalf("RecoverCreateOnly: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("recovered path error=%v, want removed", err)
	}

	if err := os.WriteFile(path, []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecoverCreateOnly(root, records); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("RecoverCreateOnly mismatch error=%v, want digest refusal", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("mismatched destination disappeared: %v", err)
	}
}

func TestRecoverCreateOnlyRejectsSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "state.txt")
	if err := os.WriteFile(outsidePath, []byte("restore-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "category")); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	records := []JournalRecord{{
		TransactionID: "tx-1",
		Entry:         "category/state.txt", Action: "create", Before: "absent", After: "created",
		SourceDigest: digestBytes("restore-data"),
	}}

	if err := RecoverCreateOnly(root, records); err == nil || !strings.Contains(err.Error(), "safe directory") {
		t.Fatalf("RecoverCreateOnly symlink ancestor error = %v, want refusal", err)
	}
	if _, err := os.Lstat(outsidePath); err != nil {
		t.Fatalf("outside file was touched: %v", err)
	}
}
