package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func validV2Config(t *testing.T) *Config {
	t.Helper()
	runtimeDir := t.TempDir()
	sourceDir := t.TempDir()
	return &Config{
		SchemaVersion: CurrentSchemaVersion,
		RcloneRuntime: RcloneRuntime{
			Executable: filepath.Join(runtimeDir, "rclone"), ExecutableFileID: "exe-id", ExecutableDigest: "sha256:exe",
			Version: "1.67.0", Provenance: "release", Signature: "sig", Owner: "role", ACL: "owner-only",
			Config: filepath.Join(runtimeDir, "rclone.conf"), ConfigFileID: "cfg-id", ConfigDigest: "sha256:cfg",
			AllowedRemotes: []string{"gdrive"}, AllowedBackends: []string{"drive"},
		},
		Jobs: []Job{{
			ID: "home-claude", Source: sourceDir, Direction: "push", Mode: "copy", Required: true,
			CategoryPolicyID: "claude-state", CategoryPolicyVersion: 1, CategoryPolicyDigest: "sha256:policy",
			SymlinkPolicy: "preserve", Schedule: "30s", Interval: 30_000_000_000,
			Destinations: []Destination{{Backend: "drive", Path: "Backups/home/Claude", AccountID: "account", RootID: "root", CredentialRef: "rclone:gdrive", Required: true, Retention: "30d", MinCompleteRestoreSets: 2, DeletePolicy: "none"}},
		}},
	}
}

func TestValidateAcceptsCompleteV2Config(t *testing.T) {
	if err := validV2Config(t).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsDuplicateJobIDs(t *testing.T) {
	cfg := validV2Config(t)
	cfg.Jobs = append(cfg.Jobs, cfg.Jobs[0])
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Fatalf("Validate error = %v, want duplicate-id rejection", err)
	}
}

func TestValidateRejectsUnsupportedDirectionModeCombination(t *testing.T) {
	cfg := validV2Config(t)
	cfg.Jobs[0].Direction = "bidirectional"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "bidirectional") {
		t.Fatalf("Validate error = %v, want direction/mode rejection", err)
	}
}

func TestValidateForExecutionRequiresPinnedRuntime(t *testing.T) {
	cfg := validV2Config(t)
	cfg.RcloneRuntime = RcloneRuntime{}
	if err := cfg.ValidateForExecution(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("ValidateForExecution error = %v, want pinned runtime rejection", err)
	}
}

func TestLoadRcloneConfigOnlyReadsV2RuntimeConfig(t *testing.T) {
	p := writeTemp(t, `schema_version = 2
[rclone_runtime]
config = "C:/tools/rclone.conf"
[[job]]
id = "job"
source = "C:/source"
direction = "push"
mode = "copy"
required = true
category_policy_id = "policy"
category_policy_version = 1
category_policy_digest = "sha256:policy"
symlink_policy = "preserve"
schedule = "30s"
[[job.destination]]
backend = "drive"
path = "Backups/job"
required = true
min_complete_restore_sets = 2
delete_policy = "none"
`)
	got, err := LoadRcloneConfigOnly(p)
	if err != nil {
		t.Fatalf("LoadRcloneConfigOnly: %v", err)
	}
	if got != "C:/tools/rclone.conf" {
		t.Fatalf("config = %q, want explicit v2 runtime config", got)
	}
}

func TestLoadRcloneConfigOnlyAllowsMissingFile(t *testing.T) {
	got, err := LoadRcloneConfigOnly(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("LoadRcloneConfigOnly(missing): %v", err)
	}
	if got != "" {
		t.Fatalf("config = %q, want empty for mount-only missing file", got)
	}
}

func TestLoadRcloneConfigOnlyRejectsMalformedExistingFile(t *testing.T) {
	p := writeTemp(t, `rclone_config = "unterminated`)
	if _, err := LoadRcloneConfigOnly(p); err == nil {
		t.Fatal("want malformed existing config to fail")
	}
}
