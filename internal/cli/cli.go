package cli

import (
	"context"
	"fmt"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/n24q02m/better-drive/internal/syncloop"
	"github.com/n24q02m/better-drive/internal/tray"
	"github.com/spf13/cobra"
)

func Execute() error { return newRootCmd().Execute() }

func newRootCmd() *cobra.Command {
	root := &cobra.Command{Use: "better-drive", Short: "2-way Google Drive sync with .driveignore"}
	root.AddCommand(setupCmd(), runCmd(), statusCmd())
	return root
}

func setupCmd() *cobra.Command {
	var remote string
	c := &cobra.Command{
		Use:   "setup",
		Short: "Create the rclone Google Drive remote (opens browser for OAuth)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := engine.New()
			defer e.Close()
			ok, err := e.RemoteExists(remote)
			if err != nil {
				return err
			}
			if ok {
				fmt.Fprintf(cmd.OutOrStdout(), "remote %q already exists\n", remote)
				return nil
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
		Short: "Start the sync daemon with tray icon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(paths.ConfigFile())
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			p := cfg.Pairs[0]
			e := engine.New()
			defer e.Close()
			loop := syncloop.New(e, p.Local, p.Remote, paths.Workdir(),
				func() ([]string, error) { return config.TranslateDriveIgnore(p.Local) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go loop.Start(ctx, p.Interval)
			return tray.Run(loop, p) // block trên systray event loop
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print current config",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(paths.ConfigFile())
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			p := cfg.Pairs[0]
			fmt.Fprintf(cmd.OutOrStdout(), "pair: %s <-> %s every %s\nrun `better-drive run` to start\n",
				p.Local, p.Remote, p.Interval)
			return nil
		},
	}
}
