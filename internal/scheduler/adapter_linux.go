//go:build linux

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/state"
)

type nativeAdapter struct {
	now func() time.Time
}

// NewNativeAdapter returns the current user's systemd timer adapter.
func NewNativeAdapter() Adapter {
	return &nativeAdapter{now: time.Now}
}

func (a *nativeAdapter) Platform() Platform { return PlatformLinux }

func (a *nativeAdapter) Install(ctx context.Context, desired Definition, replace bool) error {
	current, err := a.Readback(ctx, desired.JobID)
	if err != nil {
		return fmt.Errorf("read current systemd timer: %w", err)
	}
	definition, err := nextDefinition(current, desired, replace)
	if err != nil {
		return err
	}
	servicePath, timerPath, err := linuxUnitPaths(definition.JobID)
	if err != nil {
		return err
	}
	service, timer := renderLinuxUnits(definition)
	if err := writeFileAtomic(servicePath, service, 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(timerPath, timer, 0o600); err != nil {
		return err
	}
	if output, err := linuxSystemctl(ctx, "daemon-reload"); err != nil {
		return nativeCommandError("reload systemd user manager", output, err)
	}
	return nil
}

func (a *nativeAdapter) Readback(ctx context.Context, jobID string) (Readback, error) {
	servicePath, timerPath, err := linuxUnitPaths(jobID)
	if err != nil {
		return Readback{}, err
	}
	service, serviceErr := os.ReadFile(servicePath)
	timer, timerErr := os.ReadFile(timerPath)
	serviceExists := serviceErr == nil
	timerExists := timerErr == nil
	if serviceErr != nil && !errors.Is(serviceErr, os.ErrNotExist) {
		return Readback{}, fmt.Errorf("read systemd service: %w", serviceErr)
	}
	if timerErr != nil && !errors.Is(timerErr, os.ErrNotExist) {
		return Readback{}, fmt.Errorf("read systemd timer: %w", timerErr)
	}

	name := ManagedName(jobID)
	output, showErr := linuxSystemctl(ctx, "show", name+".timer", "--no-pager", "--property=LoadState", "--property=UnitFileState", "--property=ActiveState", "--property=LastTriggerUSec", "--property=NextElapseUSecRealtime")
	if showErr != nil && !linuxUnitMissing(output) {
		return Readback{}, nativeCommandError("read systemd timer state", output, showErr)
	}
	properties := parseProperties(string(output))
	if showErr != nil {
		properties["LoadState"] = "not-found"
	}
	loaded := properties["LoadState"] != "" && properties["LoadState"] != "not-found"
	now := a.now().UTC()
	if !serviceExists && !timerExists && !loaded {
		return missingReadback(PlatformLinux, now), nil
	}

	readback := Readback{
		Platform:      PlatformLinux,
		Installed:     true,
		Enabled:       strings.HasPrefix(properties["UnitFileState"], "enabled"),
		OverlapState:  state.OverlapNone,
		OverlapHealth: "ok",
		ObservedAt:    now,
		Health:        state.HealthUnknown,
		LastTrigger:   parseSystemdTime(properties["LastTriggerUSec"]),
		NextTrigger:   parseSystemdTime(properties["NextElapseUSecRealtime"]),
	}
	metadata := ""
	if serviceExists {
		metadata = metadataLine(string(service))
	}
	if timerExists {
		timerMetadata := metadataLine(string(timer))
		if metadata == "" {
			metadata = timerMetadata
		} else if timerMetadata != metadata {
			readback.Health = state.HealthOwnerMismatch
			readback.OverlapState = state.OverlapUnknown
			readback.OverlapHealth = state.HealthOwnerMismatch
			readback.Warnings = []string{"systemd definition metadata is inconsistent"}
			return readback, nil
		}
	}
	definition, metadataErr := definitionFromMetadata(metadata)
	if metadataErr != nil {
		readback.Health = state.HealthOwnerMismatch
		readback.OverlapState = state.OverlapUnknown
		readback.OverlapHealth = state.HealthOwnerMismatch
		readback.Warnings = []string{"systemd definition metadata is missing or invalid"}
		return readback, nil
	}
	readback.Definition = &definition
	readback.Owner = OwnerRecord{Owner: definition.Owner, JobID: definition.JobID, Generation: definition.Generation}
	if !serviceExists || !timerExists || !loaded {
		readback.Health = state.HealthNeedsReconciliation
		readback.OverlapHealth = state.HealthNeedsReconciliation
		readback.Warnings = []string{"systemd service/timer files and loaded state are incomplete"}
		return readback, nil
	}
	serviceOutput, serviceShowErr := linuxSystemctl(ctx, "show", name+".service", "--no-pager", "--property=ActiveState", "--property=MainPID", "--property=Result", "--property=ExecMainStatus", "--property=ExecMainStartTimestamp")
	if serviceShowErr != nil {
		return Readback{}, nativeCommandError("read systemd service state", serviceOutput, serviceShowErr)
	}
	serviceProperties := parseProperties(string(serviceOutput))
	serviceTrigger := parseSystemdTime(serviceProperties["ExecMainStartTimestamp"])
	if serviceTrigger.After(readback.LastTrigger) {
		readback.LastTrigger = serviceTrigger
	}
	if serviceProperties["ActiveState"] == "active" || serviceProperties["ActiveState"] == "activating" {
		readback.ActiveInstance = serviceProperties["MainPID"]
		readback.OverlapState = state.OverlapSingleActive
	}
	switch {
	case definition.JobID != jobID || definition.Owner != "better-drive":
		readback.Health = state.HealthOwnerMismatch
		readback.OverlapHealth = state.HealthOwnerMismatch
	case !readback.Enabled:
		readback.Health = state.HealthDisabled
	case properties["ActiveState"] == "failed" || serviceProperties["ActiveState"] == "failed" ||
		(serviceProperties["Result"] != "" && serviceProperties["Result"] != "success") ||
		(serviceProperties["ExecMainStatus"] != "" && serviceProperties["ExecMainStatus"] != "0"):
		readback.Health = state.HealthNeedsReconciliation
		readback.OverlapHealth = state.HealthNeedsReconciliation
	case readback.LastTrigger.IsZero():
		readback.Health = state.HealthUnknown
	default:
		readback.Health = state.HealthHealthy
	}
	return readback, nil
}

func (a *nativeAdapter) SetEnabled(ctx context.Context, jobID string, enabled bool) error {
	current, err := a.Readback(ctx, jobID)
	if err != nil {
		return err
	}
	if !current.Installed || current.Owner.Owner != "better-drive" || current.Owner.JobID != jobID {
		return fmt.Errorf("%w: refusing to change native timer for job %q", ErrOwnerMismatch, jobID)
	}
	if !enabled && current.ActiveInstance != "" {
		return fmt.Errorf("%w: drain active job %q before disabling it", ErrOverlapConflict, jobID)
	}
	action := "disable"
	if enabled {
		action = "enable"
	}
	output, err := linuxSystemctl(ctx, action, "--now", ManagedName(jobID)+".timer")
	if err != nil {
		return nativeCommandError("change systemd timer state", output, err)
	}
	return nil
}

func (a *nativeAdapter) Run(ctx context.Context, jobID string) error {
	current, err := a.Readback(ctx, jobID)
	if err != nil {
		return err
	}
	if !current.Installed || current.Owner.Owner != "better-drive" || current.Owner.JobID != jobID {
		return fmt.Errorf("%w: refusing to run native timer for job %q", ErrOwnerMismatch, jobID)
	}
	if !current.Enabled {
		return fmt.Errorf("scheduler job %q is disabled", jobID)
	}
	if current.ActiveInstance != "" {
		return fmt.Errorf("%w: job %q is already active", ErrOverlapConflict, jobID)
	}
	output, err := linuxSystemctl(ctx, "start", ManagedName(jobID)+".service")
	if err != nil {
		return nativeCommandError("run systemd service", output, err)
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
		return fmt.Errorf("%w: refusing to remove native timer for job %q", ErrOwnerMismatch, jobID)
	}
	if current.ActiveInstance != "" {
		return fmt.Errorf("%w: drain active job %q before removal", ErrOverlapConflict, jobID)
	}
	name := ManagedName(jobID)
	if current.Enabled {
		if output, err := linuxSystemctl(ctx, "disable", "--now", name+".timer"); err != nil {
			return nativeCommandError("disable systemd timer", output, err)
		}
	}
	servicePath, timerPath, err := linuxUnitPaths(jobID)
	if err != nil {
		return err
	}
	if err := removeFileIfPresent(timerPath); err != nil {
		return fmt.Errorf("remove systemd timer: %w", err)
	}
	if err := removeFileIfPresent(servicePath); err != nil {
		return fmt.Errorf("remove systemd service: %w", err)
	}
	if output, err := linuxSystemctl(ctx, "daemon-reload"); err != nil {
		return nativeCommandError("reload systemd user manager", output, err)
	}
	return nil
}

func linuxUnitPaths(jobID string) (string, string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve user config directory: %w", err)
	}
	name := ManagedName(jobID)
	dir := filepath.Join(configDir, "systemd", "user")
	return filepath.Join(dir, name+".service"), filepath.Join(dir, name+".timer"), nil
}

func renderLinuxUnits(definition Definition) ([]byte, []byte) {
	name := ManagedName(definition.JobID)
	metadata := definitionMetadata(definition)
	service := []byte(fmt.Sprintf(`# X-BetterDrive-Definition=%s
[Unit]
Description=better-drive managed job %s

[Service]
Type=oneshot
ExecStart=%s
TimeoutStartSec=%d
`, metadata, name, commandLine(definition), definition.ExecutionLimitSeconds))
	timer := []byte(fmt.Sprintf(`# X-BetterDrive-Definition=%s
[Unit]
Description=better-drive schedule %s

[Timer]
OnBootSec=1min
OnUnitActiveSec=%s
Persistent=%t
Unit=%s.service

[Install]
WantedBy=timers.target
`, metadata, name, formatInterval(definition.IntervalSeconds), definition.CatchUp, name))
	return service, timer
}

func metadataLine(content string) string {
	const prefix = "# X-BetterDrive-Definition="
	for line := range strings.SplitSeq(content, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			return value
		}
	}
	return ""
}

func parseProperties(output string) map[string]string {
	properties := make(map[string]string)
	for line := range strings.SplitSeq(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			properties[key] = value
		}
	}
	return properties
}
func linuxUnitMissing(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "not-found") ||
		strings.Contains(text, "could not be found") ||
		strings.Contains(text, "not loaded")
}

func parseSystemdTime(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05 -07"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func linuxSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	path := ""
	for _, candidate := range []string{"/usr/bin/systemctl", "/bin/systemctl"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			path = candidate
			break
		}
	}
	if path == "" {
		return nil, errors.New("systemctl executable was not found in /usr/bin or /bin")
	}
	/* #nosec G204 -- executable is selected from fixed system paths; arguments remain separate argv entries. */
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = linuxSchedulerEnvironment()
	return cmd.CombinedOutput()
}

func linuxSchedulerEnvironment() []string {
	keys := []string{"DBUS_SESSION_BUS_ADDRESS", "HOME", "LANG", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"}
	env := []string{"LC_ALL=C"}
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}
