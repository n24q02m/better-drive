package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/state"
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

func TestOwnerRecordFromStatePreservesOwnerJobID(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	now := time.Now().UTC()
	persisted := state.State{
		SchemaVersion: state.CurrentSchemaVersion,
		EngineVersion: "1.6.0",
		Scheduler: state.SchedulerState{
			Owner: "better-drive", OwnerJobID: "job-1", Enabled: true,
			ObservedAt: now, FreshnessWindow: time.Hour, CatchUpGrace: time.Hour,
			ActiveInstance: "one-shot", OverlapState: state.OverlapNone, OverlapHealth: "ok", Health: state.HealthHealthy,
		},
	}
	if err := state.Save(statePath, persisted); err != nil {
		t.Fatalf("save state: %v", err)
	}
	t.Setenv("BETTER_DRIVE_STATE", statePath)
	got, err := ownerRecordFromState()
	if err != nil {
		t.Fatalf("ownerRecordFromState: %v", err)
	}
	if got.Owner != "better-drive" || got.JobID != "job-1" {
		t.Fatalf("owner record = %#v, want owner and job id readback", got)
	}
}

func TestOwnerRecordFromStateMapsAggregateSyncOwnerToManagedOwner(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	now := time.Now().UTC()
	persisted := state.State{
		SchemaVersion: state.CurrentSchemaVersion,
		EngineVersion: "1.6.0",
		Scheduler: state.SchedulerState{
			Owner: "better-drive", OwnerJobID: "scheduled-sync", Enabled: true,
			ObservedAt: now, FreshnessWindow: time.Hour, CatchUpGrace: time.Hour,
			ActiveInstance: "one-shot", OverlapState: state.OverlapNone, OverlapHealth: "ok", Health: state.HealthHealthy,
		},
	}
	if err := state.Save(statePath, persisted); err != nil {
		t.Fatalf("save state: %v", err)
	}
	t.Setenv("BETTER_DRIVE_STATE", statePath)

	got, err := ownerRecordFromState()
	if err != nil {
		t.Fatalf("ownerRecordFromState: %v", err)
	}
	if got.Owner != "better-drive" || got.JobID != "" {
		t.Fatalf("owner record = %#v, want aggregate managed owner without per-job mismatch", got)
	}
}

func TestScheduleStatusReevaluatesSchedulerFreshness(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	now := time.Now().UTC()
	persisted := state.State{
		SchemaVersion: state.CurrentSchemaVersion,
		EngineVersion: "1.6.0",
		Scheduler: state.SchedulerState{
			Owner: "better-drive", OwnerJobID: "job-1", Enabled: true,
			ObservedAt: now.Add(-2 * time.Hour), FreshnessWindow: time.Minute, CatchUpGrace: time.Hour,
			ActiveInstance: "one-shot", OverlapState: state.OverlapNone, OverlapHealth: "ok", Health: state.HealthHealthy,
		},
	}
	if err := state.Save(statePath, persisted); err != nil {
		t.Fatalf("save state: %v", err)
	}
	t.Setenv("BETTER_DRIVE_STATE", statePath)
	var out bytes.Buffer
	cmd := scheduleStatusCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("schedule status: %v", err)
	}
	var got state.SchedulerState
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode scheduler status: %v", err)
	}
	if got.Health != state.HealthStale {
		t.Fatalf("scheduler health=%q, want %q", got.Health, state.HealthStale)
	}
}
