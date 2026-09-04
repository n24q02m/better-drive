//go:build windows

package scheduler

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/n24q02m/better-drive/internal/state"
)

type nativeAdapter struct {
	now func() time.Time
}

// NewNativeAdapter returns the current user's Windows Task Scheduler adapter.
func NewNativeAdapter() Adapter {
	return &nativeAdapter{now: time.Now}
}

func (a *nativeAdapter) Platform() Platform { return PlatformWindows }

func (a *nativeAdapter) Install(ctx context.Context, desired Definition, replace bool) error {
	current, err := a.Readback(ctx, desired.JobID)
	if err != nil {
		return fmt.Errorf("read current scheduled task: %w", err)
	}
	definition, err := nextDefinition(current, desired, replace)
	if err != nil {
		return err
	}

	file, err := os.CreateTemp("", "better-drive-task-*.xml")
	if err != nil {
		return fmt.Errorf("create scheduled task XML: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return fmt.Errorf("protect scheduled task XML: %w", err)
	}
	if _, err := file.Write(windowsXML(renderWindows(definition))); err != nil {
		file.Close()
		return fmt.Errorf("write scheduled task XML: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close scheduled task XML: %w", err)
	}

	output, err := a.command(ctx, "schtasks.exe", "/Create", "/TN", ManagedName(definition.JobID), "/XML", path, "/F")
	if err != nil {
		return nativeCommandError("register scheduled task", output, err)
	}
	return nil
}

func (a *nativeAdapter) Readback(ctx context.Context, jobID string) (Readback, error) {
	const script = `$ErrorActionPreference='Stop'
$taskName = $env:BETTER_DRIVE_SCHEDULER_NAME
$task = Get-ScheduledTask -TaskName $taskName -TaskPath '\' -ErrorAction SilentlyContinue
if ($null -eq $task) { [pscustomobject]@{installed=$false} | ConvertTo-Json -Compress; exit 0 }
$info = Get-ScheduledTaskInfo -TaskName $taskName -TaskPath '\' -ErrorAction Stop
[pscustomobject]@{
  installed=$true
  description=[string]$task.Description
  enabled=([string]$task.State -ne 'Disabled')
  state=[string]$task.State
  last_trigger=$(if ($info.LastRunTime.Year -gt 2000) { $info.LastRunTime.ToUniversalTime().ToString('o') } else { '' })
  next_trigger=$(if ($info.NextRunTime.Year -gt 2000) { $info.NextRunTime.ToUniversalTime().ToString('o') } else { '' })
  last_result=[int64]$info.LastTaskResult
  missed_runs=[int]$info.NumberOfMissedRuns
} | ConvertTo-Json -Compress`

	output, err := a.commandWithEnvironment(ctx, "powershell.exe", []string{"BETTER_DRIVE_SCHEDULER_NAME=" + ManagedName(jobID)}, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	if err != nil {
		return Readback{}, nativeCommandError("read scheduled task", output, err)
	}
	var native struct {
		Installed   bool   `json:"installed"`
		Description string `json:"description"`
		Enabled     bool   `json:"enabled"`
		State       string `json:"state"`
		LastTrigger string `json:"last_trigger"`
		NextTrigger string `json:"next_trigger"`
		LastResult  int64  `json:"last_result"`
		MissedRuns  int    `json:"missed_runs"`
	}
	if err := json.Unmarshal(output, &native); err != nil {
		return Readback{}, fmt.Errorf("decode scheduled task readback: %w", err)
	}
	now := a.now().UTC()
	if !native.Installed {
		return missingReadback(PlatformWindows, now), nil
	}

	readback := Readback{
		Platform:   PlatformWindows,
		Installed:  true,
		Enabled:    native.Enabled,
		ObservedAt: now,
		Health:     state.HealthUnknown,
	}
	definition, metadataErr := definitionFromMetadata(native.Description)
	if metadataErr != nil {
		readback.Health = state.HealthOwnerMismatch
		readback.OverlapState = state.OverlapUnknown
		readback.OverlapHealth = state.HealthOwnerMismatch
		readback.Warnings = []string{metadataErr.Error()}
		return readback, nil
	}
	readback.Definition = &definition
	readback.Owner = OwnerRecord{Owner: definition.Owner, JobID: definition.JobID, Generation: definition.Generation}
	readback.LastTrigger = parseRFC3339Time(native.LastTrigger)
	readback.NextTrigger = parseRFC3339Time(native.NextTrigger)
	readback.OverlapState = state.OverlapNone
	readback.OverlapHealth = "ok"
	if strings.EqualFold(native.State, "Running") {
		readback.ActiveInstance = ManagedName(jobID)
		readback.OverlapState = state.OverlapSingleActive
	}
	switch {
	case definition.JobID != jobID || definition.Owner != "better-drive":
		readback.Health = state.HealthOwnerMismatch
		readback.OverlapHealth = state.HealthOwnerMismatch
	case !native.Enabled:
		readback.Health = state.HealthDisabled
	case native.MissedRuns > 0:
		readback.Health = state.HealthStale
	case readback.LastTrigger.IsZero():
		readback.Health = state.HealthUnknown
	case native.LastResult == 0:
		readback.Health = state.HealthHealthy
	default:
		readback.Health = state.HealthUnknown
		readback.Warnings = append(readback.Warnings, "last_task_result="+strconv.FormatInt(native.LastResult, 10))
	}
	return readback, nil
}

func (a *nativeAdapter) SetEnabled(ctx context.Context, jobID string, enabled bool) error {
	current, err := a.Readback(ctx, jobID)
	if err != nil {
		return err
	}
	if !current.Installed || current.Owner.Owner != "better-drive" || current.Owner.JobID != jobID {
		return fmt.Errorf("%w: refusing to change native task for job %q", ErrOwnerMismatch, jobID)
	}
	if !enabled && current.ActiveInstance != "" {
		return fmt.Errorf("%w: drain active job %q before disabling it", ErrOverlapConflict, jobID)
	}
	action := "/DISABLE"
	if enabled {
		action = "/ENABLE"
	}
	output, err := a.command(ctx, "schtasks.exe", "/Change", "/TN", ManagedName(jobID), action)
	if err != nil {
		return nativeCommandError("change scheduled task state", output, err)
	}
	return nil
}

func (a *nativeAdapter) Run(ctx context.Context, jobID string) error {
	current, err := a.Readback(ctx, jobID)
	if err != nil {
		return err
	}
	if !current.Installed || current.Owner.Owner != "better-drive" || current.Owner.JobID != jobID {
		return fmt.Errorf("%w: refusing to run native task for job %q", ErrOwnerMismatch, jobID)
	}
	if !current.Enabled {
		return fmt.Errorf("scheduler job %q is disabled", jobID)
	}
	if current.ActiveInstance != "" {
		return fmt.Errorf("%w: job %q is already active", ErrOverlapConflict, jobID)
	}
	output, err := a.command(ctx, "schtasks.exe", "/Run", "/TN", ManagedName(jobID))
	if err != nil {
		return nativeCommandError("run scheduled task", output, err)
	}
	return nil
}

func (a *nativeAdapter) Remove(ctx context.Context, jobID string, _ bool) error {
	current, err := a.Readback(ctx, jobID)
	if err != nil {
		return err
	}
	if !current.Installed {
		return nil
	}
	if current.Owner.Owner != "better-drive" || current.Owner.JobID != jobID {
		return fmt.Errorf("%w: refusing to remove native task for job %q", ErrOwnerMismatch, jobID)
	}
	if current.ActiveInstance != "" {
		return fmt.Errorf("%w: drain active job %q before removal", ErrOverlapConflict, jobID)
	}
	output, err := a.command(ctx, "schtasks.exe", "/Delete", "/TN", ManagedName(jobID), "/F")
	if err != nil {
		return nativeCommandError("remove scheduled task", output, err)
	}
	return nil
}

func (a *nativeAdapter) command(ctx context.Context, program string, args ...string) ([]byte, error) {
	return a.commandWithEnvironment(ctx, program, nil, args...)
}

func (a *nativeAdapter) commandWithEnvironment(ctx context.Context, program string, environment []string, args ...string) ([]byte, error) {
	root := strings.TrimSpace(os.Getenv("SystemRoot"))
	if root == "" {
		return nil, errors.New("SystemRoot is required for Windows scheduler commands")
	}
	path := filepath.Join(root, "System32", program)
	if program == "powershell.exe" {
		path = filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", program)
	}
	/* #nosec G204 -- executable is fixed under SystemRoot; arguments use native argv boundaries. */
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(windowsSchedulerEnvironment(root), environment...)
	return cmd.CombinedOutput()
}

func windowsSchedulerEnvironment(root string) []string {
	keys := []string{"APPDATA", "COMSPEC", "LOCALAPPDATA", "PSModulePath", "TEMP", "TMP", "USERDOMAIN", "USERNAME", "USERPROFILE", "WINDIR"}
	env := []string{"SystemRoot=" + root}
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func windowsXML(source []byte) []byte {
	codeUnits := utf16.Encode([]rune(string(source)))
	encoded := make([]byte, 2+len(codeUnits)*2)
	encoded[0] = 0xff
	encoded[1] = 0xfe
	for index, codeUnit := range codeUnits {
		binary.LittleEndian.PutUint16(encoded[2+index*2:], codeUnit)
	}
	return encoded
}
