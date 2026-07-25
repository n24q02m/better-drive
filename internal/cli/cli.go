package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/n24q02m/better-drive/internal/autostart"
	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/n24q02m/better-drive/internal/syncloop"
	"github.com/n24q02m/better-drive/internal/tray"
	"github.com/n24q02m/better-drive/internal/version"
	"github.com/spf13/cobra"
)

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
		Short:   "Google Drive sync (bisync/copy/sync modes) with .driveignore + config excludes, multi-pair",
		Version: version.Version,
		// cli.RenderError (via Execute) owns error rendering now, in whatever
		// format the caller asked for - cobra's own default "Error: ..." +
		// Usage: auto-print on a failing RunE must not also fire, or a
		// --format json caller would get that plain text ahead of (or
		// instead of) the JSON envelope.
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(accountCmd(), setupCmd(), runCmd(), statusCmd(), syncCmd(), installCmd(), uninstallCmd())
	root.InitDefaultCompletionCmd()
	return root
}

// loadConfig reads and validates config.toml (the same two-step Load +
// Validate that run/status/sync all did inline), wrapping either failure as
// an exitcode.ConfigError with a remediation hint that names the resolved
// config path - so all three commands report the same actionable hint
// instead of each carrying its own copy of the wrapping.
func loadConfig() (*config.Config, error) {
	path := paths.ConfigFile()
	cfg, err := config.Load(path)
	if err != nil {
		return nil, exitcode.WithRemediation(exitcode.ConfigError(err),
			fmt.Sprintf("create or fix %s (TOML syntax) - see README for the [[pair]] schema", path))
	}
	if err := cfg.Validate(); err != nil {
		return nil, exitcode.WithRemediation(exitcode.ConfigError(err),
			fmt.Sprintf("edit %s and fix the pair(s) reported above", path))
	}
	return cfg, nil
}

// badFormatErr wraps an invalid --format value (output.Validate's error) as
// an exitcode.ConfigError with a remediation hint, shared by status and sync
// - the two commands with a --format flag.
func badFormatErr(err error) error {
	return exitcode.WithRemediation(exitcode.ConfigError(err),
		fmt.Sprintf("use --format %s or --format %s", output.FormatTable, output.FormatJSON))
}

// remoteNotConfiguredErr reports remoteName as missing or token-less, with a
// remediation hint that is the exact command to fix it. Extracted out of
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
			"create`, which opens a browser for OAuth. Idempotent: a remote that is\n"+
			"already configured with a valid token is left alone; a broken, token-less\n"+
			"remote left behind by an interrupted setup is deleted and recreated.\n"+
			"`better-drive account add` is the same command under its other name.",
		"  better-drive setup\n"+
			"  better-drive setup --remote gdrive-work")
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Start the sync daemon (all configured pairs) with a tray icon showing combined status",
		Long: "Start the continuous sync daemon: one sync loop per pair in config.toml,\n" +
			"each on its own interval/mode, plus a system-tray icon showing the\n" +
			"combined status. Blocks until the tray is quit. Every remote referenced\n" +
			"by a pair must already be set up (`better-drive setup`).",
		Example: "  better-drive run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			e := engine.New(config.ResolveRcloneConfig(cfg.RcloneConfig))
			for _, p := range cfg.Pairs {
				remoteName, _, _ := strings.Cut(p.Remote, ":")
				if configured, _ := e.RemoteConfigured(remoteName); !configured {
					e.Close()
					return remoteNotConfiguredErr(remoteName)
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
				logger.Printf("daemon started, %d pairs", len(cfg.Pairs))
			}

			// One syncloop per pair, each with its own mode/interval/filters
			// and its own workdir (bisync baselines must not collide across
			// pairs). agg.Register wires each loop's OnChange into the shared
			// aggregator so the tray shows one combined status.
			agg := tray.NewAggregator()
			loops := make([]*syncloop.Loop, len(cfg.Pairs))
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			for i, p := range cfg.Pairs {
				p := p
				loop := syncloop.New(e, p.Local, p.Remote, paths.PairWorkdir(p.Local, p.Remote), p.Mode,
					func() ([]string, error) { return config.PairFilters(p.Local, p.Exclude) })
				loops[i] = loop
				agg.Register(i, loop)
				if logger != nil {
					loop.OnResult(func(err error) {
						if err != nil {
							logger.Printf("%s <-> %s [mode=%s]: FAILED: %v", p.Local, p.Remote, p.Mode, err)
							return
						}
						logger.Printf("%s <-> %s [mode=%s]: OK", p.Local, p.Remote, p.Mode)
					})
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					loop.Start(ctx, p.Interval)
				}()
			}

			err = tray.Run(loops, cfg.Pairs, agg) // blocks on the systray event loop until Quit
			cancel()
			wg.Wait() // wait for every sync loop goroutine to finish its current cycle
			e.Close() // safe to Finalize the engine now that no goroutine can touch it
			if logFile != nil {
				logFile.Close()
			}
			// NOTE (v1 accepted edge case): a SyncNow-triggered run started via the tray
			// right before Quit races with cancel()/wg.Wait() above (SyncNow spawns its own
			// goroutine per loop, not tracked by `wg`), so a loop can still be mid-sync when
			// e.Close runs. Narrow window, no known data loss; revisit if it proves to matter.
			return err
		},
	}
}

func statusCmd() *cobra.Command {
	var format string
	c := &cobra.Command{
		Use:   "status",
		Short: "Print current config (every pair)",
		Long: "Print every pair from config.toml: local path, remote, interval and mode.\n" +
			"Read-only - makes no rclone call and never touches the network. Use\n" +
			"--format json for machine-readable output.",
		Example: "  better-drive status\n" +
			"  better-drive status --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if format == output.FormatJSON {
				pairs := make([]output.PairStatus, 0, len(cfg.Pairs))
				for _, p := range cfg.Pairs {
					pairs = append(pairs, output.PairStatus{Local: p.Local, Remote: p.Remote, Mode: p.Mode, Interval: p.Interval.String()})
				}
				return output.RenderJSON(cmd.OutOrStdout(), pairs)
			}
			for _, p := range cfg.Pairs {
				fmt.Fprintf(cmd.OutOrStdout(), "pair: %s <-> %s every %s [mode=%s]\n", p.Local, p.Remote, p.Interval, p.Mode)
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
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			e := engine.New(config.ResolveRcloneConfig(cfg.RcloneConfig))
			defer e.Close()
			_, err = runSyncOnce(cmd, e, cfg, format, dryRun, resync)
			return err
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
	// Whether any pair failed because rclone has no usable baseline for it,
	// which changes what the aggregate failure tells the user to do next.
	needsResync := false
	results := make([]output.PairResult, 0, len(cfg.Pairs))
	for _, p := range cfg.Pairs {
		p := p
		// Skip a pair whose local source does not exist (e.g. a machine that
		// doesn't have hermes), matching the backup script's Test-Path guard,
		// instead of failing the whole run on a missing optional source.
		if _, err := os.Stat(p.Local); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(cmd.ErrOrStderr(), "pair %s <-> %s [mode=%s]: SKIPPED (local not found)\n", p.Local, p.Remote, p.Mode)
			results = append(results, output.PairResult{Local: p.Local, Remote: p.Remote, Mode: p.Mode, Status: "skipped"})
			continue
		}
		loop := syncloop.New(s, p.Local, p.Remote, paths.PairWorkdir(p.Local, p.Remote), p.Mode,
			func() ([]string, error) { return config.PairFilters(p.Local, p.Exclude) })
		loop.SetDryRun(dryRun)
		loop.SetForceResync(forceResync)
		if err := loop.RunOnce(); err != nil {
			failed = true
			needsResync = needsResync || errors.Is(err, engine.ErrNeedsResync)
			fmt.Fprintf(cmd.ErrOrStderr(), "pair %s <-> %s [mode=%s]: FAILED: %v\n", p.Local, p.Remote, p.Mode, err)
			results = append(results, output.PairResult{Local: p.Local, Remote: p.Remote, Mode: p.Mode, Status: "failed", Error: err.Error(), DryRun: dryRun})
			continue
		}
		if format == output.FormatTable {
			fmt.Fprintf(cmd.OutOrStdout(), "pair %s <-> %s [mode=%s]: OK\n", p.Local, p.Remote, p.Mode)
		}
		results = append(results, output.PairResult{Local: p.Local, Remote: p.Remote, Mode: p.Mode, Status: "ok", DryRun: dryRun})
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
