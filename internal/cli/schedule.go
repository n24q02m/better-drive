package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/n24q02m/better-drive/internal/scheduler"
	"github.com/spf13/cobra"
)

type schedulePreview struct {
	JobID      string `json:"job_id"`
	Platform   string `json:"platform"`
	Definition string `json:"definition"`
}

type scheduleRemovalPreview struct {
	JobID      string `json:"job_id"`
	Platform   string `json:"platform"`
	NativeName string `json:"native_name"`
}

type scheduleMutationResult struct {
	JobID    string             `json:"job_id"`
	Action   string             `json:"action"`
	Readback scheduler.Readback `json:"readback"`
}

func scheduleCmd() *cobra.Command {
	return scheduleCmdWithDependencies(RuntimeDependencies{})
}

func scheduleCmdWithDependencies(deps RuntimeDependencies) *cobra.Command {
	command := &cobra.Command{
		Use:     "schedule",
		Short:   "Install, inspect, and remove managed per-job schedulers",
		Long:    "Manage one native scheduler definition per configured job with exact owner, job, generation, and semantic readback checks.",
		Example: "  better-drive schedule install --dry-run --format json\n  better-drive schedule status --format json",
	}
	command.AddCommand(scheduleInstallCmd(deps), scheduleStatusCmd(deps), scheduleSetEnabledCmd(deps, true), scheduleSetEnabledCmd(deps, false), scheduleExerciseCmd(deps), scheduleRemoveCmd(deps))
	return command
}

func scheduleInstallCmd(deps RuntimeDependencies) *cobra.Command {
	var platform string
	var format string
	var configPath string
	var dryRun bool
	var replace bool
	command := &cobra.Command{
		Use:     "install",
		Short:   "Install managed per-job scheduler definitions",
		Long:    "Preflight every configured job, install transactionally in the disabled state, verify native semantic readback, and restore prior definitions if any job fails. --dry-run only renders definitions. Enabling and exercising require separate exact-job commands.",
		Example: "  better-drive schedule install --dry-run --platform linux --format json\n  better-drive schedule install --format json\n  better-drive schedule enable --job exact-job-id",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			resolved, err := normalizePlatform(platform)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "use --platform windows, linux, or darwin")
			}
			activeConfigPath := selectedConfigPath(configPath)
			cfg, err := loadConfigAt(activeConfigPath)
			if err != nil {
				return err
			}
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			definitions, err := scheduleDefinitions(cfg, executable, activeConfigPath)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			if dryRun {
				return renderSchedulePreviews(cmd, format, resolved, definitions)
			}

			adapter, err := liveSchedulerAdapter(deps, resolved)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			ctx := commandContext(cmd)
			before, err := preflightSchedulerInstall(ctx, adapter, definitions, replace)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "inspect `better-drive schedule status --format json`; use --replace only for an exact recoverable managed owner")
			}
			results, err := installDefinitions(ctx, adapter, definitions, before, replace)
			if err != nil {
				return err
			}
			return renderScheduleMutationResults(cmd, format, results)
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&platform, "platform", runtime.GOOS, "target scheduler platform: windows|linux|darwin (live operations must match this host)")
	command.Flags().StringVar(&configPath, "config", "", "read jobs from this absolute config path")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "render definitions without changing the native scheduler")
	command.Flags().BoolVar(&replace, "replace", false, "replace a recoverable managed definition after exact owner readback")
	return command
}

func scheduleStatusCmd(deps RuntimeDependencies) *cobra.Command {
	var format string
	var configPath string
	command := &cobra.Command{
		Use:     "status",
		Short:   "Read live scheduler state for every configured job",
		Long:    "Query the current host scheduler for every configured job; transfer state.json is not scheduler authority.",
		Example: "  better-drive schedule status --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			cfg, err := loadConfigAt(selectedConfigPath(configPath))
			if err != nil {
				return err
			}
			adapter := deps.SchedulerAdapter
			if adapter == nil {
				adapter = scheduler.NewNativeAdapter()
			}
			ctx := commandContext(cmd)
			readbacks := make([]scheduler.Readback, 0, len(cfg.Jobs))
			for _, job := range cfg.Jobs {
				readback, err := adapter.Readback(ctx, job.ID)
				if err != nil {
					return fmt.Errorf("read scheduler job %q: %w", job.ID, err)
				}
				readbacks = append(readbacks, readback)
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), readbacks)
			}
			for index, readback := range readbacks {
				fmt.Fprintf(cmd.OutOrStdout(), "job %s [%s]: installed=%t enabled=%t health=%s overlap=%s\n", cfg.Jobs[index].ID, readback.Platform, readback.Installed, readback.Enabled, readback.Health, readback.OverlapState)
			}
			return nil
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&configPath, "config", "", "read jobs from this absolute config path")
	return command
}

func scheduleSetEnabledCmd(deps RuntimeDependencies, enabled bool) *cobra.Command {
	name := "disable"
	action := "disabled"
	if enabled {
		name = "enable"
		action = "enabled"
	}
	var format string
	var configPath string
	var jobID string
	command := &cobra.Command{
		Use:   name + " --job <exact-job-id>",
		Short: name + " one exact managed scheduler job",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			cfg, err := loadConfigAt(selectedConfigPath(configPath))
			if err != nil {
				return err
			}
			if err := requireConfiguredJob(cfg, jobID); err != nil {
				return exitcode.ConfigError(err)
			}
			adapter := deps.SchedulerAdapter
			if adapter == nil {
				adapter = scheduler.NewNativeAdapter()
			}
			ctx := commandContext(cmd)
			before, err := adapter.Readback(ctx, jobID)
			if err != nil {
				return err
			}
			if err := validateOwnedReadback(before, jobID); err != nil {
				return exitcode.ConfigError(err)
			}
			if !enabled && before.ActiveInstance != "" {
				return exitcode.ConfigError(fmt.Errorf("%w: drain active job %q before disabling it", scheduler.ErrOverlapConflict, jobID))
			}
			mutationErr := adapter.SetEnabled(ctx, jobID, enabled)
			after, readbackErr := adapter.Readback(ctx, jobID)
			if readbackErr == nil && after.Installed && after.Enabled == enabled && after.Owner == before.Owner {
				return renderScheduleMutationResults(cmd, format, []scheduleMutationResult{{JobID: jobID, Action: action, Readback: after}})
			}
			cause := errors.Join(mutationErr, readbackErr)
			if cause == nil {
				cause = fmt.Errorf("scheduler %s readback mismatch for job %q", name, jobID)
			}
			rollbackErr := adapter.SetEnabled(ctx, jobID, before.Enabled)
			if rollbackErr == nil {
				rollback, err := adapter.Readback(ctx, jobID)
				if err != nil || rollback.Enabled != before.Enabled || rollback.Owner != before.Owner {
					rollbackErr = errors.Join(err, fmt.Errorf("scheduler state rollback readback mismatch for job %q", jobID))
				}
			}
			return errors.Join(cause, rollbackErr)
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&configPath, "config", "", "read jobs from this absolute config path")
	command.Flags().StringVar(&jobID, "job", "", "exact configured job ID")
	_ = command.MarkFlagRequired("job")
	return command
}

func scheduleExerciseCmd(deps RuntimeDependencies) *cobra.Command {
	var format string
	var configPath string
	var jobID string
	command := &cobra.Command{
		Use:   "exercise --job <exact-job-id>",
		Short: "Run one exact enabled managed scheduler job",
		Long:  "Trigger one exact enabled job and require live scheduler evidence that the run started or completed. This command never enables another job.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			cfg, err := loadConfigAt(selectedConfigPath(configPath))
			if err != nil {
				return err
			}
			if err := requireConfiguredJob(cfg, jobID); err != nil {
				return exitcode.ConfigError(err)
			}
			adapter := deps.SchedulerAdapter
			if adapter == nil {
				adapter = scheduler.NewNativeAdapter()
			}
			ctx := commandContext(cmd)
			before, err := adapter.Readback(ctx, jobID)
			if err != nil {
				return err
			}
			if err := validateOwnedReadback(before, jobID); err != nil {
				return exitcode.ConfigError(err)
			}
			if !before.Enabled {
				return exitcode.ConfigError(fmt.Errorf("scheduler job %q is disabled; enable that exact job first", jobID))
			}
			if before.ActiveInstance != "" {
				return exitcode.ConfigError(fmt.Errorf("%w: job %q is already active", scheduler.ErrOverlapConflict, jobID))
			}
			runErr := adapter.Run(ctx, jobID)
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				readback, readbackErr := adapter.Readback(ctx, jobID)
				if readbackErr == nil && readback.Owner == before.Owner &&
					(readback.ActiveInstance != "" || readback.LastTrigger.After(before.LastTrigger) || readback.RunCount > before.RunCount) {
					return renderScheduleMutationResults(cmd, format, []scheduleMutationResult{{JobID: jobID, Action: "exercised", Readback: readback}})
				}
				if runErr != nil {
					return errors.Join(runErr, readbackErr)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
					return fmt.Errorf("scheduler exercise for job %q had no live trigger readback within 10s", jobID)
				case <-ticker.C:
				}
			}
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&configPath, "config", "", "read jobs from this absolute config path")
	command.Flags().StringVar(&jobID, "job", "", "exact configured job ID")
	_ = command.MarkFlagRequired("job")
	return command
}

func scheduleRemoveCmd(deps RuntimeDependencies) *cobra.Command {
	var format string
	var configPath string
	var platform string
	var dryRun bool
	command := &cobra.Command{
		Use:     "remove",
		Short:   "Remove managed per-job scheduler definitions",
		Long:    "Preflight exact ownership for every configured job, remove transactionally, verify bounded absence, and restore prior definitions if any job fails. Active or foreign definitions are always rejected.",
		Example: "  better-drive schedule remove --dry-run --format json\n  better-drive schedule remove --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			resolved, err := normalizePlatform(platform)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "use --platform windows, linux, or darwin")
			}
			cfg, err := loadConfigAt(selectedConfigPath(configPath))
			if err != nil {
				return err
			}
			if dryRun {
				previews := make([]scheduleRemovalPreview, 0, len(cfg.Jobs))
				for _, job := range cfg.Jobs {
					previews = append(previews, scheduleRemovalPreview{JobID: job.ID, Platform: string(resolved), NativeName: scheduler.ManagedName(job.ID)})
				}
				if format == output.FormatJSON {
					return output.RenderJSON(cmd.OutOrStdout(), previews)
				}
				for _, preview := range previews {
					fmt.Fprintf(cmd.OutOrStdout(), "job %s [%s]: remove %s\n", preview.JobID, preview.Platform, preview.NativeName)
				}
				return nil
			}
			adapter, err := liveSchedulerAdapter(deps, resolved)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			ctx := commandContext(cmd)
			before, err := preflightSchedulerRemoval(ctx, adapter, cfg)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "inspect `better-drive schedule status --format json`; drain active jobs before removal")
			}
			results, err := removeDefinitions(ctx, adapter, cfg, before)
			if err != nil {
				return err
			}
			return renderScheduleMutationResults(cmd, format, results)
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&configPath, "config", "", "read jobs from this config path")
	command.Flags().StringVar(&platform, "platform", runtime.GOOS, "target scheduler platform: windows|linux|darwin (live operations must match this host)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "preview exact native scheduler targets without removing them")
	return command
}

func requireConfiguredJob(cfg *config.Config, jobID string) error {
	if strings.TrimSpace(jobID) == "" {
		return errors.New("exact --job is required")
	}
	for _, job := range cfg.Jobs {
		if job.ID == jobID {
			return nil
		}
	}
	return fmt.Errorf("configured job %q was not found", jobID)
}

func validateOwnedReadback(readback scheduler.Readback, jobID string) error {
	if !readback.Installed || readback.Definition == nil ||
		readback.Owner.Owner != "better-drive" || readback.Owner.JobID != jobID {
		return fmt.Errorf("%w: job %q is not an exact better-drive-owned definition", scheduler.ErrOwnerMismatch, jobID)
	}
	return nil
}

func scheduleDefinitions(cfg *config.Config, executable, configPath string) ([]scheduler.Definition, error) {
	definitions := make([]scheduler.Definition, 0, len(cfg.Jobs))
	for _, job := range cfg.Jobs {
		seconds := int(job.Interval / time.Second)
		definition := scheduler.Definition{
			JobID:                 job.ID,
			Executable:            executable,
			Config:                configPath,
			IntervalSeconds:       seconds,
			CatchUp:               true,
			ExecutionLimitSeconds: max(3600, seconds),
			Owner:                 "better-drive",
		}
		if err := definition.Validate(); err != nil {
			return nil, fmt.Errorf("job %q: %w", job.ID, err)
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func renderSchedulePreviews(cmd *cobra.Command, format string, platform scheduler.Platform, definitions []scheduler.Definition) error {
	previews := make([]schedulePreview, 0, len(definitions))
	for _, definition := range definitions {
		definitionBytes, err := scheduler.Render(platform, definition)
		if err != nil {
			return err
		}
		previews = append(previews, schedulePreview{JobID: definition.JobID, Platform: string(platform), Definition: string(definitionBytes)})
	}
	if format == output.FormatJSON {
		return output.RenderJSON(cmd.OutOrStdout(), previews)
	}
	for _, preview := range previews {
		fmt.Fprintf(cmd.OutOrStdout(), "job %s [%s]\n%s\n", preview.JobID, preview.Platform, preview.Definition)
	}
	return nil
}

func liveSchedulerAdapter(deps RuntimeDependencies, platform scheduler.Platform) (scheduler.Adapter, error) {
	adapter := deps.SchedulerAdapter
	if adapter == nil {
		if string(platform) != runtime.GOOS {
			return nil, fmt.Errorf("live scheduler platform %q does not match host %q", platform, runtime.GOOS)
		}
		adapter = scheduler.NewNativeAdapter()
	}
	if adapter.Platform() != platform {
		return nil, fmt.Errorf("scheduler adapter platform %q does not match requested %q", adapter.Platform(), platform)
	}
	return adapter, nil
}

func preflightSchedulerInstall(ctx context.Context, adapter scheduler.Adapter, definitions []scheduler.Definition, replace bool) ([]scheduler.Readback, error) {
	before := make([]scheduler.Readback, len(definitions))
	for index, definition := range definitions {
		readback, err := adapter.Readback(ctx, definition.JobID)
		if err != nil {
			return nil, fmt.Errorf("preflight job %q: %w", definition.JobID, err)
		}
		before[index] = readback
		if !readback.Installed {
			continue
		}
		if readback.Definition == nil || readback.Owner.Owner == "" || readback.Owner.JobID == "" {
			return nil, fmt.Errorf("job %q has no recoverable managed definition", definition.JobID)
		}
		if err := scheduler.ValidateOwner(readback.Owner, definition, replace); err != nil {
			return nil, err
		}
		if readback.ActiveInstance != "" {
			return nil, fmt.Errorf("%w: job %q has active instance %q", scheduler.ErrOverlapConflict, definition.JobID, readback.ActiveInstance)
		}
	}
	return before, nil
}

func installDefinitions(ctx context.Context, adapter scheduler.Adapter, definitions []scheduler.Definition, before []scheduler.Readback, replace bool) ([]scheduleMutationResult, error) {
	results := make([]scheduleMutationResult, 0, len(definitions))
	for index, definition := range definitions {
		mutationErr := adapter.Install(ctx, definition, replace)
		readback, readbackErr := adapter.Readback(ctx, definition.JobID)
		if readbackErr == nil && !readback.Enabled && scheduler.MatchesDefinition(readback, definition) {
			results = append(results, scheduleMutationResult{JobID: definition.JobID, Action: "installed", Readback: readback})
			continue
		}
		cause := errors.Join(mutationErr, readbackErr)
		if cause == nil {
			cause = fmt.Errorf("scheduler semantic readback mismatch for job %q", definition.JobID)
		}
		rollbackErr := rollbackScheduler(ctx, adapter, definitions[:index+1], before[:index+1])
		return nil, errors.Join(cause, rollbackErr)
	}
	return results, nil
}

func preflightSchedulerRemoval(ctx context.Context, adapter scheduler.Adapter, cfg *config.Config) ([]scheduler.Readback, error) {
	before := make([]scheduler.Readback, len(cfg.Jobs))
	for index, job := range cfg.Jobs {
		readback, err := adapter.Readback(ctx, job.ID)
		if err != nil {
			return nil, fmt.Errorf("preflight job %q: %w", job.ID, err)
		}
		before[index] = readback
		if !readback.Installed {
			continue
		}
		if err := validateOwnedReadback(readback, job.ID); err != nil {
			return nil, err
		}
		if readback.ActiveInstance != "" {
			return nil, fmt.Errorf("%w: job %q has active instance %q", scheduler.ErrOverlapConflict, job.ID, readback.ActiveInstance)
		}
	}
	return before, nil
}

func removeDefinitions(ctx context.Context, adapter scheduler.Adapter, cfg *config.Config, before []scheduler.Readback) ([]scheduleMutationResult, error) {
	results := make([]scheduleMutationResult, 0, len(cfg.Jobs))
	for index, job := range cfg.Jobs {
		mutationErr := adapter.Remove(ctx, job.ID, false)
		readback, readbackErr := adapter.Readback(ctx, job.ID)
		if readbackErr == nil && !readback.Installed {
			results = append(results, scheduleMutationResult{JobID: job.ID, Action: "removed", Readback: readback})
			continue
		}
		cause := errors.Join(mutationErr, readbackErr)
		if cause == nil {
			cause = fmt.Errorf("scheduler remained installed for job %q", job.ID)
		}
		snapshots := make([]scheduler.Readback, 0, index+1)
		for rollbackIndex := range index + 1 {
			if before[rollbackIndex].Installed && before[rollbackIndex].Definition != nil {
				snapshots = append(snapshots, before[rollbackIndex])
			}
		}
		rollbackErr := restoreSchedulerSnapshots(ctx, adapter, snapshots)
		return nil, errors.Join(cause, rollbackErr)
	}
	return results, nil
}

func rollbackScheduler(ctx context.Context, adapter scheduler.Adapter, attempted []scheduler.Definition, before []scheduler.Readback) error {
	var rollbackErr error
	for index := len(attempted) - 1; index >= 0; index-- {
		snapshot := before[index]
		if !snapshot.Installed {
			mutationErr := adapter.Remove(ctx, attempted[index].JobID, false)
			readback, readbackErr := adapter.Readback(ctx, attempted[index].JobID)
			if readbackErr == nil && !readback.Installed {
				continue
			}
			rollbackErr = errors.Join(rollbackErr, mutationErr, readbackErr, fmt.Errorf("rollback removal readback mismatch for job %q", attempted[index].JobID))
			continue
		}
		rollbackErr = errors.Join(rollbackErr, restoreSchedulerSnapshot(ctx, adapter, snapshot))
	}
	return rollbackErr
}

func restoreSchedulerSnapshots(ctx context.Context, adapter scheduler.Adapter, snapshots []scheduler.Readback) error {
	var restoreErr error
	for index := len(snapshots) - 1; index >= 0; index-- {
		restoreErr = errors.Join(restoreErr, restoreSchedulerSnapshot(ctx, adapter, snapshots[index]))
	}
	return restoreErr
}

func restoreSchedulerSnapshot(ctx context.Context, adapter scheduler.Adapter, snapshot scheduler.Readback) error {
	if snapshot.Definition == nil {
		return errors.New("scheduler rollback snapshot has no recoverable definition")
	}
	definition := *snapshot.Definition
	definition.Generation = 0
	mutationErr := adapter.Install(ctx, definition, true)
	readback, readbackErr := adapter.Readback(ctx, definition.JobID)
	if readbackErr != nil || !scheduler.MatchesDefinition(readback, definition) {
		return errors.Join(mutationErr, readbackErr, fmt.Errorf("restore definition readback mismatch for job %q", definition.JobID))
	}
	if snapshot.Enabled {
		enableErr := adapter.SetEnabled(ctx, definition.JobID, true)
		readback, readbackErr = adapter.Readback(ctx, definition.JobID)
		if readbackErr != nil || !readback.Enabled || !scheduler.MatchesDefinition(readback, definition) {
			return errors.Join(mutationErr, enableErr, readbackErr, fmt.Errorf("restore enabled-state readback mismatch for job %q", definition.JobID))
		}
	}
	return nil
}

func renderScheduleMutationResults(cmd *cobra.Command, format string, results []scheduleMutationResult) error {
	if format == output.FormatJSON {
		return output.RenderJSON(cmd.OutOrStdout(), results)
	}
	for _, result := range results {
		fmt.Fprintf(cmd.OutOrStdout(), "job %s: %s; installed=%t enabled=%t health=%s\n", result.JobID, result.Action, result.Readback.Installed, result.Readback.Enabled, result.Readback.Health)
	}
	return nil
}

func selectedConfigPath(value string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return paths.ConfigFile()
}

func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func normalizePlatform(value string) (scheduler.Platform, error) {
	switch value {
	case "windows":
		return scheduler.PlatformWindows, nil
	case "linux":
		return scheduler.PlatformLinux, nil
	case "darwin":
		return scheduler.PlatformDarwin, nil
	default:
		return "", fmt.Errorf("unsupported scheduler platform %q", value)
	}
}
