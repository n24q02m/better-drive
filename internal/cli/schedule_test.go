package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/scheduler"
	"github.com/n24q02m/better-drive/internal/state"
)

func writeScheduleTestConfig(t *testing.T, jobIDs ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	var body strings.Builder
	body.WriteString("schema_version = 2\n")
	for index, jobID := range jobIDs {
		fmt.Fprintf(&body, `[[job]]
id = %q
source = %q
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
path = %q
account_id = "account"
root_id = %q
credential_ref = "rclone:gdrive"
required = true
min_complete_restore_sets = 2
delete_policy = "none"
`, jobID, fmt.Sprintf("C:/source-%d", index), "Backups/"+jobID, "root-"+jobID)
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func schedulerTestRoot(adapter scheduler.Adapter, args ...string) (*bytes.Buffer, error) {
	root := newRootCmdWithDependencies(RuntimeDependencies{SchedulerAdapter: adapter})
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(args)
	return &output, root.Execute()
}

func TestScheduleInstallDryRunRendersDistinctExactJobActions(t *testing.T) {
	configPath := writeScheduleTestConfig(t, "job-1", "job-2")
	output, err := schedulerTestRoot(nil, "schedule", "install", "--dry-run", "--platform", "windows", "--config", configPath, "--format", "json")
	if err != nil {
		t.Fatalf("schedule install dry-run: %v", err)
	}
	var previews []schedulePreview
	if err := json.Unmarshal(output.Bytes(), &previews); err != nil {
		t.Fatalf("decode previews: %v", err)
	}
	if len(previews) != 2 || previews[0].Definition == previews[1].Definition {
		t.Fatalf("previews = %#v, want two distinct definitions", previews)
	}
	for index, preview := range previews {
		for _, want := range []string{
			`&quot;--job&quot; &quot;` + preview.JobID + `&quot;`,
			"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
			"<ExecutionTimeLimit>PT21600S</ExecutionTimeLimit>",
			"<Enabled>false</Enabled>",
		} {
			if !strings.Contains(preview.Definition, want) {
				t.Errorf("preview %d missing %q: %s", index, want, preview.Definition)
			}
		}
	}
}

func TestScheduleInstallStagesEveryJobDisabledAndStatusUsesLiveAdapter(t *testing.T) {
	configPath := writeScheduleTestConfig(t, "job-1", "job-2")
	now := time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)
	adapter := scheduler.NewMemoryAdapter(scheduler.PlatformLinux, func() time.Time { return now })
	output, err := schedulerTestRoot(adapter, "schedule", "install", "--platform", "linux", "--config", configPath, "--format", "json")
	if err != nil {
		t.Fatalf("schedule install: %v", err)
	}
	var mutations []scheduleMutationResult
	if err := json.Unmarshal(output.Bytes(), &mutations); err != nil {
		t.Fatalf("decode install output: %v", err)
	}
	if len(mutations) != 2 {
		t.Fatalf("install mutations = %d, want 2", len(mutations))
	}
	for _, mutation := range mutations {
		if !mutation.Readback.Installed || mutation.Readback.Enabled || mutation.Readback.Health != state.HealthDisabled {
			t.Fatalf("install readback = %#v, want installed and disabled", mutation.Readback)
		}
	}

	statusOutput, err := schedulerTestRoot(adapter, "schedule", "status", "--config", configPath, "--format", "json")
	if err != nil {
		t.Fatalf("schedule status: %v", err)
	}
	var readbacks []scheduler.Readback
	if err := json.Unmarshal(statusOutput.Bytes(), &readbacks); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if len(readbacks) != 2 || readbacks[0].Owner.JobID != "job-1" || readbacks[1].Owner.JobID != "job-2" {
		t.Fatalf("live readbacks = %#v, want every configured job", readbacks)
	}
}

func TestScheduleEnableAndExerciseTargetOnlyOneExactJob(t *testing.T) {
	configPath := writeScheduleTestConfig(t, "job-1", "job-2")
	now := time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)
	adapter := scheduler.NewMemoryAdapter(scheduler.PlatformLinux, func() time.Time { return now })
	if _, err := schedulerTestRoot(adapter, "schedule", "install", "--platform", "linux", "--config", configPath); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := schedulerTestRoot(adapter, "schedule", "enable", "--job", "job-1", "--config", configPath); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := schedulerTestRoot(adapter, "schedule", "exercise", "--job", "job-1", "--config", configPath); err != nil {
		t.Fatalf("exercise: %v", err)
	}
	first, _ := adapter.Readback(context.Background(), "job-1")
	second, _ := adapter.Readback(context.Background(), "job-2")
	if !first.Enabled || first.LastTrigger != now {
		t.Fatalf("job-1 readback = %#v, want enabled and exercised", first)
	}
	if second.Enabled || !second.LastTrigger.IsZero() {
		t.Fatalf("job-2 readback = %#v, want untouched disabled job", second)
	}
	if _, err := schedulerTestRoot(adapter, "schedule", "enable", "--job", "missing", "--config", configPath); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing exact job enable error = %v", err)
	}
}

type runCountSchedulerAdapter struct {
	scheduler.Adapter
	runs uint64
}

func (adapter *runCountSchedulerAdapter) Readback(ctx context.Context, jobID string) (scheduler.Readback, error) {
	readback, err := adapter.Adapter.Readback(ctx, jobID)
	readback.LastTrigger = time.Time{}
	readback.RunCount = adapter.runs
	return readback, err
}

func (adapter *runCountSchedulerAdapter) Run(ctx context.Context, jobID string) error {
	readback, err := adapter.Adapter.Readback(ctx, jobID)
	if err != nil {
		return err
	}
	if !readback.Enabled {
		return errors.New("job is disabled")
	}
	adapter.runs++
	return nil
}

func TestScheduleExerciseAcceptsLiveRunCountAdvance(t *testing.T) {
	configPath := writeScheduleTestConfig(t, "job-1")
	base := scheduler.NewMemoryAdapter(scheduler.PlatformDarwin, time.Now)
	adapter := &runCountSchedulerAdapter{Adapter: base}
	if _, err := schedulerTestRoot(adapter, "schedule", "install", "--platform", "darwin", "--config", configPath); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := schedulerTestRoot(adapter, "schedule", "enable", "--job", "job-1", "--config", configPath); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := schedulerTestRoot(adapter, "schedule", "exercise", "--job", "job-1", "--config", configPath); err != nil {
		t.Fatalf("exercise by run-count readback: %v", err)
	}
	if adapter.runs != 1 {
		t.Fatalf("run count = %d, want one exact native invocation", adapter.runs)
	}
}

type failingSchedulerAdapter struct {
	scheduler.Adapter
	installJob     string
	removeJob      string
	applyThenError bool
}

func (adapter failingSchedulerAdapter) Install(ctx context.Context, definition scheduler.Definition, replace bool) error {
	if definition.JobID != adapter.installJob {
		return adapter.Adapter.Install(ctx, definition, replace)
	}
	if adapter.applyThenError {
		if err := adapter.Adapter.Install(ctx, definition, replace); err != nil {
			return err
		}
	}
	return errors.New("injected install failure")
}

func (adapter failingSchedulerAdapter) Remove(ctx context.Context, jobID string, force bool) error {
	if jobID != adapter.removeJob {
		return adapter.Adapter.Remove(ctx, jobID, force)
	}
	if adapter.applyThenError {
		if err := adapter.Adapter.Remove(ctx, jobID, force); err != nil {
			return err
		}
	}
	return errors.New("injected remove failure")
}

func TestScheduleInstallRollsBackEarlierJobsOnDefiniteFailure(t *testing.T) {
	configPath := writeScheduleTestConfig(t, "job-1", "job-2")
	base := scheduler.NewMemoryAdapter(scheduler.PlatformLinux, time.Now)
	adapter := failingSchedulerAdapter{Adapter: base, installJob: "job-2"}
	if _, err := schedulerTestRoot(adapter, "schedule", "install", "--platform", "linux", "--config", configPath); err == nil {
		t.Fatal("schedule install succeeded despite injected failure")
	}
	for _, jobID := range []string{"job-1", "job-2"} {
		readback, err := base.Readback(context.Background(), jobID)
		if err != nil || readback.Installed {
			t.Fatalf("rollback %s readback = %#v, err=%v; want absent", jobID, readback, err)
		}
	}
}

func TestScheduleInstallAcceptsAppliedThenErrorOnlyAfterSemanticReadback(t *testing.T) {
	configPath := writeScheduleTestConfig(t, "job-1", "job-2")
	base := scheduler.NewMemoryAdapter(scheduler.PlatformLinux, time.Now)
	adapter := failingSchedulerAdapter{Adapter: base, installJob: "job-2", applyThenError: true}
	if _, err := schedulerTestRoot(adapter, "schedule", "install", "--platform", "linux", "--config", configPath); err != nil {
		t.Fatalf("applied-then-error install was not reconciled from readback: %v", err)
	}
	for _, jobID := range []string{"job-1", "job-2"} {
		readback, _ := base.Readback(context.Background(), jobID)
		if !readback.Installed || readback.Enabled {
			t.Fatalf("%s readback = %#v, want staged disabled", jobID, readback)
		}
	}
}

func TestScheduleRemoveRollsBackEnabledStateOnDefiniteFailure(t *testing.T) {
	configPath := writeScheduleTestConfig(t, "job-1", "job-2")
	base := scheduler.NewMemoryAdapter(scheduler.PlatformLinux, time.Now)
	if _, err := schedulerTestRoot(base, "schedule", "install", "--platform", "linux", "--config", configPath); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := base.SetEnabled(context.Background(), "job-1", true); err != nil {
		t.Fatal(err)
	}
	adapter := failingSchedulerAdapter{Adapter: base, removeJob: "job-2"}
	if _, err := schedulerTestRoot(adapter, "schedule", "remove", "--platform", "linux", "--config", configPath); err == nil {
		t.Fatal("schedule remove succeeded despite injected failure")
	}
	first, _ := base.Readback(context.Background(), "job-1")
	second, _ := base.Readback(context.Background(), "job-2")
	if !first.Installed || !first.Enabled || !second.Installed || second.Enabled {
		t.Fatalf("rollback states: first=%#v second=%#v", first, second)
	}
}

func TestScheduleRemoveAppliedThenErrorRequiresAbsenceReadback(t *testing.T) {
	configPath := writeScheduleTestConfig(t, "job-1")
	base := scheduler.NewMemoryAdapter(scheduler.PlatformLinux, time.Now)
	if _, err := schedulerTestRoot(base, "schedule", "install", "--platform", "linux", "--config", configPath); err != nil {
		t.Fatalf("install: %v", err)
	}
	adapter := failingSchedulerAdapter{Adapter: base, removeJob: "job-1", applyThenError: true}
	if _, err := schedulerTestRoot(adapter, "schedule", "remove", "--platform", "linux", "--config", configPath); err != nil {
		t.Fatalf("applied-then-error remove was not reconciled from absence: %v", err)
	}
	readback, _ := base.Readback(context.Background(), "job-1")
	if readback.Installed {
		t.Fatalf("removed job readback = %#v", readback)
	}
}
