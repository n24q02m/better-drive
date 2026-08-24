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
		Example: "  better-drive config migrate --dry-run --format json",
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
	var runtimeExecutable string
	var runtimeExecutableFileID string
	var runtimeExecutableDigest string
	var runtimeVersion string
	var runtimeProvenance string
	var runtimeSignature string
	var runtimeOwner string
	var runtimeACL string
	var runtimeConfig string
	var runtimeConfigFileID string
	var runtimeConfigDigest string
	var runtimeAllowedRemotes []string
	var runtimeAllowedBackends []string
	var categoryPolicyID string
	var categoryPolicyVersion int
	var categoryPolicyDigest string
	var categoryPolicyRoot string
	var categoryPolicyDeny []string
	var categoryPolicyMaxBytes int64
	var categoryPolicyRestoreExpectation string
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
				bindings := config.MigrationBindings{
					AccountID: accountID, RootID: rootID,
					RoleRef: roleRef, RoleDigest: roleDigest,
					PolicyRef: policyRef, PolicyDigest: policyDigest,
					RcloneRuntime: config.RcloneRuntime{
						Executable: runtimeExecutable, ExecutableFileID: runtimeExecutableFileID,
						ExecutableDigest: runtimeExecutableDigest, Version: runtimeVersion,
						Provenance: runtimeProvenance, Signature: runtimeSignature,
						Owner: runtimeOwner, ACL: runtimeACL, Config: runtimeConfig,
						ConfigFileID: runtimeConfigFileID, ConfigDigest: runtimeConfigDigest,
						AllowedRemotes: runtimeAllowedRemotes, AllowedBackends: runtimeAllowedBackends,
					},
					CategoryPolicy: config.CategoryPolicy{
						ID: categoryPolicyID, Version: categoryPolicyVersion, Digest: categoryPolicyDigest,
						AllowlistedRoot: categoryPolicyRoot, MandatoryDenylist: categoryPolicyDeny,
						SizeGuard:          config.CategorySizeGuard{MaxBytes: categoryPolicyMaxBytes},
						RestoreExpectation: categoryPolicyRestoreExpectation,
					},
				}
				migrated, err := config.ApplyMigrationBindings(cfg, bindings)
				if err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(err), "supply complete explicit account/root/role/runtime/category-policy bindings")
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
	c.Flags().StringVar(&runtimeExecutable, "runtime-executable", "", "explicit absolute rclone executable path")
	c.Flags().StringVar(&runtimeExecutableFileID, "runtime-executable-file-id", "", "explicit rclone executable file identity")
	c.Flags().StringVar(&runtimeExecutableDigest, "runtime-executable-digest", "", "explicit rclone executable digest")
	c.Flags().StringVar(&runtimeVersion, "runtime-version", "", "explicit rclone version")
	c.Flags().StringVar(&runtimeProvenance, "runtime-provenance", "", "explicit rclone provenance")
	c.Flags().StringVar(&runtimeSignature, "runtime-signature", "", "explicit rclone signature")
	c.Flags().StringVar(&runtimeOwner, "runtime-owner", "", "explicit rclone owner")
	c.Flags().StringVar(&runtimeACL, "runtime-acl", "", "explicit rclone ACL")
	c.Flags().StringVar(&runtimeConfig, "runtime-config", "", "explicit absolute rclone config path")
	c.Flags().StringVar(&runtimeConfigFileID, "runtime-config-file-id", "", "explicit rclone config file identity")
	c.Flags().StringVar(&runtimeConfigDigest, "runtime-config-digest", "", "explicit rclone config digest")
	c.Flags().StringArrayVar(&runtimeAllowedRemotes, "runtime-allowed-remote", nil, "explicit allowed rclone remote (repeatable)")
	c.Flags().StringArrayVar(&runtimeAllowedBackends, "runtime-allowed-backend", nil, "explicit allowed destination backend (repeatable)")
	c.Flags().StringVar(&categoryPolicyID, "category-policy-id", "", "explicit category policy identity")
	c.Flags().IntVar(&categoryPolicyVersion, "category-policy-version", 0, "explicit category policy version")
	c.Flags().StringVar(&categoryPolicyDigest, "category-policy-digest", "", "explicit category policy digest")
	c.Flags().StringVar(&categoryPolicyRoot, "category-policy-root", "", "explicit category policy allowlisted root")
	c.Flags().StringArrayVar(&categoryPolicyDeny, "category-policy-deny", nil, "explicit mandatory category-policy denylist entry (repeatable)")
	c.Flags().Int64Var(&categoryPolicyMaxBytes, "category-policy-max-bytes", 0, "explicit category policy maximum source bytes")
	c.Flags().StringVar(&categoryPolicyRestoreExpectation, "category-policy-restore-expectation", "", "explicit category policy restore expectation")
	return c
}
