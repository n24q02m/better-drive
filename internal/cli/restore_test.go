package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

type cliStagingVerifier struct {
	calls  int
	mutate func(restore.StagingEvidence, int) restore.StagingEvidence
}

func (v *cliStagingVerifier) Verify(_ string, identity restore.RootIdentity) (restore.StagingEvidence, error) {
	v.calls++
	evidence := restore.StagingEvidence{
		RootIdentity:       identity,
		ProofDigest:        cliDigest(identity.Path + "|staging-proof"),
		ProofID:            "proof-1",
		VerifiedAt:         time.Unix(1700000000, 0).UTC(),
		EncryptedAtRest:    true,
		OwnerOnly:          true,
		ExcludedFromBackup: true,
		NonInheritedACL:    true,
	}
	if v.mutate != nil {
		evidence = v.mutate(evidence, v.calls)
	}
	return evidence, nil
}

type cliSourceProvider struct {
	artifacts map[string][]byte
}

func (p *cliSourceProvider) Open(_ context.Context, ref restore.SourceReference) (io.ReadCloser, restore.SourceReadback, error) {
	key := fmt.Sprintf("%s/%s/%s/%s", ref.Provider, ref.AccountID, ref.ObjectID, ref.Version)
	data, ok := p.artifacts[key]
	if !ok {
		return nil, restore.SourceReadback{}, fmt.Errorf("source artifact %q not found in test provider", key)
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	readback := restore.SourceReadback{
		Reference:        ref,
		Size:             int64(len(data)),
		CiphertextDigest: digest,
		Version:          ref.Version,
	}
	return io.NopCloser(bytes.NewReader(data)), readback, nil
}

type cliCheckpointVerifier struct{}

func (v *cliCheckpointVerifier) Verify(_ context.Context, checkpoint restore.MachineCheckpoint, intent restore.ApplyIntent) error {
	if checkpoint.Signature != "valid-sig" {
		return fmt.Errorf("invalid checkpoint signature")
	}
	return nil
}

type cliCleanupVerifier struct{}

func (v *cliCleanupVerifier) Verify(_ context.Context, claim restore.CleanupClaim, intent restore.CleanupIntent) error {
	if claim.Signature != "valid-claim-sig" {
		return fmt.Errorf("invalid cleanup claim signature")
	}
	return nil
}

func cliDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
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
		RelativePath:     relative,
		SourcePath:       source,
		ArtifactMetadata: metadata,
		PlaintextDigest:  result.PlaintextDigest,
		CiphertextDigest: result.CiphertextDigest,
		PlaintextSize:    int64(len(payload)),
	}
}

func restoreTestCmd(deps RestoreDependencies) *cobra.Command {
	return restoreCmdWithRestoreDependencies(deps)
}

func restoreDeps(resolver artifactcrypto.Resolver) RestoreDependencies {
	return RestoreDependencies{
		ArtifactResolver:   resolver,
		StagingVerifier:    &cliStagingVerifier{},
		CheckpointVerifier: &cliCheckpointVerifier{},
		CleanupVerifier:    &cliCleanupVerifier{},
	}
}

func TestRestorePlanJSONValidatesCanonicalManifest(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifest, []byte(`[{"relative_path":"category/state.txt","plaintext_size":120}]`), 0o600); err != nil {
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
	if !strings.Contains(out.String(), "category/state.txt") || !strings.Contains(out.String(), "capacity_bytes") {
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
	cmd := restoreTestCmd(restoreDeps(resolver))
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

func TestRootRestoreUsesInjectedDependencies(t *testing.T) {
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
	rootPath := t.TempDir()
	cmd := newRootCmdWithDependencies(RuntimeDependencies{
		ArtifactResolver: resolver,
		StagingVerifier:  &cliStagingVerifier{},
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"restore", "fetch", "--root", rootPath, "--manifest", manifest, "--execute"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("root restore fetch: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(rootPath, "state.txt")); err != nil || string(got) != "restore-data" {
		t.Fatalf("root restore payload = %q, err=%v", got, err)
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

	cmd := restoreTestCmd(restoreDeps(resolver))
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

func TestRestoreFetchExecuteRequiresStagingVerifierBeforeJournal(t *testing.T) {
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
	rootPath := t.TempDir()
	cmd := restoreTestCmd(RestoreDependencies{ArtifactResolver: resolver})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"fetch", "--root", rootPath, "--manifest", manifest, "--execute"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "staging verifier") {
		t.Fatalf("restore fetch without staging verifier error = %v", err)
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("restore root residue after missing verifier: %#v", entries)
	}
}

func TestRestoreFetchEvidenceFailureLeavesNoExecutionResidue(t *testing.T) {
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
	rootPath := t.TempDir()
	verifier := &cliStagingVerifier{mutate: func(e restore.StagingEvidence, _ int) restore.StagingEvidence {
		e.EncryptedAtRest = false
		return e
	}}
	cmd := restoreTestCmd(RestoreDependencies{ArtifactResolver: resolver, StagingVerifier: verifier})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"fetch", "--root", rootPath, "--manifest", manifest, "--execute"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("restore fetch evidence failure = %v", err)
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("restore root residue after evidence failure: %#v", entries)
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
	cmd := restoreTestCmd(restoreDeps(resolver))
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

func TestRestoreApplyReplaceWithMachineCheckpoint(t *testing.T) {
	rootPath := t.TempDir()
	destPath := filepath.Join(rootPath, "file.txt")
	if err := os.WriteFile(destPath, []byte("old-content"), 0o600); err != nil {
		t.Fatal(err)
	}

	metadata := cliMetadata()
	resolver := cliArtifactResolver{metadata.Reference(): []byte("0123456789abcdef0123456789abcdef")}
	entry := cliSealedEntry(t, "file.txt", []byte("new-content"), metadata, resolver)
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	data, err := json.Marshal([]restore.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}

	identity, err := restore.CaptureRootIdentity(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := restore.MachineCheckpoint{
		ID:            "cp-replace-1",
		Kind:          restore.CheckpointKindReplace,
		Root:          rootPath,
		RootIdentity:  identity,
		Entries:       []string{"file.txt"},
		CapacityBytes: int64(len("new-content")),
		ExpiresAt:     time.Now().Add(1 * time.Hour),
		Signature:     "valid-sig",
	}
	checkpointPath := filepath.Join(t.TempDir(), "checkpoint.json")
	cpData, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpointPath, cpData, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := restoreTestCmd(restoreDeps(resolver))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"apply", "--root", rootPath, "--manifest", manifest, "--transaction", "tx-replace", "--replace", "--checkpoint", checkpointPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("restore apply replace: %v", err)
	}

	got, err := os.ReadFile(destPath)
	if err != nil || string(got) != "new-content" {
		t.Fatalf("replaced content = %q err=%v", got, err)
	}

	// Recover transaction rolls back to original content.
	recoverCmd := newRootCmd()
	recoverCmd.SetOut(&bytes.Buffer{})
	recoverCmd.SetErr(&bytes.Buffer{})
	recoverCmd.SetArgs([]string{"restore", "recover", "--root", rootPath, "--transaction", "tx-replace"})
	if err := recoverCmd.Execute(); err != nil {
		t.Fatalf("restore recover replace: %v", err)
	}

	restored, err := os.ReadFile(destPath)
	if err != nil || string(restored) != "old-content" {
		t.Fatalf("recovered content = %q err=%v", restored, err)
	}
}

func TestRestoreApplyRejectsLiveReplaceWithoutCheckpoint(t *testing.T) {
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

func TestRestoreCleanupCommand(t *testing.T) {
	rootPath := t.TempDir()
	stagingDir, _ := restore.StagingRollbackDirs(rootPath)
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(stagingDir, "stage.tmp")
	if err := os.WriteFile(testFile, []byte("temp-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Check TTL passes for fresh file.
	cmd := restoreTestCmd(restoreDeps(nil))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"cleanup", "--root", rootPath, "--check-ttl", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("restore cleanup --check-ttl: %v", err)
	}
	if !strings.Contains(out.String(), "ttl_ok") {
		t.Fatalf("cleanup output = %s", out.String())
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
