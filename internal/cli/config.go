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
		Long:    "Inspect better-drive configuration without starting a transfer. Migration is dry-run by default; an explicit create-only operation requires complete caller-supplied bindings and never overwrites a target.",
		Example: "  better-drive config migrate --dry-run --format json\n  better-drive config migrate --create-only --output migrated.toml --account-id <id> --root-id <id> --role-ref <ref> --role-digest sha256:<digest> --policy-ref <ref> --policy-digest sha256:<digest>",
	}
	c.AddCommand(configMigrateCmd())
	return c
}

func configMigrateCmd() *cobra.Command {
	var format string
	var dryRun bool
	var createOnly bool
	var outputPath string
	var accountID string
	var rootID string
	var roleRef string
	var roleDigest string
	var policyRef string
	var policyDigest string
	c := &cobra.Command{
		Use:     "migrate",
		Short:   "Preview or explicitly create a schema-v2 migration",
		Long:    "Read the current configuration and normalize legacy pairs into schema v2. Dry-run emits a redacted preview; create-only writes a new target only with complete explicit bindings.",
		Example: "  better-drive config migrate --dry-run --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if createOnly && dryRun {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("config migrate cannot combine --create-only and --dry-run")),
					"omit --dry-run when writing a new target")
			}
			if !createOnly && !dryRun {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("config migrate requires --dry-run or --create-only")),
					"run: better-drive config migrate --dry-run --format json")
			}
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			cfg, err := config.Load(paths.ConfigFile())
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), fmt.Sprintf("fix or replace %s before migrating", paths.ConfigFile()))
			}
			if createOnly {
				if outputPath == "" {
					return exitcode.WithRemediation(exitcode.ConfigError(errors.New("config migrate --create-only requires --output")),
						"provide a new target path with --output")
				}
				bindings := config.MigrationBindings{AccountID: accountID, RootID: rootID, RoleRef: roleRef, RoleDigest: roleDigest, PolicyRef: policyRef, PolicyDigest: policyDigest}
				migrated, err := config.ApplyMigrationBindings(cfg, bindings)
				if err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(err), "supply complete explicit account/root/role/policy bindings")
				}
				if err := config.WriteCanonicalV2CreateOnly(outputPath, migrated); err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(err), "choose a new target and verify explicit bindings")
				}
				return nil
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
	c.Flags().BoolVar(&createOnly, "create-only", false, "create a new canonical target and refuse existing targets")
	c.Flags().StringVar(&outputPath, "output", "", "new canonical target path for --create-only")
	c.Flags().StringVar(&accountID, "account-id", "", "explicit provider account identity")
	c.Flags().StringVar(&rootID, "root-id", "", "explicit provider root identity")
	c.Flags().StringVar(&roleRef, "role-ref", "", "explicit role/profile reference")
	c.Flags().StringVar(&roleDigest, "role-digest", "", "explicit role/profile digest")
	c.Flags().StringVar(&policyRef, "policy-ref", "", "explicit policy reference")
	c.Flags().StringVar(&policyDigest, "policy-digest", "", "explicit policy digest")
	return c
}
