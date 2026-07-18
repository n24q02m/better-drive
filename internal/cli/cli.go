package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/n24q02m/better-drive/internal/autostart"
	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
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
	return root
}

func installCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Register better-drive to start at login (hidden tray daemon)",
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(paths.ConfigFile())
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}

			e := engine.New(config.ResolveRcloneConfig(cfg.RcloneConfig))
			for _, p := range cfg.Pairs {
				remoteName, _, _ := strings.Cut(p.Remote, ":")
				if configured, _ := e.RemoteConfigured(remoteName); !configured {
					e.Close()
					return fmt.Errorf("remote %q is not set up; run: better-drive setup", remoteName)
				}
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
			// NOTE (v1 accepted edge case): a SyncNow-triggered run started via the tray
			// right before Quit races with cancel()/wg.Wait() above (SyncNow spawns its own
			// goroutine per loop, not tracked by `wg`), so a loop can still be mid-sync when
			// e.Close runs. Narrow window, no known data loss; revisit if it proves to matter.
			return err
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print current config (every pair)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(paths.ConfigFile())
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			for _, p := range cfg.Pairs {
				fmt.Fprintf(cmd.OutOrStdout(), "pair: %s <-> %s every %s [mode=%s]\n", p.Local, p.Remote, p.Interval, p.Mode)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "run `better-drive run` to start")
			return nil
		},
	}
}

// syncCmd runs exactly one sync cycle for every configured pair, then exits -
// no tray, no ticker. It is meant to be invoked by an external scheduler (a
// Windows Scheduled Task) in place of a one-shot backup script: same config,
// same per-pair mode/filters/workdir as `run`, but a single pass instead of a
// continuous daemon.
func syncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Run exactly one sync cycle for every configured pair, then exit (for a scheduled task)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(paths.ConfigFile())
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}

			e := engine.New(config.ResolveRcloneConfig(cfg.RcloneConfig))
			defer e.Close()
			return runSyncOnce(cmd, e, cfg)
		},
	}
}

// runSyncOnce builds one syncloop.Loop per configured pair (same workdir
// convention as runCmd, so a bisync-mode pair's baseline is shared with the
// `run` daemon) and runs exactly one RunOnce cycle on each, printing a
// per-pair result line. It returns a non-nil error if any pair failed. The
// Syncer is a parameter (rather than constructed here) so tests can inject a
// fake instead of a real engine.Engine, which would make a real Drive rc call.
func runSyncOnce(cmd *cobra.Command, s syncloop.Syncer, cfg *config.Config) error {
	failed := false
	for i, p := range cfg.Pairs {
		p := p
		// Skip a pair whose local source does not exist (e.g. a machine that
		// doesn't have hermes), matching the backup script's Test-Path guard,
		// instead of failing the whole run on a missing optional source.
		if _, err := os.Stat(p.Local); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(cmd.OutOrStdout(), "pair %s <-> %s [mode=%s]: SKIPPED (local not found)\n", p.Local, p.Remote, p.Mode)
			continue
		}
		loop := syncloop.New(s, p.Local, p.Remote, paths.PairWorkdir(i), p.Mode,
			func() ([]string, error) { return config.PairFilters(p.Local, p.Exclude) })
		if err := loop.RunOnce(); err != nil {
			failed = true
			fmt.Fprintf(cmd.OutOrStdout(), "pair %s <-> %s [mode=%s]: FAILED: %v\n", p.Local, p.Remote, p.Mode, err)
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "pair %s <-> %s [mode=%s]: OK\n", p.Local, p.Remote, p.Mode)
	}
	if failed {
		return fmt.Errorf("sync: one or more pairs failed")
	}
	return nil
}
