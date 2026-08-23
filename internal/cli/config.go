package cli

import (
	"errors"
	"fmt"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/spf13/cobra"
)

func configCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "config",
		Short:   "Inspect and migrate better-drive configuration",
		Long:    "Inspect better-drive configuration without starting a transfer. Migration is dry-run only in Task 1.",
		Example: "  better-drive config migrate --dry-run --format json",
	}
	c.AddCommand(configMigrateCmd())
	return c
}

func configMigrateCmd() *cobra.Command {
	var format string
	var dryRun bool
	c := &cobra.Command{
		Use:     "migrate",
		Short:   "Preview deterministic v1 to v2 migration",
		Long:    "Read the current configuration, normalize legacy pairs into schema v2, and emit a redacted preview. No write path exists without an explicit future migration command.",
		Example: "  better-drive config migrate --dry-run --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dryRun {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("config migrate requires --dry-run")),
					"run: better-drive config migrate --dry-run --format json")
			}
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			cfg, err := config.Load(paths.ConfigFile())
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), fmt.Sprintf("fix or replace %s before migrating", paths.ConfigFile()))
			}
			if format != output.FormatJSON {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("config migrate supports only --format json")),
					"run: better-drive config migrate --dry-run --format json")
			}
			return output.RenderJSON(cmd.OutOrStdout(), config.Preview(cfg))
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().BoolVar(&dryRun, "dry-run", false, "preview migration without writing the config")
	return c
}
