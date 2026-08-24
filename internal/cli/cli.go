package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/n24q02m/better-drive/internal/autostart"
	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/n24q02m/better-drive/internal/state"
	"github.com/n24q02m/better-drive/internal/syncloop"
	"github.com/n24q02m/better-drive/internal/tray"
	"github.com/n24q02m/better-drive/internal/version"
	"github.com/spf13/cobra"
)

const maxInt64 = int64(1<<63 - 1)

// Execute runs the CLI against the real process args and reports the
// resolved --format alongside the error, so main can render a failure (see
// RenderError) in the format the user asked for instead of always printing
// plain text.
func Execute() (string, error) { return execute(nil) }

// execute is Execute's args-injectable body: nil means "use the real
// os.Args[1:]" (cobra's own default, for the production binary); a non-nil
// (possibly empty) slice pins the args explicitly, which is what tests use -
// SetArgs(nil) would instead leak the test binary's own -test.* flags into
// pflag (see the SetArgs([]string{}) comment on
// TestStatusCmd_TableFormatUnchanged for the same hazard one layer down).
//
// The format is read off whichever command actually ran (cmd, returned by
// ExecuteC), not a shared package variable: only status/sync register a
// local --format flag, so cmd.Flags().Lookup("format") returns nil - and
// this defaults to the table format - for every other command (and for an
// unrecognized subcommand, where cmd resolves to the root command itself).
func execute(args []string) (string, error) {
	root := newRootCmd()
	if args != nil {
		root.SetArgs(args)
	}
	cmd, err := root.ExecuteC()
	format := output.FormatTable
	if cmd != nil {
		if f := cmd.Flags().Lookup("format"); f != nil {
			format = f.Value.String()
		}
	}
	return format, err
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "better-drive",
		Short:   "Google Drive sync and virtual-drive mount with rclone",
		Version: version.Version,
		// cli.RenderError (via Execute) owns error rendering now, in whatever
		// format the caller asked for - cobra's own default "Error: ..." +
		// Usage: auto-print on a failing RunE must not also fire, or a
		// --format json caller would get that plain text ahead of (or
		// instead of) the JSON envelope.
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(accountCmd(), cleanupCmd(), configCmd(), restoreCmd(), scheduleCmd(), setupCmd(), runCmd(), statusCmd(), syncCmd(), mountCmd(), installCmd(), uninstallCmd())
	root.InitDefaultCompletionCmd()
	return root
}

// loadConfig reads and validates config.toml without making a network call.
// Sync-capable commands add the runtime/endpoint gate through
// loadExecutionConfig before constructing an engine.
func loadConfig() (*config.Config, error) {
	path := paths.ConfigFile()
	cfg, err := config.Load(path)
	if err != nil {
		return nil, exitcode.WithRemediation(exitcode.ConfigError(err),
			fmt.Sprintf("create or fix %s (TOML syntax) - see README for the [[job]] schema", path))
	}
	if err := cfg.Validate(); err != nil {
		return nil, exitcode.WithRemediation(exitcode.ConfigError(err),
			fmt.Sprintf("edit %s and fix the job(s) reported above", path))
	}
	return cfg, nil
}

func loadExecutionConfig() (*config.Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if err := cfg.ValidateForExecution(); err != nil {
		return nil, exitcode.WithRemediation(exitcode.ConfigError(err),
			fmt.Sprintf("enroll the pinned rclone_runtime in %s before running transfers", paths.ConfigFile()))
	}
	for _, job := range cfg.Jobs {
		if len(job.Destinations) == 0 {
			return nil, exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("job %q has no destinations", job.ID)),
				"add at least one [[job.destination]] block")
		}
		for _, destination := range job.Destinations {
			if _, err := destination.RcloneTarget(); err != nil {
				return nil, exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("job %q: %w", job.ID, err)),
					"set destination.credential_ref to an enrolled rclone:<remote> reference")
			}
		}
	}
	return cfg, nil
}

func replicasForJob(job config.Job) ([]engine.ReplicaSpec, error) {
	if len(job.Destinations) == 0 {
		return nil, fmt.Errorf("job %q has no destinations", job.ID)
	}
	identities := make([]engine.DestinationIdentity, 0, len(job.Destinations))
	for _, destination := range job.Destinations {
		identities = append(identities, engine.DestinationIdentity{Provider: destination.Backend, AccountID: destination.AccountID, RootID: destination.RootID, Namespace: destination.Path})
	}
	if err := engine.ValidateDestinationCollisions(identities); err != nil {
		return nil, fmt.Errorf("job %q: %w", job.ID, err)
	}
	replicas := make([]engine.ReplicaSpec, 0, len(job.Destinations))
	for i, destination := range job.Destinations {
		target, err := destination.RcloneTarget()
		if err != nil {
			return nil, fmt.Errorf("job %q destination %d: %w", job.ID, i, err)
		}
		replicaID := destination.RootID
		if replicaID == "" {
			replicaID = fmt.Sprintf("%s-%d", job.ID, i)
		}
		replicas = append(replicas, engine.ReplicaSpec{
			ID: replicaID, Target: target, Workdir: paths.JobReplicaWorkdir(job.ID, replicaID),
			Required: destination.Required, MinCompleteRestoreSets: destination.MinCompleteRestoreSets,
		})
	}
	return replicas, nil
}

func replicaResults(summary engine.ReplicaSummary) []output.ReplicaResult {
	results := make([]output.ReplicaResult, 0, len(summary.Outcomes))
	for _, outcome := range summary.Outcomes {
		item := output.ReplicaResult{ID: outcome.ID, Target: outcome.Target, Required: outcome.Required, Status: outcome.Status}
		if outcome.Err != nil {
			item.Error = outcome.Err.Error()
		}
		results = append(results, item)
	}
	return results
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func localSourceStats(path string) (objects, bytes int64, err error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if info.Mode().IsRegular() {
		if info.Size() < 0 {
			return 0, 0, fmt.Errorf("source size is negative")
		}
		return 1, info.Size(), nil
	}
	if !info.IsDir() {
		return 0, 0, nil
	}
	err = filepath.WalkDir(path, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		fileInfo, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		size := fileInfo.Size()
		if size < 0 || bytes > maxInt64-size {
			return fmt.Errorf("source byte count overflow")
		}
		if objects == maxInt64 {
			return fmt.Errorf("source object count overflow")
		}
		objects++
		bytes += size
		return nil
	})
	return objects, bytes, err
}

func attachLocalStats(result *output.PairResult) {
	objects, bytes, err := localSourceStats(result.Local)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("source inventory unavailable: %v", err))
		return
	}
	result.ObjectCount = objects
	result.ByteCount = bytes
}

func attachJobEvidence(result *output.PairResult, interval time.Duration) {
	result.NextDue = time.Now().UTC().Add(interval)
	attachLocalStats(result)
}

func buildStateFromResults(results []output.PairResult, now time.Time) state.State {
	scheduler := state.SchedulerState{
		Owner: "better-drive", OwnerJobID: aggregateSchedulerOwnerJobID, Enabled: true,
		LastTrigger: now, ActiveInstance: "one-shot", OverlapState: "none", OverlapHealth: "ok",
		ObservedAt: now, FreshnessWindow: 15 * time.Minute, CatchUpGrace: 6*time.Hour + 15*time.Minute,
	}
	persisted := state.State{SchemaVersion: state.CurrentSchemaVersion, EngineVersion: version.Version, Scheduler: scheduler}
	for _, result := range results {
		item := state.JobState{
			JobID: result.JobID, Status: result.Status, LastSuccess: time.Time{},
			NextDue: result.NextDue, ObjectCount: result.ObjectCount, ByteCount: result.ByteCount,
			Warnings: append([]string(nil), result.Warnings...),
		}
		if item.JobID == "" {
			item.JobID = result.Local + "->" + result.Remote
		}
		if result.Status == "ok" || result.Status == "degraded" {
			item.LastSuccess = now
		}
		for _, replica := range result.Replicas {
			item.ReplicaOutcomes = append(item.ReplicaOutcomes, state.ReplicaState{ID: replica.ID, Target: replica.Target, Required: replica.Required, Status: replica.Status, Error: replica.Error})
		}
		persisted.Jobs = append(persisted.Jobs, item)
		if !result.NextDue.IsZero() && (persisted.Scheduler.NextTrigger.IsZero() || result.NextDue.Before(persisted.Scheduler.NextTrigger)) {
			persisted.Scheduler.NextTrigger = result.NextDue
		}
	}
	persisted.Scheduler.Health = state.EvaluateSchedulerHealth(persisted.Scheduler, now)
	return persisted
}

// badFormatErr wraps an invalid --format value (output.Validate's error) as
// an exitcode.ConfigError with a remediation hint, shared by status and sync
// - the two commands with a --format flag.
func badFormatErr(err error) error {
	return exitcode.WithRemediation(exitcode.ConfigError(err),
		fmt.Sprintf("use --format %s or --format %s", output.FormatTable, output.FormatJSON))
}

// remoteNotConfiguredErr reports remoteName as missing or credential-less,
// with a remediation hint that is the exact command to fix it. Extracted out of
// runCmd's RunE (its only call site) so it can be unit-tested without a real
// engine.Engine/rclone binary - the same reasoning runSyncOnce's doc comment
// gives for taking a syncloop.Syncer parameter instead of constructing one.
func remoteNotConfiguredErr(remoteName string) error {
	err := fmt.Errorf("remote %q is not set up; run: better-drive setup", remoteName)
	return exitcode.WithRemediation(exitcode.RemoteNotConfiguredError(err),
		fmt.Sprintf("run: better-drive setup --remote %s", remoteName))
}

// resyncCommand is the exact command that rebuilds a lost bisync baseline.
// It is named in the failure's MESSAGE, not just in its remediation hint,
// because RenderError prints a remediation only for a --format json caller: a
// hint kept solely in that field never reaches a terminal user, who would then
// be told the baseline is unusable without being told what to do about it.
const resyncCommand = "better-drive sync --resync"

// syncFailedErr reports that at least one pair failed. needsResync marks that
// at least one of them failed with engine.ErrNeedsResync, i.e. rclone has no
// usable baseline listing for that pair's session. That failure is not
// transient - it repeats identically on every subsequent run - so it earns a
// message and a remediation naming the command that rebuilds the baseline,
// rather than the generic "re-run: better-drive sync" that would loop forever.
// The rebuild stays an explicit user action because a --resync does not
// propagate deletions and would resurrect anything deleted since the baseline
// broke.
func syncFailedErr(needsResync bool) error {
	msg := "sync: one or more pairs failed"
	hint := "see the FAILED line(s) on stderr above for per-pair detail (or the \"error\" field of each --format json result), then re-run: better-drive sync"
	if needsResync {
		msg += "; a bisync pair has no usable baseline - rebuild it with: " + resyncCommand
		hint = "run: " + resyncCommand + " (rebuilds the bisync baseline; deletions made since it broke are not propagated)"
	}
	return exitcode.WithRemediation(exitcode.SyncFailed(errors.New(msg)), hint)
}

func installCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Register better-drive to start at login (hidden tray daemon)",
		Long: "Register the current executable to start automatically at login, running\n" +
			"the same sync daemon as `better-drive run` (tray icon, all configured\n" +
			"pairs). Safe to run again: re-registers the current binary's path.",
		Example: "  better-drive install",
		RunE: func(cmd *cobra.Command, _ []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if err := autostart.Enable(exe); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed: %q run will start at login\n", exe)
			return nil
		},
	}
}

func uninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove better-drive from login autostart",
		Long: "Remove the login-autostart registration added by `better-drive install`.\n" +
			"Does not touch config.toml, the rclone remote, or any synced files.",
		Example: "  better-drive uninstall",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := autostart.Disable(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "uninstalled: login autostart removed")
			return nil
		},
	}
}

// setupCmd is `better-drive setup`, the published name of the command that
// creates a Drive account. It is built by newAccountAddCmd, the same
// constructor `account add` uses, so the two names can never drift apart -
// see that function for why both exist.
func setupCmd() *cobra.Command {
	return newAccountAddCmd("setup",
		"Create the rclone Google Drive remote (opens browser for OAuth)",
		"Create (or repair) an rclone Google Drive remote via `rclone config\n"+
			"create`, which opens a browser for OAuth. Idempotent: a remote that\n"+
			"already has an OAuth token or service-account file is left alone; a broken,\n"+
			"credential-less remote left behind by an interrupted setup is deleted and recreated.\n"+
			"`better-drive account add` is the same command under its other name.",
		"  better-drive setup\n"+
			"  better-drive setup --remote gdrive-work")
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Start the sync daemon (all configured jobs) with a tray icon showing combined status",
		Long: "Start the continuous sync daemon: one sync loop per job in config.toml,\n" +
			"each on its own interval/mode, plus a system-tray icon showing the\n" +
			"combined status. Blocks until the tray is quit. Every remote referenced\n" +
			"by a job must already be set up (`better-drive setup`).",
		Example: "  better-drive run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadExecutionConfig()
			if err != nil {
				return err
			}

			e, err := engine.NewVerified(cfg.RcloneRuntime)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), fmt.Sprintf("fix the pinned rclone_runtime in %s", paths.ConfigFile()))
			}
			for _, job := range cfg.Jobs {
				replicas, replicaErr := replicasForJob(job)
				if replicaErr != nil {
					e.Close()
					return exitcode.WithRemediation(exitcode.ConfigError(replicaErr), "fix the job destination identities before running")
				}
				for _, replica := range replicas {
					remoteName, _, _ := strings.Cut(replica.Target, ":")
					if configured, _ := e.RemoteConfigured(remoteName); !configured {
						e.Close()
						return remoteNotConfiguredErr(remoteName)
					}
				}
			}

			// Persistent sync log: the tray only ever shows the LATEST state
			// (an Error icon gives no history), so every cycle's outcome is
			// also appended to a log file. Best-effort - a failure to open it
			// must not block the daemon, just run with no logger.
			var logger *log.Logger
			logFile, logErr := os.OpenFile(paths.LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if logErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not open log file %q: %v (continuing without sync logging)\n", paths.LogFile(), logErr)
			} else {
				logger = log.New(logFile, "", log.LstdFlags)
				logger.Printf("daemon started, %d jobs", len(cfg.Jobs))
			}

			var stateMu sync.Mutex
			stateResults := make(map[string]output.PairResult, len(cfg.Jobs))
			persistDaemonResult := func(job config.Job, loop *syncloop.Loop, cycleErr error) {
				summary := loop.LastReplicaSummary()
				status := summary.Status
				if cycleErr != nil {
					status = "failed"
				}
				targets := make([]string, 0, len(summary.Outcomes))
				for _, outcome := range summary.Outcomes {
					targets = append(targets, outcome.Target)
				}
				result := output.PairResult{JobID: job.ID, Local: job.Source, Remote: strings.Join(targets, ","), Mode: job.Mode, Status: status, Error: errorString(cycleErr), Replicas: replicaResults(summary)}
				attachJobEvidence(&result, job.Interval)
				stateMu.Lock()
				stateResults[job.ID] = result
				snapshot := make([]output.PairResult, 0, len(stateResults))
				for _, item := range stateResults {
					snapshot = append(snapshot, item)
				}
				stateMu.Unlock()
				if err := state.Save(paths.StateFile(), buildStateFromResults(snapshot, time.Now().UTC())); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not persist state: %v\n", err)
				}
			}

			agg := tray.NewAggregator()
			loops := make([]*syncloop.Loop, len(cfg.Jobs))
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			for i, job := range cfg.Jobs {
				job := job
				replicas, replicaErr := replicasForJob(job)
				if replicaErr != nil {
					cancel()
					return exitcode.WithRemediation(exitcode.ConfigError(replicaErr), "fix the job destination identities before running")
				}
				loop := syncloop.NewWithReplicas(e, job.Source, replicas, job.Mode, job.Direction,
					func() ([]string, error) { return config.PairFilters(job.Source, job.Exclude) })
				loops[i] = loop
				agg.Register(i, loop)
				loop.OnResult(func(err error) {
					persistDaemonResult(job, loop, err)
					if logger == nil {
						return
					}
					if err != nil {
						logger.Printf("job %s [mode=%s]: FAILED: %v", job.ID, job.Mode, err)
						return
					}
					logger.Printf("job %s [mode=%s]: %s", job.ID, job.Mode, loop.LastReplicaSummary().Status)
				})
				wg.Add(1)
				go func() {
					defer wg.Done()
					loop.Start(ctx, job.Interval)
				}()
			}

			err = tray.Run(loops, cfg.Jobs, agg)
			cancel()
			syncloop.ShutdownAll(loops)
			wg.Wait()
			e.Close()
			if logFile != nil {
				if closeErr := logFile.Close(); closeErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not close log file: %v\n", closeErr)
				}
			}
			return err
		},
	}
}

func statusCmd() *cobra.Command {
	var format string
	c := &cobra.Command{
		Use:   "status",
		Short: "Print current config (every job)",
		Long: "Print every job from config.toml: source, destination, interval and mode.\n" +
			"Read-only - makes no rclone call and never touches the network. Use\n" +
			"--format json for machine-readable output.",
		Example: "  better-drive status\n" +
			"  better-drive status --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			cfg, err := config.Load(paths.ConfigFile())
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), fmt.Sprintf("create or fix %s (TOML syntax) - see README for the [[job]] schema", paths.ConfigFile()))
			}
			configWarning := ""
			if validationErr := cfg.ValidateForExecution(); validationErr != nil {
				configWarning = validationErr.Error()
			}
			persisted, stateErr := state.Load(paths.StateFile())
			if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
				return exitcode.WithRemediation(exitcode.ConfigError(stateErr), fmt.Sprintf("repair persisted state at %s", paths.StateFile()))
			}
			jobState := func(jobID string) *state.JobState {
				if persisted == nil {
					return nil
				}
				for i := range persisted.Jobs {
					if persisted.Jobs[i].JobID == jobID {
						return &persisted.Jobs[i]
					}
				}
				return nil
			}
			health := "unknown-overlap"
			if persisted != nil {
				health = state.EvaluateSchedulerHealth(persisted.Scheduler, time.Now().UTC())
			}
			if format == output.FormatJSON {
				pairs := make([]output.PairStatus, 0, len(cfg.Jobs))
				for _, job := range cfg.Jobs {
					for _, destination := range job.Destinations {
						target, targetErr := destination.RcloneTarget()
						if targetErr != nil {
							return exitcode.WithRemediation(exitcode.ConfigError(targetErr), "set a valid destination.credential_ref")
						}
						item := output.PairStatus{JobID: job.ID, Local: job.Source, Remote: target, Mode: job.Mode, Interval: job.Interval.String(), Health: health}
						if configWarning != "" {
							item.Warnings = []string{"config is not execution-ready: " + configWarning}
						}
						if evidence := jobState(job.ID); evidence != nil {
							item.JobStatus = evidence.Status
							if !evidence.LastSuccess.IsZero() {
								item.LastSuccess = &evidence.LastSuccess
							}
							if !evidence.NextDue.IsZero() {
								item.NextDue = &evidence.NextDue
							}
							item.ObjectCount = evidence.ObjectCount
							item.ByteCount = evidence.ByteCount
							item.Warnings = append(item.Warnings, evidence.Warnings...)
						} else {
							item.JobStatus = "unknown"
						}
						pairs = append(pairs, item)
					}
				}
				return output.RenderJSON(cmd.OutOrStdout(), pairs)
			}
			if configWarning != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: config is not execution-ready: %s\n", configWarning)
			}
			for _, job := range cfg.Jobs {
				for _, destination := range job.Destinations {
					target, targetErr := destination.RcloneTarget()
					if targetErr != nil {
						return exitcode.WithRemediation(exitcode.ConfigError(targetErr), "set a valid destination.credential_ref")
					}
					fmt.Fprintf(cmd.OutOrStdout(), "job %s: %s <-> %s every %s [mode=%s]\n", job.ID, job.Source, target, job.Interval, job.Mode)
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "run `better-drive run` to start")
			return nil
		},
	}
	output.AddFormatFlag(c, &format)
	return c
}

// syncCmd runs exactly one sync cycle for every configured pair, then exits -
// no tray, no ticker. It is meant to be invoked by an external scheduler (a
// Windows Scheduled Task) in place of a one-shot backup script: same config,
// same per-pair mode/filters/workdir as `run`, but a single pass instead of a
// continuous daemon.
func syncCmd() *cobra.Command {
	var format string
	var dryRun bool
	var resync bool
	c := &cobra.Command{
		Use:   "sync",
		Short: "Run exactly one sync cycle for every configured pair, then exit (for a scheduled task)",
		Long: "Run a single sync cycle for every pair in config.toml, then exit - no tray,\n" +
			"no ticker. A pair whose local path does not exist is SKIPPED (not a\n" +
			"failure). Successful pairs are reported on stdout; SKIPPED and FAILED\n" +
			"pairs go to stderr. Use --format json for machine-readable output and\n" +
			"--dry-run to preview changes without applying them.\n\n" +
			"--resync rebuilds the bisync baseline of every bisync-mode pair. It is\n" +
			"the recovery path for a pair reporting that its baseline is missing or\n" +
			"unusable. Use it only when that happens: a resync does NOT propagate\n" +
			"deletions, so files deleted on one side since the baseline broke come\n" +
			"back from the other. Combine it with --dry-run to preview first.",
		Example: "  better-drive sync\n" +
			"  better-drive sync --dry-run\n" +
			"  better-drive sync --resync\n" +
			"  better-drive sync --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			cmd.SetContext(ctx)
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			cfg, err := loadExecutionConfig()
			if err != nil {
				return err
			}

			e, err := engine.NewVerified(cfg.RcloneRuntime)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), fmt.Sprintf("fix the pinned rclone_runtime in %s", paths.ConfigFile()))
			}
			defer e.Close()
			results, syncErr := runSyncOnce(cmd, e, cfg, format, dryRun, resync)
			if stateErr := state.Save(paths.StateFile(), buildStateFromResults(results, time.Now().UTC())); stateErr != nil {
				if syncErr != nil {
					return fmt.Errorf("%v; persist state: %w", syncErr, stateErr)
				}
				return stateErr
			}
			return syncErr
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without modifying anything")
	c.Flags().BoolVar(&resync, "resync", false, "rebuild the bisync baseline of every bisync pair (recovery; does not propagate deletions)")
	return c
}

// runSyncOnce builds one syncloop.Loop per configured pair (same workdir
// convention as runCmd, so a bisync-mode pair's baseline is shared with the
// `run` daemon) and runs exactly one RunOnce cycle on each. In the table
// format it prints a per-pair OK line to stdout as each pair finishes (the
// pre-existing behavior); in the json format nothing is written per pair -
// the full []output.PairResult is rendered once, after the loop. Diagnostics
// (SKIPPED, FAILED) always go to stderr, in both formats. It returns the
// per-pair results (for callers/tests that need the outcome directly) and a
// non-nil error if any pair failed. The Syncer is a parameter (rather than
// constructed here) so tests can inject a fake instead of a real
// engine.Engine, which would make a real Drive rc call. forceResync backs the
// --resync flag; it applies to bisync-mode pairs only, the other modes keeping
// no baseline to rebuild.
func runSyncOnce(cmd *cobra.Command, s syncloop.Syncer, cfg *config.Config, format string, dryRun, forceResync bool) ([]output.PairResult, error) {
	if dryRun {
		fmt.Fprintln(cmd.ErrOrStderr(), "dry-run: no changes will be made")
	}
	failed := false
	needsResync := false
	results := make([]output.PairResult, 0, len(cfg.Jobs))
	for _, job := range cfg.Jobs {
		replicas, err := replicasForJob(job)
		if err != nil {
			return results, err
		}
		targets := make([]string, 0, len(replicas))
		for _, replica := range replicas {
			targets = append(targets, replica.Target)
		}
		remoteSummary := strings.Join(targets, ",")
		if _, statErr := os.Stat(job.Source); errors.Is(statErr, os.ErrNotExist) {
			status := "skipped_optional"
			if job.Required {
				status = "failed"
				failed = true
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "job %s %s <-> %s [mode=%s]: %s (local not found)\n", job.ID, job.Source, remoteSummary, job.Mode, strings.ToUpper(status))
			result := output.PairResult{JobID: job.ID, Local: job.Source, Remote: remoteSummary, Mode: job.Mode, Status: status}
			attachJobEvidence(&result, job.Interval)
			results = append(results, result)
			continue
		}
		loop := syncloop.NewWithReplicas(s, job.Source, replicas, job.Mode, job.Direction,
			func() ([]string, error) { return config.PairFilters(job.Source, job.Exclude) })
		loop.SetDryRun(dryRun)
		loop.SetForceResync(forceResync)
		loop.SetExecution(cmd.Context(), cmd.ErrOrStderr())
		fmt.Fprintf(cmd.ErrOrStderr(), "job %s %s <-> %s [mode=%s]: RUNNING\n", job.ID, job.Source, remoteSummary, job.Mode)
		if err := loop.RunOnce(); err != nil {
			if errors.Is(err, context.Canceled) {
				return results, err
			}
			failed = true
			needsResync = needsResync || errors.Is(err, engine.ErrNeedsResync)
			fmt.Fprintf(cmd.ErrOrStderr(), "job %s %s <-> %s [mode=%s]: FAILED: %v\n", job.ID, job.Source, remoteSummary, job.Mode, err)
			result := output.PairResult{JobID: job.ID, Local: job.Source, Remote: remoteSummary, Mode: job.Mode, Status: "failed", Error: err.Error(), Replicas: replicaResults(loop.LastReplicaSummary()), DryRun: dryRun}
			attachJobEvidence(&result, job.Interval)
			results = append(results, result)
			continue
		}
		status := loop.LastReplicaSummary().Status
		if status == "" {
			status = "ok"
		}
		if format == output.FormatTable {
			fmt.Fprintf(cmd.OutOrStdout(), "job %s %s <-> %s [mode=%s]: %s\n", job.ID, job.Source, remoteSummary, job.Mode, strings.ToUpper(status))
		}
		result := output.PairResult{JobID: job.ID, Local: job.Source, Remote: remoteSummary, Mode: job.Mode, Status: status, Replicas: replicaResults(loop.LastReplicaSummary()), DryRun: dryRun}
		attachJobEvidence(&result, job.Interval)
		results = append(results, result)
	}
	if format == output.FormatJSON {
		if err := output.RenderJSON(cmd.OutOrStdout(), results); err != nil {
			return results, err
		}
	}
	if failed {
		return results, syncFailedErr(needsResync)
	}
	return results, nil
}
