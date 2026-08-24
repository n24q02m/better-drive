package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/restore"
)

func TestRestorePlanJSONValidatesCanonicalManifest(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifest, []byte(`[{"relative_path":"category/state.txt","source_path":"C:/source/state.txt","source_digest":"sha256:abc","size":3}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"restore", "plan", "--root", t.TempDir(), "--manifest", manifest, "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("restore plan: %v; stderr=%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "category/state.txt") || !strings.Contains(out.String(), "conflicts") {
		t.Fatalf("restore plan output = %s", out.String())
	}
}

func TestRestoreFetchRequiresExplicitMode(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"restore", "fetch", "--root", t.TempDir(), "--manifest", "missing.json"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatal("restore fetch without --dry-run/--execute was accepted")
	}
}

func TestRestoreFetchJournalsRecoveryIntentBeforeCreate(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	payload := []byte("restore-data")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	data, err := json.Marshal([]restore.Entry{{RelativePath: "category/state.txt", SourcePath: source, SourceDigest: "sha256:" + hex.EncodeToString(sum[:]), Size: 12}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	rootPath := t.TempDir()
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"restore", "fetch", "--root", rootPath, "--manifest", manifest, "--execute"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("restore fetch: %v; stderr=%s", err, errOut.String())
	}
	records, err := (restore.Journal{Path: filepath.Join(rootPath, ".restore-apply.jsonl")}).Read()
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(records) != 2 || records[0].After != "staged" || records[1].After != "created" ||
		records[0].TransactionID == "" || records[0].TransactionID != records[1].TransactionID {
		t.Fatalf("journal records=%#v, want one transaction with staged then created markers", records)
	}
}

func TestRestoreFetchFailureDoesNotRollbackPriorSuccessfulTransaction(t *testing.T) {
	rootPath := t.TempDir()
	priorPath := filepath.Join(rootPath, "prior.txt")
	priorPayload := []byte("prior")
	if err := os.WriteFile(priorPath, priorPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	priorDigest := sha256.Sum256(priorPayload)
	journal := restore.Journal{Path: filepath.Join(rootPath, ".restore-apply.jsonl")}
	for _, after := range []string{"staged", "created"} {
		if err := journal.Append(restore.JournalRecord{
			TransactionID: "prior-transaction",
			Entry:         "prior.txt", Action: "create", Before: "absent", After: after,
			SourceDigest: "sha256:" + hex.EncodeToString(priorDigest[:]),
		}); err != nil {
			t.Fatalf("append prior journal: %v", err)
		}
	}

	newSource := filepath.Join(t.TempDir(), "new.txt")
	if err := os.WriteFile(newSource, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	badSource := filepath.Join(t.TempDir(), "bad.txt")
	if err := os.WriteFile(badSource, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	newDigest := sha256.Sum256([]byte("new"))
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	data, err := json.Marshal([]restore.Entry{
		{RelativePath: "new.txt", SourcePath: newSource, SourceDigest: "sha256:" + hex.EncodeToString(newDigest[:]), Size: 3},
		{RelativePath: "bad.txt", SourcePath: badSource, SourceDigest: "sha256:" + strings.Repeat("0", 64), Size: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"restore", "fetch", "--root", rootPath, "--manifest", manifest, "--execute"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("restore fetch unexpectedly succeeded with bad digest")
	}
	if got, err := os.ReadFile(priorPath); err != nil || string(got) != "prior" {
		t.Fatalf("prior transaction file = %q, err=%v; want preserved", got, err)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("current transaction file was not recovered: %v", err)
	}
}

func TestRestoreApplyRemainsOwnerGated(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"restore", "apply"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "data-owner") {
		t.Fatal("restore apply without owner gate was accepted")
	}
}

func TestRestoreApplyUsesExplicitIsolatedTransaction(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	payload := []byte("restore-data")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	data, err := json.Marshal([]restore.Entry{{
		RelativePath: "category/state.txt", SourcePath: source,
		SourceDigest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(payload)),
		CiphertextDigest: "sha256:ciphertext",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	rootPath := t.TempDir()
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"restore", "apply", "--root", rootPath, "--manifest", manifest, "--transaction", "tx-cli"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("restore apply: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(rootPath, "category", "state.txt")); err != nil || string(got) != string(payload) {
		t.Fatalf("applied payload = %q, err=%v", got, err)
	}
	recoverCmd := newRootCmd()
	recoverCmd.SetOut(&bytes.Buffer{})
	recoverCmd.SetErr(&bytes.Buffer{})
	recoverCmd.SetArgs([]string{"restore", "recover", "--root", rootPath, "--transaction", "tx-cli"})
	if err := recoverCmd.Execute(); err != nil {
		t.Fatalf("restore recover: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, "category", "state.txt")); !os.IsNotExist(err) {
		t.Fatalf("recover left destination: %v", err)
	}
}

func TestRestoreApplyRejectsLiveReplaceAndIncompleteContract(t *testing.T) {
	for _, args := range [][]string{
		{"restore", "apply"},
		{"restore", "apply", "--root", t.TempDir(), "--manifest", "missing.json"},
		{"restore", "apply", "--root", t.TempDir(), "--manifest", "missing.json", "--transaction", "tx", "--replace"},
	} {
		cmd := newRootCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("restore apply accepted unsafe args %v", args)
		}
	}
}

func TestRestoreRecoverRequiresExplicitTransaction(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"restore", "recover", "--root", t.TempDir()})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "transaction") {
		t.Fatalf("restore recover error = %v, want explicit transaction rejection", err)
	}
}
