package cli

import (
	"bytes"
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
