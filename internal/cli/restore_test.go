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

	"github.com/n24q02m/better-drive/internal/artifactcrypto"
	"github.com/n24q02m/better-drive/internal/restore"
	"github.com/spf13/cobra"
)

type cliArtifactResolver map[artifactcrypto.KeyReference][]byte

func (r cliArtifactResolver) Resolve(reference artifactcrypto.KeyReference) ([]byte, error) {
	key, ok := r[reference]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), key...), nil
}

func cliMetadata() artifactcrypto.Metadata {
	return artifactcrypto.Metadata{RestoreSetID: "set-1", Component: "state", KeyRef: "drive-state", KeyVersion: 7}
}

func cliSealedEntry(t *testing.T, relative string, payload []byte, metadata artifactcrypto.Metadata, resolver artifactcrypto.Resolver) restore.Entry {
	t.Helper()
	source := filepath.Join(t.TempDir(), "artifact.bin")
	file, err := os.OpenFile(source, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	result, sealErr := artifactcrypto.Seal(file, bytes.NewReader(payload), resolver, metadata)
	closeErr := file.Close()
	if sealErr != nil {
		t.Fatal(sealErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return restore.Entry{
		RelativePath: relative, SourcePath: source, ArtifactMetadata: metadata,
		PlaintextDigest: result.PlaintextDigest, CiphertextDigest: result.CiphertextDigest,
		PlaintextSize: int64(len(payload)),
	}
}

func restoreTestCmd(resolver artifactcrypto.Resolver) *cobra.Command {
	return restoreCmdWithResolver(resolver)
}

func TestRestorePlanJSONValidatesCanonicalManifest(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifest, []byte(`[{"relative_path":"category/state.txt"}]`), 0o600); err != nil {
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
	metadata := cliMetadata()
	resolver := cliArtifactResolver{metadata.Reference(): []byte("0123456789abcdef0123456789abcdef")}
	entry := cliSealedEntry(t, "category/state.txt", []byte("restore-data"), metadata, resolver)
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	data, err := json.Marshal([]restore.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	rootPath := t.TempDir()
	cmd := restoreTestCmd(resolver)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"fetch", "--root", rootPath, "--manifest", manifest, "--execute"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("restore fetch: %v; stderr=%s", err, errOut.String())
	}
	records, err := (restore.Journal{Path: filepath.Join(rootPath, ".restore-apply.jsonl")}).Read()
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(records) != 2 || records[0].After != "staged" || records[1].After != "created" ||
		records[0].TransactionID == "" || records[0].TransactionID != records[1].TransactionID ||
		records[0].PlaintextDigest != entry.PlaintextDigest || records[0].CiphertextDigest != entry.CiphertextDigest {
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
			PlaintextDigest: "sha256:" + hex.EncodeToString(priorDigest[:]),
		}); err != nil {
			t.Fatalf("append prior journal: %v", err)
		}
	}

	metadata := cliMetadata()
	resolver := cliArtifactResolver{metadata.Reference(): []byte("0123456789abcdef0123456789abcdef")}
	newEntry := cliSealedEntry(t, "new.txt", []byte("new"), metadata, resolver)
	badEntry := cliSealedEntry(t, "bad.txt", []byte("bad"), metadata, resolver)
	badEntry.PlaintextDigest = "sha256:" + strings.Repeat("0", 64)
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	data, err := json.Marshal([]restore.Entry{newEntry, badEntry})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := restoreTestCmd(resolver)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"fetch", "--root", rootPath, "--manifest", manifest, "--execute"})
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

func TestRestoreFetchExecuteRequiresArtifactResolver(t *testing.T) {
	metadata := cliMetadata()
	resolver := cliArtifactResolver{metadata.Reference(): []byte("0123456789abcdef0123456789abcdef")}
	entry := cliSealedEntry(t, "state.txt", []byte("restore-data"), metadata, resolver)
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	data, err := json.Marshal([]restore.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"restore", "fetch", "--root", t.TempDir(), "--manifest", manifest, "--execute"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "artifact resolver") {
		t.Fatalf("restore fetch without resolver error = %v", err)
	}
}

func TestRestoreApplyUsesExplicitIsolatedTransaction(t *testing.T) {
	payload := []byte("restore-data")
	metadata := cliMetadata()
	resolver := cliArtifactResolver{metadata.Reference(): []byte("0123456789abcdef0123456789abcdef")}
	entry := cliSealedEntry(t, "category/state.txt", payload, metadata, resolver)
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	data, err := json.Marshal([]restore.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	rootPath := t.TempDir()
	cmd := restoreTestCmd(resolver)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"apply", "--root", rootPath, "--manifest", manifest, "--transaction", "tx-cli"})
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
