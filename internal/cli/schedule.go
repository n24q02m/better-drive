package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/n24q02m/better-drive/internal/scheduler"
	"github.com/n24q02m/better-drive/internal/state"
	"github.com/spf13/cobra"
)

const aggregateSchedulerOwnerJobID = "scheduled-sync"

type schedulePreview struct {
	JobID      string `json:"job_id"`
	Platform   string `json:"platform"`
	Definition string `json:"definition"`
}

func scheduleCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "schedule",
		Short:   "Render and inspect managed scheduler definitions",
		Long:    "Scheduler commands are read-only in this phase. Install and remove require --dry-run and never mutate a live scheduler.",
		Example: "  better-drive schedule install --dry-run --format json",
	}
	c.AddCommand(scheduleInstallCmd(), scheduleStatusCmd(), scheduleRemoveCmd())
	return c
}

func scheduleInstallCmd() *cobra.Command {
	var platform string
	var format string
	var dryRun bool
	var replace bool
	c := &cobra.Command{
		Use:     "install",
		Short:   "Preview managed scheduler definitions",
		Long:    "Render one definition per configured job. Live registration is disabled until scheduler ownership and rollback gates pass.",
		Example: "  better-drive schedule install --dry-run --platform linux --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dryRun {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("schedule install requires --dry-run")), "run: better-drive schedule install --dry-run --format json")
			}
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			resolved, err := normalizePlatform(platform)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "use --platform windows, linux, or darwin")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			currentOwner, err := ownerRecordFromState()
			if err != nil {
				return err
			}

			// Validate definitions through the scheduler adapter seam while
			// enforcing no live registration.
			adapter := scheduler.NewMemoryAdapter(resolved, time.Now)
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			previews := make([]schedulePreview, 0, len(cfg.Jobs))
			for _, job := range cfg.Jobs {
				seconds := int(job.Interval / time.Second)
				definition := scheduler.Definition{
					JobID: job.ID, Executable: executable, Config: paths.ConfigFile(),
					IntervalSeconds: seconds, CatchUp: true, ExecutionLimitSeconds: max(3600, seconds*2),
					Owner: "better-drive",
				}
				if err := scheduler.ValidateOwner(currentOwner, definition, replace); err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(err), "use --replace only after an exact scheduler owner readback")
				}
				if err := adapter.Install(ctx, definition, replace); err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(err), "scheduler definition rejected by adapter validation")
				}
				definitionBytes, err := scheduler.Render(resolved, definition)
				if err != nil {
					return err
				}
				previews = append(previews, schedulePreview{JobID: job.ID, Platform: string(resolved), Definition: string(definitionBytes)})
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), previews)
			}
			for _, preview := range previews {
				fmt.Fprintf(cmd.OutOrStdout(), "job %s [%s]\n%s\n", preview.JobID, preview.Platform, preview.Definition)
			}
			return nil
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().StringVar(&platform, "platform", runtime.GOOS, "target scheduler platform: windows|linux|darwin")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "render without registering a live scheduler")
	c.Flags().BoolVar(&replace, "replace", false, "allow replacement only after an exact owner readback")
	return c
}

func scheduleStatusCmd() *cobra.Command {
	var format string
	c := &cobra.Command{
		Use:     "status",
		Short:   "Read persisted scheduler health",
		Long:    "Read scheduler health from persisted state without querying or mutating a live scheduler.",
		Example: "  better-drive schedule status --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			persisted, err := state.Load(paths.StateFile())
			if errors.Is(err, os.ErrNotExist) {
				persisted = &state.State{
					SchemaVersion: state.CurrentSchemaVersion,
					Scheduler: state.SchedulerState{
						Health:          state.HealthUnknown,
						OverlapState:    state.OverlapUnknown,
						FreshnessWindow: 15 * time.Minute,
						CatchUpGrace:    6*time.Hour + 15*time.Minute,
					},
				}
			} else if err != nil {
				return err
			}
			if err == nil {
				persisted.Scheduler.Health = state.EvaluateSchedulerHealth(persisted.Scheduler, time.Now().UTC())
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), persisted.Scheduler)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "scheduler health: %s\n", persisted.Scheduler.Health)
			return nil
		},
	}
	output.AddFormatFlag(c, &format)
	return c
}

func scheduleRemoveCmd() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:     "remove",
		Short:   "Preview scheduler removal",
		Long:    "Live scheduler removal is disabled in this phase; this command only confirms the requested no-op.",
		Example: "  better-drive schedule remove --dry-run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dryRun {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("schedule remove requires --dry-run")), "run: better-drive schedule remove --dry-run")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "dry-run: scheduler removal not applied")
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "preview without removing a live scheduler")
	return c
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

func ownerRecordFromState() (scheduler.OwnerRecord, error) {
	persisted, err := state.Load(paths.StateFile())
	if errors.Is(err, os.ErrNotExist) {
		return scheduler.OwnerRecord{}, nil
	}
	if err != nil {
		return scheduler.OwnerRecord{}, err
	}
	jobID := persisted.Scheduler.OwnerJobID
	if persisted.Scheduler.Owner == "better-drive" && jobID == aggregateSchedulerOwnerJobID {
		configured := false
		for _, job := range persisted.Jobs {
			if job.JobID == jobID {
				configured = true
				break
			}
		}
		if !configured {
			jobID = ""
		}
	}
	return scheduler.OwnerRecord{Owner: persisted.Scheduler.Owner, JobID: jobID}, nil
}
