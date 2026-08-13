package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/spf13/cobra"
)

type mountEngine interface {
	RemoteConfigured(name string) (bool, error)
	Mount(context.Context, engine.MountParams) error
	Close()
}

type mountEngineFactory func(rcloneConfig string) mountEngine

func mountCmd() *cobra.Command {
	return mountCmdWithFactory(func(rcloneConfig string) mountEngine {
		return engine.New(rcloneConfig)
	})
}

func mountCmdWithFactory(factory mountEngineFactory) *cobra.Command {
	var readOnly bool
	c := &cobra.Command{
		Use:   "mount <remote:path> <mountpoint>",
		Short: "Mount a Drive remote as a foreground virtual drive",
		Long: "Mount an rclone Drive remote as a foreground virtual filesystem.\n" +
			"The mount uses VFS full-cache mode for application compatibility and stays\n" +
			"attached until interrupted with Ctrl+C. It does not require any [[pair]]\n" +
			"entries in better-drive's config.toml. Windows requires WinFsp; Linux\n" +
			"requires FUSE and access to /dev/fuse; macOS requires a mount backend\n" +
			"supported by rclone.",
		Example: "  better-drive mount gdrive: G:\n" +
			"  better-drive mount gdrive:Documents * --read-only",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 2 {
				return exitcode.WithRemediation(
					exitcode.ConfigError(fmt.Errorf("mount requires exactly 2 arguments: <remote:path> <mountpoint>")),
					"use: better-drive mount <remote:path> <mountpoint>",
				)
			}
			if _, err := mountRemoteName(args[0]); err != nil {
				return err
			}
			if strings.TrimSpace(args[1]) == "" {
				return exitcode.WithRemediation(
					exitcode.ConfigError(errors.New("mountpoint must not be empty")),
					"use a drive letter such as G: on Windows or a directory on Linux/macOS",
				)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			remoteName, _ := mountRemoteName(args[0])
			configPath := paths.ConfigFile()
			rcloneConfig, err := config.LoadRcloneConfigOnly(configPath)
			if err != nil {
				return exitcode.WithRemediation(
					exitcode.ConfigError(err),
					fmt.Sprintf("create or fix %s (TOML syntax)", configPath),
				)
			}

			e := factory(config.ResolveRcloneConfig(rcloneConfig))
			defer e.Close()
			configured, err := e.RemoteConfigured(remoteName)
			if err != nil {
				return fmt.Errorf("check remote %q: %w", remoteName, err)
			}
			if !configured {
				return remoteNotConfiguredErr(remoteName)
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			err = e.Mount(ctx, engine.MountParams{
				Remote:     args[0],
				Mountpoint: args[1],
				ReadOnly:   readOnly,
				Stdout:     cmd.OutOrStdout(),
				Stderr:     cmd.ErrOrStderr(),
			})
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return mountDriverError(runtime.GOOS, err)
		},
	}
	c.Flags().BoolVar(&readOnly, "read-only", false, "prevent writes through the mounted filesystem")
	return c
}

func mountRemoteName(remotePath string) (string, error) {
	name, _, found := strings.Cut(remotePath, ":")
	if found && strings.TrimSpace(name) != "" && !strings.ContainsAny(name, `/\\`) {
		return name, nil
	}
	err := fmt.Errorf("invalid remote path %q: expected <remote>:<path>", remotePath)
	return "", exitcode.WithRemediation(exitcode.ConfigError(err),
		"use a configured rclone remote, for example: gdrive:Documents")
}

func mountDriverError(goos string, err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	var hint string
	switch {
	case goos == "windows" && (strings.Contains(message, "winfsp") || strings.Contains(message, "cgofuse")):
		hint = "install WinFsp, then retry: scoop install winfsp"
	case goos == "linux" && (strings.Contains(message, "fuse") || strings.Contains(message, "fusermount")):
		hint = "install FUSE 3 and ensure /dev/fuse is available, then retry"
	case goos == "darwin" && strings.Contains(message, "fuse"):
		hint = "install or enable a supported rclone mount backend (NFS or macFUSE), then retry"
	default:
		return err
	}
	return exitcode.WithRemediation(fmt.Errorf("%w; %s", err, hint), hint)
}
