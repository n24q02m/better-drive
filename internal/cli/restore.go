package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/n24q02m/better-drive/internal/artifactcrypto"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/restore"
	"github.com/spf13/cobra"
)

func restoreCmd() *cobra.Command {
	return restoreCmdWithResolver(nil)
}

func restoreCmdWithResolver(resolver artifactcrypto.Resolver) *cobra.Command {
	c := &cobra.Command{
		Use:     "restore",
		Short:   "Plan and stage safe isolated restores",
		Long:    "Plan is read-only. Fetch and apply write only below an explicit isolated root with create-only no-overwrite semantics. Live source replacement is disabled.",
		Example: "  better-drive restore plan --root C:/staging --manifest restore.json --format json",
	}
	c.AddCommand(restorePlanCmd(), restoreFetchCmd(resolver), restoreApplyCmd(resolver), restoreRecoverCmd())
	return c
}

func readRestorePlan(root, manifest string) (restore.Plan, error) {
	if root == "" || manifest == "" {
		return restore.Plan{}, errors.New("restore command requires --root and --manifest")
	}
	file, err := os.Open(manifest)
	if err != nil {
		return restore.Plan{}, err
	}
	defer file.Close()
	var entries []restore.Entry
	if err := json.NewDecoder(file).Decode(&entries); err != nil {
		return restore.Plan{}, fmt.Errorf("manifest must be a JSON array of restore entries: %w", err)
	}
	return restore.BuildPlan(root, entries)
}

func requireRestoreResolver(resolver artifactcrypto.Resolver) error {
	if resolver == nil {
		return exitcode.WithRemediation(
			exitcode.ConfigError(errors.New("restore execution requires an artifact resolver")),
			"configure an artifact key resolver before executing restore",
		)
	}
	return nil
}

func renderRestorePlan(cmd *cobra.Command, format string, plan restore.Plan) error {
	if format == output.FormatJSON {
		return output.RenderJSON(cmd.OutOrStdout(), plan)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "restore root: %s\nentries: %d\nconflicts: %d\n", plan.Root, len(plan.Entries), len(plan.Conflicts))
	return nil
}

func restorePlanCmd() *cobra.Command {
	var root string
	var manifest string
	var format string
	c := &cobra.Command{
		Use:     "plan",
		Short:   "Validate a restore manifest without writing",
		Long:    "Validate canonical relative paths, duplicate destinations, and existing conflicts without touching the restore root.",
		Example: "  better-drive restore plan --root C:/staging --manifest restore.json --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			plan, err := readRestorePlan(root, manifest)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "fix unsafe or duplicate restore paths before fetch")
			}
			return renderRestorePlan(cmd, format, plan)
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().StringVar(&root, "root", "", "isolated absolute restore root")
	c.Flags().StringVar(&manifest, "manifest", "", "JSON restore manifest")
	return c
}

func restoreFetchCmd(resolver artifactcrypto.Resolver) *cobra.Command {
	var root string
	var manifest string
	var format string
	var dryRun bool
	var execute bool
	c := &cobra.Command{
		Use:     "fetch",
		Short:   "Preview or stage a no-overwrite restore",
		Long:    "Dry-run is read-only. --execute writes only missing files below the explicit isolated root and journals each created entry.",
		Example: "  better-drive restore fetch --root C:/staging --manifest restore.json --dry-run --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dryRun == execute {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("restore fetch requires exactly one of --dry-run or --execute")), "choose --dry-run or --execute explicitly")
			}
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			plan, err := readRestorePlan(root, manifest)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "fix the restore manifest before fetch")
			}
			if dryRun {
				return renderRestorePlan(cmd, format, plan)
			}
			if len(plan.Conflicts) != 0 {
				return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("restore has %d existing destination conflicts", len(plan.Conflicts))), "remove conflicts or use a new isolated staging root")
			}
			if err := requireRestoreResolver(resolver); err != nil {
				return err
			}
			journal := restore.Journal{Path: filepath.Join(plan.Root, ".restore-apply.jsonl")}
			transactionID, err := restore.NewTransactionID()
			if err != nil {
				return err
			}
			runRecords := make([]restore.JournalRecord, 0, len(plan.Entries)*2)
			recoverFailure := func(cause error) error {
				if len(runRecords) == 0 {
					return cause
				}
				if recoveryErr := restore.RecoverCreateOnly(plan.Root, runRecords); recoveryErr != nil {
					return fmt.Errorf("%v; recover staged files: %w", cause, recoveryErr)
				}
				return cause
			}
			for _, entry := range plan.Entries {
				record := restore.JournalRecord{
					TransactionID:    transactionID,
					Entry:            entry.RelativePath,
					Action:           "create",
					Before:           "absent",
					After:            "staged",
					PlaintextDigest:  entry.PlaintextDigest,
					CiphertextDigest: entry.CiphertextDigest,
				}
				if err := journal.Append(record); err != nil {
					return recoverFailure(err)
				}
				runRecords = append(runRecords, record)
				if err := restore.StageFile(plan, entry, resolver); err != nil {
					return recoverFailure(err)
				}
				record.After = "created"
				if err := journal.Append(record); err != nil {
					return recoverFailure(err)
				}
				runRecords = append(runRecords, record)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "fetched %d entries into isolated root %s\n", len(plan.Entries), plan.Root)
			return nil
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().StringVar(&root, "root", "", "isolated absolute restore root")
	c.Flags().StringVar(&manifest, "manifest", "", "JSON restore manifest")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	c.Flags().BoolVar(&execute, "execute", false, "write only missing files below the isolated root")
	return c
}

func restoreApplyCmd(resolver artifactcrypto.Resolver) *cobra.Command {
	var root string
	var manifest string
	var transactionID string
	var format string
	var dryRun bool
	var execute bool
	var replace bool
	c := &cobra.Command{
		Use:     "apply",
		Short:   "Apply a create-only restore in an explicit isolated root",
		Long:    "Apply requires an absolute isolated root, a JSON manifest, and one transaction ID. It never replaces a destination or mutates a live source.",
		Example: "  better-drive restore apply --root C:/staging --manifest restore.json --transaction tx-1",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if replace {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("restore apply replace mode is disabled")), "use create-only apply in a new isolated root")
			}
			if root == "" || manifest == "" || transactionID == "" {
				if dryRun && root == "" && manifest == "" && transactionID == "" {
					fmt.Fprintln(cmd.OutOrStdout(), "dry-run: restore apply not executed")
					return nil
				}
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("restore apply requires --root, --manifest, and --transaction; live data-owner apply is disabled")), "provide an explicit isolated root, manifest, and transaction ID")
			}
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			plan, err := readRestorePlan(root, manifest)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "fix the restore manifest before apply")
			}
			if dryRun {
				return renderRestorePlan(cmd, format, plan)
			}
			if len(plan.Conflicts) != 0 {
				return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("restore has %d existing destination conflicts", len(plan.Conflicts))), "remove conflicts or use a new isolated staging root")
			}
			if err := requireRestoreResolver(resolver); err != nil {
				return err
			}
			tx, err := restore.BeginTransaction(plan.Root, transactionID)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "choose a new transaction ID and preserve prior transaction evidence")
			}
			if err := applyRestorePlan(plan, tx, resolver); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "applied %d entries into isolated root %s (transaction %s)\n", len(plan.Entries), plan.Root, transactionID)
			_ = execute
			return nil
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().StringVar(&root, "root", "", "isolated absolute restore root")
	c.Flags().StringVar(&manifest, "manifest", "", "JSON restore manifest")
	c.Flags().StringVar(&transactionID, "transaction", "", "explicit transaction ID")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "validate and preview without writing")
	c.Flags().BoolVar(&execute, "execute", false, "accepted for explicit create-only apply")
	c.Flags().BoolVar(&replace, "replace", false, "rejected: live/overwrite replacement is disabled")
	return c
}

func applyRestorePlan(plan restore.Plan, tx *restore.Transaction, resolver artifactcrypto.Resolver) error {
	runRecords := make([]restore.JournalRecord, 0, len(plan.Entries)*2)
	recoverFailure := func(cause error) error {
		if len(runRecords) == 0 {
			return cause
		}
		if recoveryErr := restore.RecoverCreateOnly(plan.Root, runRecords); recoveryErr != nil {
			return fmt.Errorf("%v; recover staged files: %w", cause, recoveryErr)
		}
		return cause
	}
	for _, entry := range plan.Entries {
		record := restore.JournalRecord{
			TransactionID:    tx.ID,
			Entry:            entry.RelativePath,
			Action:           "create",
			Before:           "absent",
			After:            "staged",
			PlaintextDigest:  entry.PlaintextDigest,
			CiphertextDigest: entry.CiphertextDigest,
			Root:             plan.Root,
			RootIdentity:     tx.RootIdentity.Token,
		}
		if err := tx.Append(record); err != nil {
			return recoverFailure(err)
		}
		runRecords = append(runRecords, record)
		if err := restore.StageFile(plan, entry, resolver); err != nil {
			return recoverFailure(err)
		}
		record.After = "created"
		if err := tx.Append(record); err != nil {
			return recoverFailure(err)
		}
		runRecords = append(runRecords, record)
	}
	return nil
}

func restoreRecoverCmd() *cobra.Command {
	var root string
	var transactionID string
	var format string
	var live bool
	c := &cobra.Command{
		Use:     "recover",
		Short:   "Recover exactly one interrupted isolated restore transaction",
		Long:    "Recover removes only matching create-only files from the named transaction. Live source replacement and cross-root deletion are disabled.",
		Example: "  better-drive restore recover --root C:/staging --transaction tx-1",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if live {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("restore recover live mode is disabled")), "name one isolated root and transaction")
			}
			if root == "" || transactionID == "" {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("restore recover requires --root and --transaction")), "provide the exact isolated root and transaction ID")
			}
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			if err := restore.RecoverTransaction(root, transactionID); err != nil {
				return err
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), map[string]string{"root": root, "transaction": transactionID, "status": "recovered"})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recovered transaction %s in isolated root %s\n", transactionID, root)
			return nil
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().StringVar(&root, "root", "", "isolated absolute restore root")
	c.Flags().StringVar(&transactionID, "transaction", "", "exact transaction ID to recover")
	c.Flags().BoolVar(&live, "live", false, "rejected: live-source recovery is disabled")
	return c
}
