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

func Execute() error { return newRootCmd().Execute() }

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "better-drive",
		Short:   "Google Drive sync (bisync/copy/sync modes) with .driveignore + config excludes, multi-pair",
		Version: version.Version,
	}
	root.AddCommand(setupCmd(), runCmd(), statusCmd(), syncCmd(), installCmd(), uninstallCmd())
	root.InitDefaultCompletionCmd()
	return root
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

func setupCmd() *cobra.Command {
	var remote string
	c := &cobra.Command{
		Use:   "setup",
		Short: "Create the rclone Google Drive remote (opens browser for OAuth)",
		Long: "Create (or repair) an rclone Google Drive remote via `rclone config\n" +
			"create`, which opens a browser for OAuth. Idempotent: a remote that is\n" +
			"already configured with a valid token is left alone; a broken, token-less\n" +
			"remote left behind by an interrupted setup is deleted and recreated.",
		Example: "  better-drive setup\n" +
			"  better-drive setup --remote gdrive-work",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// setup can run before a config.toml exists yet (first-run before any
			// [[pair]] is defined), so a missing/unloadable config is not fatal
			// here - fall back to "" (auto-detect) rather than cfg.RcloneConfig.
			rcloneConfigPath := ""
			if cfg, err := config.Load(paths.ConfigFile()); err == nil {
				rcloneConfigPath = cfg.RcloneConfig
			}
			e := engine.New(config.ResolveRcloneConfig(rcloneConfigPath))
			defer e.Close()
			// RemoteConfigured (not RemoteExists) gates the skip: config/create writes
			// the remote's config stanza to disk BEFORE OAuth completes, so an
			// interrupted `setup` leaves behind a remote that "exists" by name but has
			// no token. Treat that as broken and self-heal instead of silently skipping.
			configured, _ := e.RemoteConfigured(remote)
			if configured {
				fmt.Fprintf(cmd.OutOrStdout(), "remote %q already set up\n", remote)
				return nil
			}
			if exists, _ := e.RemoteExists(remote); exists {
				_ = e.DeleteRemote(remote) // clear broken, token-less stanza before recreating
			}
			// PLAN-TIME VERIFY (spec §3): config/create với backend drive tự mở browser OAuth
			// qua librclone in-process. Nếu librclone không trigger browser → fallback delegate
			// `rclone authorize "drive"` rồi truyền token. Xác nhận lúc impl step này.
			if err := e.CreateDriveRemote(remote, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "remote %q created\n", remote)
			return nil
		},
	}
	c.Flags().StringVar(&remote, "remote", "gdrive", "rclone remote name to create")
	return c
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
			cfg, err := config.Load(paths.ConfigFile())
			if err != nil {
				return exitcode.ConfigError(err)
			}
			if err := cfg.Validate(); err != nil {
				return exitcode.ConfigError(err)
			}

			e := engine.New(config.ResolveRcloneConfig(cfg.RcloneConfig))
			for _, p := range cfg.Pairs {
				remoteName, _, _ := strings.Cut(p.Remote, ":")
				if configured, _ := e.RemoteConfigured(remoteName); !configured {
					e.Close()
					return exitcode.RemoteNotConfiguredError(fmt.Errorf("remote %q is not set up; run: better-drive setup", remoteName))
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
				loop := syncloop.New(e, p.Local, p.Remote, paths.PairWorkdir(i), p.Mode,
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
				return exitcode.ConfigError(err)
			}
			cfg, err := config.Load(paths.ConfigFile())
			if err != nil {
				return exitcode.ConfigError(err)
			}
			if err := cfg.Validate(); err != nil {
				return exitcode.ConfigError(err)
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
	c := &cobra.Command{
		Use:   "sync",
		Short: "Run exactly one sync cycle for every configured pair, then exit (for a scheduled task)",
		Long: "Run a single sync cycle for every pair in config.toml, then exit - no tray,\n" +
			"no ticker. A pair whose local path does not exist is SKIPPED (not a\n" +
			"failure). Successful pairs are reported on stdout; SKIPPED and FAILED\n" +
			"pairs go to stderr. Use --format json for machine-readable output and\n" +
			"--dry-run to preview changes without applying them.",
		Example: "  better-drive sync\n" +
			"  better-drive sync --dry-run\n" +
			"  better-drive sync --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return exitcode.ConfigError(err)
			}
			cfg, err := config.Load(paths.ConfigFile())
			if err != nil {
				return exitcode.ConfigError(err)
			}
			if err := cfg.Validate(); err != nil {
				return exitcode.ConfigError(err)
			}

			e := engine.New(config.ResolveRcloneConfig(cfg.RcloneConfig))
			defer e.Close()
			_, err = runSyncOnce(cmd, e, cfg, format, dryRun)
			return err
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without modifying anything")
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
// engine.Engine, which would make a real Drive rc call.
func runSyncOnce(cmd *cobra.Command, s syncloop.Syncer, cfg *config.Config, format string, dryRun bool) ([]output.PairResult, error) {
	if dryRun {
		fmt.Fprintln(cmd.ErrOrStderr(), "dry-run: no changes will be made")
	}
	failed := false
	results := make([]output.PairResult, 0, len(cfg.Pairs))
	for i, p := range cfg.Pairs {
		p := p
		// Skip a pair whose local source does not exist (e.g. a machine that
		// doesn't have hermes), matching the backup script's Test-Path guard,
		// instead of failing the whole run on a missing optional source.
		if _, err := os.Stat(p.Local); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(cmd.ErrOrStderr(), "pair %s <-> %s [mode=%s]: SKIPPED (local not found)\n", p.Local, p.Remote, p.Mode)
			results = append(results, output.PairResult{Local: p.Local, Remote: p.Remote, Mode: p.Mode, Status: "skipped"})
			continue
		}
		loop := syncloop.New(s, p.Local, p.Remote, paths.PairWorkdir(i), p.Mode,
			func() ([]string, error) { return config.PairFilters(p.Local, p.Exclude) })
		loop.SetDryRun(dryRun)
		if err := loop.RunOnce(); err != nil {
			failed = true
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
		return results, exitcode.SyncFailed(fmt.Errorf("sync: one or more pairs failed"))
	}
	return results, nil
}
