//go:build darwin

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/state"
)

type nativeAdapter struct {
	now func() time.Time
}

// NewNativeAdapter returns the current user's launchd LaunchAgent adapter.
func NewNativeAdapter() Adapter {
	return &nativeAdapter{now: time.Now}
}

func (a *nativeAdapter) Platform() Platform { return PlatformDarwin }

func (a *nativeAdapter) Install(ctx context.Context, desired Definition, replace bool) error {
	current, err := a.Readback(ctx, desired.JobID)
	if err != nil {
		return fmt.Errorf("read current LaunchAgent: %w", err)
	}
	definition, err := nextDefinition(current, desired, replace)
	if err != nil {
		return err
	}
	path, err := darwinPlistPath(definition.JobID)
	if err != nil {
		return err
	}
	if current.Enabled {
		if output, err := darwinLaunchctl(ctx, "bootout", darwinDomain()+"/"+darwinLabel(definition.JobID)); err != nil {
			return nativeCommandError("unload existing LaunchAgent", output, err)
		}
	}
	if err := writeFileAtomic(path, renderDarwin(definition), 0o600); err != nil {
		return err
	}
	if output, err := darwinLaunchctl(ctx, "disable", darwinDomain()+"/"+darwinLabel(definition.JobID)); err != nil {
		return nativeCommandError("disable staged LaunchAgent", output, err)
	}
	return nil
}

func (a *nativeAdapter) Readback(ctx context.Context, jobID string) (Readback, error) {
	path, err := darwinPlistPath(jobID)
	if err != nil {
		return Readback{}, err
	}
	content, fileErr := os.ReadFile(path)
	fileExists := fileErr == nil
	if fileErr != nil && !errors.Is(fileErr, os.ErrNotExist) {
		return Readback{}, fmt.Errorf("read LaunchAgent plist: %w", fileErr)
	}
	output, printErr := darwinLaunchctl(ctx, "print", darwinDomain()+"/"+darwinLabel(jobID))
	loaded := printErr == nil
	if printErr != nil && !darwinServiceMissing(output) {
		return Readback{}, nativeCommandError("read LaunchAgent state", output, printErr)
	}
	now := a.now().UTC()
	if !fileExists && !loaded {
		return missingReadback(PlatformDarwin, now), nil
	}
	readback := Readback{
		Platform:      PlatformDarwin,
		Installed:     true,
		Enabled:       loaded,
		OverlapState:  state.OverlapNone,
		OverlapHealth: "ok",
		ObservedAt:    now,
		Health:        state.HealthUnknown,
	}
	if !fileExists {
		readback.Health = state.HealthOwnerMismatch
		readback.OverlapState = state.OverlapUnknown
		readback.OverlapHealth = state.HealthOwnerMismatch
		readback.Warnings = []string{"loaded LaunchAgent has no managed plist"}
		return readback, nil
	}
	definition, metadataErr := definitionFromMetadata(darwinMetadata(string(content)))
	if metadataErr != nil {
		readback.Health = state.HealthOwnerMismatch
		readback.OverlapState = state.OverlapUnknown
		readback.OverlapHealth = state.HealthOwnerMismatch
		readback.Warnings = []string{metadataErr.Error()}
		return readback, nil
	}
	readback.Definition = &definition
	readback.Owner = OwnerRecord{Owner: definition.Owner, JobID: definition.JobID, Generation: definition.Generation}
	runCount, _ := strconv.ParseUint(darwinProperty(string(output), "runs"), 10, 64)
	readback.RunCount = runCount
	if loaded && strings.Contains(string(output), "state = running") {
		readback.ActiveInstance = darwinProperty(string(output), "pid")
		readback.OverlapState = state.OverlapSingleActive
	}
	switch {
	case definition.JobID != jobID || definition.Owner != "better-drive":
		readback.Health = state.HealthOwnerMismatch
		readback.OverlapHealth = state.HealthOwnerMismatch
	case !loaded:
		readback.Health = state.HealthDisabled
	case readback.ActiveInstance != "":
		readback.Health = state.HealthHealthy
	case readback.RunCount == 0:
		readback.Health = state.HealthUnknown
	case darwinProperty(string(output), "last exit code") == "0":
		readback.Health = state.HealthHealthy
	default:
		readback.Health = state.HealthNeedsReconciliation
		readback.OverlapHealth = state.HealthNeedsReconciliation
	}
	return readback, nil
}

func (a *nativeAdapter) SetEnabled(ctx context.Context, jobID string, enabled bool) error {
	current, err := a.Readback(ctx, jobID)
	if err != nil {
		return err
	}
	if !current.Installed || current.Owner.Owner != "better-drive" || current.Owner.JobID != jobID {
		return fmt.Errorf("%w: refusing to change LaunchAgent for job %q", ErrOwnerMismatch, jobID)
	}
	target := darwinDomain() + "/" + darwinLabel(jobID)
	if !enabled {
		if current.ActiveInstance != "" {
			return fmt.Errorf("%w: drain active job %q before disabling it", ErrOverlapConflict, jobID)
		}
		if current.Enabled {
			if output, err := darwinLaunchctl(ctx, "bootout", target); err != nil {
				return nativeCommandError("unload LaunchAgent", output, err)
			}
		}
		if output, err := darwinLaunchctl(ctx, "disable", target); err != nil {
			return nativeCommandError("disable LaunchAgent", output, err)
		}
		return nil
	}
	if current.Enabled {
		return nil
	}
	if output, err := darwinLaunchctl(ctx, "enable", target); err != nil {
		return nativeCommandError("enable LaunchAgent", output, err)
	}
	path, err := darwinPlistPath(jobID)
	if err != nil {
		return err
	}
	if output, err := darwinLaunchctl(ctx, "bootstrap", darwinDomain(), path); err != nil {
		return nativeCommandError("load LaunchAgent", output, err)
	}
	return nil
}

func (a *nativeAdapter) Run(ctx context.Context, jobID string) error {
	current, err := a.Readback(ctx, jobID)
	if err != nil {
		return err
	}
	if !current.Installed || current.Owner.Owner != "better-drive" || current.Owner.JobID != jobID {
		return fmt.Errorf("%w: refusing to run LaunchAgent for job %q", ErrOwnerMismatch, jobID)
	}
	if !current.Enabled {
		return fmt.Errorf("scheduler job %q is disabled", jobID)
	}
	if current.ActiveInstance != "" {
		return fmt.Errorf("%w: job %q is already active", ErrOverlapConflict, jobID)
	}
	output, err := darwinLaunchctl(ctx, "kickstart", darwinDomain()+"/"+darwinLabel(jobID))
	if err != nil {
		return nativeCommandError("run LaunchAgent", output, err)
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
		return fmt.Errorf("%w: refusing to remove LaunchAgent for job %q", ErrOwnerMismatch, jobID)
	}
	if current.ActiveInstance != "" {
		return fmt.Errorf("%w: drain active job %q before removal", ErrOverlapConflict, jobID)
	}
	if current.Enabled {
		if output, err := darwinLaunchctl(ctx, "bootout", darwinDomain()+"/"+darwinLabel(jobID)); err != nil {
			return nativeCommandError("unload LaunchAgent", output, err)
		}
	}
	path, err := darwinPlistPath(jobID)
	if err != nil {
		return err
	}
	if err := removeFileIfPresent(path); err != nil {
		return fmt.Errorf("remove LaunchAgent plist: %w", err)
	}
	return nil
}

func darwinPlistPath(jobID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", darwinLabel(jobID)+".plist"), nil
}

func darwinDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func darwinLaunchctl(ctx context.Context, args ...string) ([]byte, error) {
	/* #nosec G204 -- executable path is fixed and arguments use native argv boundaries. */
	cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
	cmd.Env = darwinSchedulerEnvironment()
	return cmd.CombinedOutput()
}

func darwinSchedulerEnvironment() []string {
	keys := []string{"HOME", "LANG", "TMPDIR", "USER"}
	env := []string{"LC_ALL=C"}
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func darwinServiceMissing(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "could not find service") || strings.Contains(text, "service not found") || strings.Contains(text, "no such process")
}

func darwinMetadata(content string) string {
	const prefix = "<key>BetterDriveDefinition</key><string>"
	start := strings.Index(content, prefix)
	if start < 0 {
		return ""
	}
	value := content[start+len(prefix):]
	end := strings.Index(value, "</string>")
	if end < 0 {
		return ""
	}
	return value[:end]
}

func darwinProperty(output, key string) string {
	prefix := key + " = "
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, prefix); ok {
			return value
		}
	}
	return ""
}
