package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

func writeCleanupTestManifest(t *testing.T, dir string) string {
	t.Helper()
	manifest := cleanup.Manifest{
		SchemaVersion:       cleanup.CurrentSchemaVersion,
		ManifestID:          "manifest-1",
		AccountID:           "account-1",
		RootID:              "root-1",
		Namespace:           "backup/home",
		Mode:                cleanup.ModeQuarantine,
		CreatedAt:           time.Now().UTC().Add(-time.Minute),
		ExpiresAt:           time.Now().UTC().Add(time.Hour),
		Nonce:               "nonce-1",
		Budget:              cleanup.Budget{MaxObjects: 1, MaxBytes: 5},
		SourceInventoryHash: strings.Repeat("a", 64),
		Objects: []cleanup.Object{{
			ID: "object-1", ParentID: "parent-1", Name: "object.bin", ContentHash: strings.Repeat("b", 64), Size: 5,
			Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home", Version: "v1", ETag: "etag-1",
			Class: cleanup.ClassOrphan, OwnershipMarker: "marker-1", RestoreEvidence: "restore-1",
		}},
	}
	path := filepath.Join(dir, "manifest.json")
	data, err := cleanup.CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeCleanupTestRootSet(t *testing.T, dir string) string {
	t.Helper()
	rootSet := cleanup.RootSet{
		SchemaVersion: cleanup.CurrentInventorySchemaVersion,
		Roots: []cleanup.Root{{
			Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home", ExpectedPages: 1,
			Pages: []cleanup.Page{{Number: 1, Cursor: "cursor-1", Status: cleanup.PageComplete, Objects: []cleanup.Object{{
				ID: "object-1", Name: "object.bin", ContentHash: strings.Repeat("a", 64), Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home", Version: "v1", ETag: "etag-1", Size: 5,
			}}},
			}}},
	}
	path := filepath.Join(dir, "all-roots.json")
	data, err := json.Marshal(rootSet)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCleanupInventoryWritesCompleteAggregateAndState(t *testing.T) {
	dir := t.TempDir()
	rootSetPath := writeCleanupTestRootSet(t, dir)
	statePath := filepath.Join(dir, "inventory-state.json")
	aggregatePath := filepath.Join(dir, "inventory-aggregate.json")
	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"cleanup", "inventory", "--account", "account-1", "--all-roots", rootSetPath, "--state", statePath, "--output", aggregatePath, "--format", "json"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("cleanup inventory error = %v", err)
	}
	if !strings.Contains(output.String(), `"status": "COMPLETE"`) {
		t.Fatalf("unexpected inventory output: %s", output.String())
	}
	for _, path := range []string{statePath, aggregatePath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing inventory output %s: %v", path, err)
		}
	}
}

func TestCleanupValidateCommandRendersJSON(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeCleanupTestManifest(t, dir)
	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"cleanup", "validate", "--manifest", manifestPath, "--format", "json"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("cleanup validate error = %v", err)
	}
	if !strings.Contains(output.String(), `"manifest_digest"`) || !strings.Contains(output.String(), `"object_count": 1`) {
		t.Fatalf("unexpected output: %s", output.String())
	}
}

func TestCleanupApplyExecuteFailsClosed(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeCleanupTestManifest(t, dir)
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"cleanup", "apply", "--manifest", manifestPath, "--execute"})
	if _, err := root.ExecuteC(); err == nil || !strings.Contains(err.Error(), "BD-DRIVE-MUTATION-RW") {
		t.Fatalf("expected fail-closed mutation gate, got %v", err)
	}
}

func TestCleanupApplyPreviewWritesJournal(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeCleanupTestManifest(t, dir)
	journalPath := filepath.Join(dir, "cleanup.jsonl")
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"cleanup", "apply", "--manifest", manifestPath, "--journal", journalPath, "--format", "json"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("cleanup preview error = %v", err)
	}
	journal, err := cleanup.OpenFileJournal(journalPath)
	if err != nil {
		t.Fatalf("open preview journal error = %v", err)
	}
	if len(journal.Records) != 1 || journal.Records[0].Action != "preview" {
		t.Fatalf("unexpected preview journal: %+v", journal.Records)
	}
}
func TestCleanupApprovalPrepareCanonicalizeAndActivate(t *testing.T) {
	dir := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approval := cleanup.Approval{
		SchemaVersion:  cleanup.CurrentApprovalSchemaVersion,
		ApprovalID:     "approval-cli-1",
		ManifestDigest: strings.Repeat("a", 64),
		AccountID:      "account-1",
		RootID:         "root-1",
		Mode:           cleanup.ModeQuarantine,
		MaxObjects:     1,
		MaxBytes:       5,
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
		Nonce:          "nonce-cli-1",
		Issuer:         "issuer-cli-1",
		FixtureDigest:  strings.Repeat("b", 64),
	}
	approvalPath := filepath.Join(dir, "approval.json")
	approvalData, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approvalPath, approvalData, 0o600); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "store")

	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"cleanup", "approval", "prepare", "--approval", approvalPath, "--store", storePath})
	if _, err := root.ExecuteC(); err == nil || !strings.Contains(err.Error(), "BD-CLEANUP-DRAFT-RW") {
		t.Fatalf("expected draft capability rejection, got %v", err)
	}

	root = newRootCmd()
	var prepareOutput bytes.Buffer
	root.SetOut(&prepareOutput)
	root.SetErr(&prepareOutput)
	root.SetArgs([]string{"cleanup", "approval", "prepare", "--approval", approvalPath, "--store", storePath, "--capability", "BD-CLEANUP-DRAFT-RW", "--format", "json"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("approval prepare error = %v", err)
	}
	if !strings.Contains(prepareOutput.String(), `"draft_digest"`) {
		t.Fatalf("unexpected prepare output: %s", prepareOutput.String())
	}

	root = newRootCmd()
	var canonicalOutput bytes.Buffer
	root.SetOut(&canonicalOutput)
	root.SetErr(&canonicalOutput)
	root.SetArgs([]string{"cleanup", "approval", "canonicalize", "--approval", approvalPath})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("approval canonicalize error = %v", err)
	}
	if !strings.Contains(canonicalOutput.String(), `"approval_id":"approval-cli-1"`) {
		t.Fatalf("unexpected canonical output: %s", canonicalOutput.String())
	}

	trustRoot, err := cleanup.NewTrustRoot("root-cli-1", approval.Issuer, cleanup.CleanupTrustPurpose, publicKey, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	trustRootPath := filepath.Join(dir, "trust-root.json")
	trustRootData, err := json.Marshal(trustRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trustRootPath, trustRootData, 0o600); err != nil {
		t.Fatal(err)
	}
	signature, err := cleanup.SignApproval(approval, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	signaturePath := filepath.Join(dir, "signature.hex")
	if err := os.WriteFile(signaturePath, []byte(hex.EncodeToString(signature)), 0o600); err != nil {
		t.Fatal(err)
	}

	root = newRootCmd()
	var activateOutput bytes.Buffer
	root.SetOut(&activateOutput)
	root.SetErr(&activateOutput)
	root.SetArgs([]string{"cleanup", "approval", "activate", "--approval", approvalPath, "--signature", signaturePath, "--trust-root", trustRootPath, "--store", storePath, "--capability", "BD-CLEANUP-APPROVAL-RW", "--format", "json"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("approval activate error = %v", err)
	}
	if !strings.Contains(activateOutput.String(), `"state": "approved"`) {
		t.Fatalf("unexpected activate output: %s", activateOutput.String())
	}
	state, err := cleanup.NewApprovalStore(storePath).ReadState(approval.ApprovalID)
	if err != nil {
		t.Fatalf("read activated state: %v", err)
	}
	if state.State != cleanup.ApprovalApproved {
		t.Fatalf("unexpected activated state: %+v", state)
	}
}
