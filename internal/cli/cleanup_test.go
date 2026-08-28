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
			ID: "object-1", ParentID: "root-1", Name: "object.bin", Path: "object.bin", ObjectType: cleanup.ObjectTypeFile,
			ContentHash: strings.Repeat("b", 64), Size: 5, Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home",
			Version: "v1", Generation: "generation-1", ETag: "etag-1", ModifiedAt: time.Now().UTC(), Depth: 1,
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
		SchemaVersion: cleanup.CurrentRootSetSchemaVersion,
		Roots: []cleanup.Root{{
			Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home", ExpectedPages: 1,
			Pages: []cleanup.Page{{Number: 1, ParentID: "root-1", Cursor: "cursor-1", Status: cleanup.PageComplete, Objects: []cleanup.Object{{
				ID: "object-1", ParentID: "root-1", Name: "object.bin", Path: "object.bin", ObjectType: cleanup.ObjectTypeFile,
				ContentHash: strings.Repeat("a", 64), Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home", Version: "v1", Generation: "generation-1", ETag: "etag-1", ModifiedAt: time.Now().UTC(), Depth: 1, Size: 5,
			}}},
			}},
		}}
	rootSet, err := cleanup.FreezeRootSet(rootSet)
	if err != nil {
		t.Fatal(err)
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

func writeCleanupTestApproval(t *testing.T, dir string) (string, cleanup.Approval) {
	t.Helper()
	approval := cleanup.Approval{
		SchemaVersion:  cleanup.CurrentApprovalSchemaVersion,
		ApprovalID:     "approval-test-1",
		ManifestDigest: strings.Repeat("a", 64),
		AccountID:      "account-1",
		RootID:         "root-1",
		Mode:           cleanup.ModeQuarantine,
		MaxObjects:     10,
		MaxBytes:       1000,
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
		Nonce:          "nonce-123",
		Issuer:         "sec-team",
		FixtureDigest:  strings.Repeat("f", 64),
	}
	path := filepath.Join(dir, "approval.json")
	data, err := cleanup.CanonicalApproval(approval)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, approval
}

func TestCleanupApprovalWorkflowCommands(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "approval-store")
	approvalPath, approval := writeCleanupTestApproval(t, dir)

	// 1. Canonicalize command
	root := newRootCmd()
	var canonOut bytes.Buffer
	root.SetOut(&canonOut)
	root.SetErr(&canonOut)
	root.SetArgs([]string{"cleanup", "approval", "canonicalize", "--approval", approvalPath})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("canonicalize error = %v", err)
	}
	if !strings.Contains(canonOut.String(), `"approval_id":"approval-test-1"`) {
		t.Fatalf("unexpected canonicalize output: %s", canonOut.String())
	}

	// 2. Prepare command
	root = newRootCmd()
	var prepOut bytes.Buffer
	root.SetOut(&prepOut)
	root.SetErr(&prepOut)
	root.SetArgs([]string{"cleanup", "approval", "prepare", "--approval", approvalPath, "--store", storePath, "--format", "json"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("prepare error = %v", err)
	}
	if !strings.Contains(prepOut.String(), `"draft_digest"`) {
		t.Fatalf("unexpected prepare output: %s", prepOut.String())
	}

	// 3. Sign offline and Activate command
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := cleanup.SignApproval(approval, priv)
	if err != nil {
		t.Fatal(err)
	}
	trustRoot, err := cleanup.NewTrustRoot("root-1", approval.Issuer, cleanup.CleanupTrustPurpose, pub, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := json.MarshalIndent(trustRoot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(dir, "trust-root.json")
	if err := os.WriteFile(rootPath, rootData, 0o600); err != nil {
		t.Fatal(err)
	}

	root = newRootCmd()
	var actOut bytes.Buffer
	root.SetOut(&actOut)
	root.SetErr(&actOut)
	root.SetArgs([]string{
		"cleanup", "approval", "activate",
		"--approval", approvalPath,
		"--signature", hex.EncodeToString(sig),
		"--root", rootPath,
		"--store", storePath,
		"--format", "json",
	})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("activate error = %v", err)
	}
	if !strings.Contains(actOut.String(), `"state": "approved"`) {
		t.Fatalf("unexpected activate output: %s", actOut.String())
	}
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
	if _, err := root.ExecuteC(); err == nil || !strings.Contains(err.Error(), "protected provider broker") {
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
