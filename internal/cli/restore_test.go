package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestRestoreApplyRemainsOwnerGated(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"restore", "apply"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "data-owner") {
		t.Fatal("restore apply without owner gate was accepted")
	}
}
