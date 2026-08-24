package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigMigrateDryRunJSONRedactsUserPathAndDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	legacy := `rclone_config = "C:/Users/alice/AppData/rclone/rclone.conf"

[[pair]]
local = "C:/Users/alice/.claude"
remote = "gdrive:Backups/claude"
interval = "30s"
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", path)

	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"config", "migrate", "--dry-run", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config migrate --dry-run: %v; stderr=%s", err, errOut.String())
	}
	if strings.Contains(out.String(), "alice") {
		t.Fatalf("migration preview leaked a username: %s", out.String())
	}
	var got struct {
		SchemaVersion int      `json:"schema_version"`
		Blockers      []string `json:"blockers"`
		Jobs          []struct {
			ID        string `json:"id"`
			Direction string `json:"direction"`
			Mode      string `json:"mode"`
			Required  bool   `json:"required"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("preview JSON: %v; output=%s", err, out.String())
	}
	if got.SchemaVersion != 2 || len(got.Jobs) != 1 || got.Jobs[0].Direction != "push" || got.Jobs[0].Mode != "copy" || !got.Jobs[0].Required {
		t.Fatalf("preview = %#v, want normalized v2 copy/push/required job", got)
	}
	if len(got.Blockers) == 0 || !strings.Contains(strings.Join(got.Blockers, "\n"), "category policy") {
		t.Fatalf("blockers = %#v, want category-policy blocker", got.Blockers)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("dry-run migration modified the legacy config")
	}
}

func TestConfigMigrateCreateOnlyRequiresCompleteExplicitBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.toml")
	if err := os.WriteFile(path, []byte(`[[pair]]
local = "C:/source"
remote = "gdrive:Backups/source"
interval = "30s"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", path)
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"config", "migrate", "--create-only", "--output", filepath.Join(t.TempDir(), "migrated.toml")})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "complete") {
		t.Fatalf("create-only without bindings error = %v, want complete-binding rejection", err)
	}
}

func TestConfigMigrateCreateOnlyBlocksLegacyWithoutRuntimeRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.toml")
	outputPath := filepath.Join(t.TempDir(), "migrated.toml")
	if err := os.WriteFile(path, []byte(`[[pair]]
local = "C:/source"
remote = "gdrive:Backups/source"
interval = "30s"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", path)
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"config", "migrate", "--create-only", "--output", outputPath,
		"--account-id", "account-1", "--root-id", "root-1",
		"--role-ref", "profile:home", "--role-digest", "sha256:" + strings.Repeat("d", 64),
		"--policy-ref", "policy:home", "--policy-digest", "sha256:" + strings.Repeat("e", 64),
	})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "category policy registry") {
		t.Fatalf("create-only migration error = %v, want category-policy blocker", err)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("blocked migration created output: stat error=%v", err)
	}
}

func TestConfigMigrateRequiresDryRunBeforeAnyWritePath(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"config", "migrate"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("config migrate without --dry-run succeeded; want explicit dry-run gate")
	}
}
