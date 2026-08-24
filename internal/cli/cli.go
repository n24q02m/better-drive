package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/n24q02m/better-drive/internal/autostart"
	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/n24q02m/better-drive/internal/runlog"
	"github.com/n24q02m/better-drive/internal/state"
	"github.com/n24q02m/better-drive/internal/syncloop"
	"github.com/n24q02m/better-drive/internal/tray"
	"github.com/n24q02m/better-drive/internal/version"
	"github.com/spf13/cobra"
)

const maxInt64 = int64(1<<63 - 1)

// Execute runs with zero-value dependencies; restore execution therefore
// fails closed until an enrolled host uses ExecuteWithDependencies.
func Execute() (string, error) { return execute(nil) }

// ExecuteWithDependencies runs the CLI against the real process args using
// the enrolled runtime services supplied by the host.
func ExecuteWithDependencies(deps RuntimeDependencies) (string, error) {
	return executeWithDependencies(nil, deps)
}

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
	return executeWithDependencies(args, RuntimeDependencies{})
}

func executeWithDependencies(args []string, deps RuntimeDependencies) (string, error) {
	root := newRootCmdWithDependencies(deps)
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
	return newRootCmdWithDependencies(RuntimeDependencies{})
}

func newRootCmdWithDependencies(deps RuntimeDependencies) *cobra.Command {
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
	root.AddCommand(accountCmd(), cleanupCmd(), configCmd(), restoreCmdWithDependencies(deps), scheduleCmd(), setupCmd(), runCmdWithDependencies(deps), statusCmd(), syncCmdWithDependencies(deps), mountCmd(), installCmd(), uninstallCmd())
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
	resolver := config.FileBindingResolver{}
	return loadExecutionConfigWithBindings(resolver, resolver)
}

func loadExecutionConfigWithBindings(policyResolver config.PolicyBindingResolver, roleResolver config.RoleBindingResolver) (*config.Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if err := validateExecutionConfigWithBindings(cfg, policyResolver, roleResolver); err != nil {
		return nil, exitcode.WithRemediation(exitcode.ConfigError(err),
			fmt.Sprintf("enroll the pinned runtime and role/policy bindings in %s before running transfers", paths.ConfigFile()))
	}
	return cfg, nil
}

func validateExecutionConfigWithBindings(cfg *config.Config, policyResolver config.PolicyBindingResolver, roleResolver config.RoleBindingResolver) error {
	if err := cfg.ValidateForExecutionWithBindings(policyResolver, roleResolver); err != nil {
		return err
	}
	for _, job := range cfg.Jobs {
		if len(job.Destinations) == 0 {
			return fmt.Errorf("job %q has no destinations", job.ID)
		}
		for _, destination := range job.Destinations {
			if _, err := destination.RcloneTarget(); err != nil {
				return fmt.Errorf("job %q: %w", job.ID, err)
			}
		}
	}
	return nil
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
		replicaID := replicaIDForDestination(job.ID, i, destination)
		replicas = append(replicas, engine.ReplicaSpec{
			ID: replicaID, Target: target, Workdir: paths.JobReplicaWorkdir(job.ID, replicaID),
			Required: destination.Required,
			// The configured restore floor is enforced by the retention
			// coordinator against provider inventory/readbacks. Ordinary
			// transfer dispatch has no exact pre-transfer acknowledgements,
			// so passing the configured value here would falsely fail every
			// enrolled transfer.
			MinCompleteRestoreSets: 0,
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
		Owner: "better-drive", Enabled: true,
		LastTrigger: now, ActiveInstance: "one-shot", OverlapState: state.OverlapNone, OverlapHealth: "ok",
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
	if len(persisted.Jobs) == 1 && strings.TrimSpace(persisted.Jobs[0].JobID) != "" {
		persisted.Scheduler.OwnerJobID = persisted.Jobs[0].JobID
	}
	persisted.Scheduler.Health = state.EvaluateSchedulerHealth(persisted.Scheduler, now)
	return persisted
}

// persistDaemonResult serializes the complete daemon result snapshot with its
// durable state write. Keeping the lock through Save prevents an older
// callback from replacing a newer snapshot after the newer callback updates
// the in-memory result map.
func persistDaemonResult(stateMu *sync.Mutex, stateResults map[string]output.PairResult, statePath string, result output.PairResult, now time.Time) error {
	stateMu.Lock()
	stateResults[result.JobID] = result
	snapshot := make([]output.PairResult, 0, len(stateResults))
	for _, item := range stateResults {
		snapshot = append(snapshot, item)
	}
	sort.SliceStable(snapshot, func(i, j int) bool {
		return snapshot[i].JobID < snapshot[j].JobID
	})
	saveErr := state.Save(statePath, buildStateFromResults(snapshot, now))
	stateMu.Unlock()
	return saveErr
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
	return runCmdWithDependencies(RuntimeDependencies{})
}

func runCmdWithDependencies(deps RuntimeDependencies) *cobra.Command {
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

			if err := deps.validateForConfig(cfg); err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err),
					fmt.Sprintf("enroll retention runtime dependencies before running quarantine jobs in %s", paths.ConfigFile()))
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

			fileLog, logErr := runlog.OpenFile(paths.LogFile(), "", runlog.RotationOptions{})
			if logErr != nil {
				e.Close()
				return fmt.Errorf("open daemon audit log %q: %w", paths.LogFile(), logErr)
			}
			if _, emitErr := fileLog.Sink.Emit(runlog.StreamSystem, fmt.Sprintf("daemon started, %d jobs", len(cfg.Jobs))); emitErr != nil {
				e.Close()
				return finalizeDaemonLog(fileLog, fmt.Errorf("write daemon start event: %w", emitErr))
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			var auditMu sync.Mutex
			var auditErr error
			recordAuditError := func(err error) {
				if err == nil {
					return
				}
				auditMu.Lock()
				if auditErr == nil {
					auditErr = err
					cancel()
				}
				auditMu.Unlock()
			}
			currentAuditError := func() error {
				auditMu.Lock()
				defer auditMu.Unlock()
				return auditErr
			}

			var stateMu sync.Mutex
			stateResults := make(map[string]output.PairResult, len(cfg.Jobs))
			persistResult := func(job config.Job, loop *syncloop.Loop, cycleErr error) (engine.ReplicaSummary, error) {
				summary := loop.LastReplicaSummary()
				summary, resultErr := applyRetentionRuntime(ctx, job, summary, cycleErr, deps)
				status := summary.Status
				if resultErr != nil {
					status = "failed"
				}
				targets := make([]string, 0, len(summary.Outcomes))
				for _, outcome := range summary.Outcomes {
					targets = append(targets, outcome.Target)
				}
				result := output.PairResult{
					JobID: job.ID, Local: job.Source, Remote: strings.Join(targets, ","), Mode: job.Mode,
					Status: status, Error: pairResultError(summary, resultErr), Replicas: replicaResults(summary),
				}
				attachJobEvidence(&result, job.Interval)
				saveErr := persistDaemonResult(&stateMu, stateResults, paths.StateFile(), result, time.Now().UTC())
				if saveErr != nil {
					recordAuditError(fmt.Errorf("persist daemon state: %w", saveErr))
				}
				return summary, resultErr
			}

			agg := tray.NewAggregator()
			loops := make([]*syncloop.Loop, len(cfg.Jobs))
			var wg sync.WaitGroup
			for i, job := range cfg.Jobs {
				job := job
				replicas, replicaErr := replicasForJob(job)
				if replicaErr != nil {
					cancel()
					e.Close()
					return finalizeDaemonLog(fileLog, exitcode.WithRemediation(exitcode.ConfigError(replicaErr), "fix the job destination identities before running"))
				}
				loop := syncloop.NewWithReplicas(e, job.Source, replicas, job.Mode, job.Direction,
					func() ([]string, error) { return config.PairFilters(job.Source, job.Exclude) })
				loops[i] = loop
				loop.SetExecution(ctx, io.MultiWriter(cmd.ErrOrStderr(), fileLog.Sink.Stderr()))
				agg.Register(i, loop)
				loop.OnResult(func(err error) {
					summary, resultErr := persistResult(job, loop, err)
					stream := runlog.StreamStdout
					message := fmt.Sprintf("job %s [mode=%s]: %s", job.ID, job.Mode, summary.Status)
					if resultErr != nil {
						stream = runlog.StreamStderr
						message = fmt.Sprintf("job %s [mode=%s]: FAILED: %v", job.ID, job.Mode, resultErr)
					}
					if _, emitErr := fileLog.Sink.Emit(stream, message); emitErr != nil {
						recordAuditError(fmt.Errorf("write daemon job event: %w", emitErr))
					}
				})
				wg.Add(1)
				go func() {
					defer wg.Done()
					loop.Start(ctx, job.Interval)
				}()
			}

			trayErr := tray.Run(loops, cfg.Jobs, agg)
			cancel()
			syncloop.ShutdownAll(loops)
			wg.Wait()
			e.Close()
			return finalizeDaemonLog(fileLog, errors.Join(trayErr, currentAuditError()))
		},
	}
}

func finalizeDaemonLog(fileLog *runlog.FileLog, runErr error) error {
	if fileLog == nil || fileLog.Sink == nil {
		return errors.Join(runErr, errors.New("daemon audit log is unavailable"))
	}
	outcome := "success"
	message := "daemon stopped"
	if errors.Is(runErr, context.Canceled) {
		outcome = "cancelled"
		message = runErr.Error()
	} else if runErr != nil {
		outcome = "error"
		message = runErr.Error()
	}
	var terminalErr error
	if _, err := fileLog.Sink.Terminal(outcome, message); err != nil {
		terminalErr = fmt.Errorf("write daemon terminal event: %w", err)
	}
	var closeErr error
	if err := fileLog.Close(); err != nil {
		closeErr = fmt.Errorf("close daemon audit log: %w", err)
	}
	return errors.Join(runErr, terminalErr, closeErr)
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

			now := time.Now().UTC()
			schedState := state.SchedulerState{
				FreshnessWindow: 15 * time.Minute,
				CatchUpGrace:    6*time.Hour + 15*time.Minute,
				OverlapState:    state.OverlapNone,
				OverlapHealth:   "missing",
				Health:          state.HealthMissing,
			}
			health := state.HealthMissing
			if persisted != nil {
				schedState = persisted.Scheduler
				health = state.EvaluateSchedulerHealth(schedState, now)
				schedState.Health = health
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

				schedulerOut := output.SchedulerStatus{
					Owner:           schedState.Owner,
					OwnerJobID:      schedState.OwnerJobID,
					Enabled:         schedState.Enabled,
					ActiveInstance:  schedState.ActiveInstance,
					OverlapState:    schedState.OverlapState,
					OverlapHealth:   schedState.OverlapHealth,
					ObservedAt:      schedState.ObservedAt,
					FreshnessWindow: schedState.FreshnessWindow,
					CatchUpGrace:    schedState.CatchUpGrace,
					Health:          health,
				}
				if !schedState.LastTrigger.IsZero() {
					schedulerOut.LastTrigger = &schedState.LastTrigger
				}
				if !schedState.NextTrigger.IsZero() {
					schedulerOut.NextTrigger = &schedState.NextTrigger
				}
				if configWarning != "" {
					schedulerOut.Warnings = []string{"config is not execution-ready: " + configWarning}
				}

				envelope := output.StatusEnvelope{
					Scheduler: schedulerOut,
					Pairs:     pairs,
				}
				return output.RenderJSON(cmd.OutOrStdout(), envelope)
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
	return syncCmdWithDependencies(RuntimeDependencies{})
}

func syncCmdWithDependencies(deps RuntimeDependencies) *cobra.Command {
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
			if err := deps.validateForConfig(cfg); err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err),
					fmt.Sprintf("enroll retention runtime dependencies before running quarantine jobs in %s", paths.ConfigFile()))
			}

			e, err := engine.NewVerified(cfg.RcloneRuntime)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), fmt.Sprintf("fix the pinned rclone_runtime in %s", paths.ConfigFile()))
			}
			defer e.Close()

			sink := runlog.NewSink("", io.Discard)
			var syncErr error
			var results []output.PairResult

			runErr := runlog.Run(ctx, sink, func() error {
				var err error
				results, err = runSyncOnce(cmd, e, cfg, format, dryRun, resync, deps)
				syncErr = err
				return err
			})

			if stateErr := state.Save(paths.StateFile(), buildStateFromResults(results, time.Now().UTC())); stateErr != nil {
				if syncErr != nil {
					return fmt.Errorf("%v; persist state: %w", syncErr, stateErr)
				}
				return stateErr
			}
			if runErr != nil && syncErr == nil {
				return runErr
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
func runSyncOnce(cmd *cobra.Command, s syncloop.Syncer, cfg *config.Config, format string, dryRun, forceResync bool, deps RuntimeDependencies) ([]output.PairResult, error) {
	if err := deps.validateForConfig(cfg); err != nil {
		return nil, err
	}
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
		loop.SetExecution(cmd.Context(), cmd.ErrOrStderr())
		fmt.Fprintf(cmd.ErrOrStderr(), "job %s %s <-> %s [mode=%s]: RUNNING\n", job.ID, job.Source, remoteSummary, job.Mode)
		cycleErr := loop.RunOnce()
		summary := loop.LastReplicaSummary()
		summary, cycleErr = applyRetentionRuntime(cmd.Context(), job, summary, cycleErr, deps)
		if cycleErr != nil {
			if errors.Is(cycleErr, context.Canceled) {
				return results, cycleErr
			}
			failed = true
			needsResync = needsResync || errors.Is(cycleErr, engine.ErrNeedsResync)
			fmt.Fprintf(cmd.ErrOrStderr(), "job %s %s <-> %s [mode=%s]: FAILED: %v\n", job.ID, job.Source, remoteSummary, job.Mode, cycleErr)
			result := output.PairResult{
				JobID: job.ID, Local: job.Source, Remote: remoteSummary, Mode: job.Mode,
				Status: "failed", Error: pairResultError(summary, cycleErr), Replicas: replicaResults(summary), DryRun: dryRun,
			}
			attachJobEvidence(&result, job.Interval)
			results = append(results, result)
			continue
		}
		status := summary.Status
		if status == "" {
			status = "ok"
		}
		if format == output.FormatTable {
			fmt.Fprintf(cmd.OutOrStdout(), "job %s %s <-> %s [mode=%s]: %s\n", job.ID, job.Source, remoteSummary, job.Mode, strings.ToUpper(status))
		}
		result := output.PairResult{
			JobID: job.ID, Local: job.Source, Remote: remoteSummary, Mode: job.Mode,
			Status: status, Error: pairResultError(summary, nil), Replicas: replicaResults(summary), DryRun: dryRun,
		}
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
