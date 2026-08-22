package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScheduleInstallRequiresDryRun(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"schedule", "install", "--platform", "linux"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "dry-run") {
		t.Fatal("schedule install without --dry-run was accepted")
	}
}

func TestScheduleInstallDryRunRendersLinuxDefinition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `schema_version = 2
[[job]]
id = "job-1"
source = "C:/source"
direction = "push"
mode = "copy"
required = true
category_policy_id = "policy"
category_policy_version = 1
category_policy_digest = "sha256:policy"
symlink_policy = "preserve"
schedule = "6h"
[[job.destination]]
backend = "drive"
path = "Backups/job-1"
account_id = "account"
root_id = "root"
credential_ref = "rclone:gdrive"
required = true
min_complete_restore_sets = 2
delete_policy = "none"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", path)
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"schedule", "install", "--dry-run", "--platform", "linux", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("schedule install: %v; stderr=%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "Persistent=true") || !strings.Contains(out.String(), "job-1") {
		t.Fatalf("schedule output = %s, want Linux persistent timer definition", out.String())
	}
}
