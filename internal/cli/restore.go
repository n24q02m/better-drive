package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/restore"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
)

func restoreCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "restore",
		Short:   "Plan and stage safe isolated restores",
		Long:    "Restore plan is read-only. Fetch writes only to the explicit staging root with no-overwrite semantics. Apply remains owner-gated.",
		Example: "  better-drive restore plan --root C:/staging --manifest restore.json --format json",
	}
	c.AddCommand(restorePlanCmd(), restoreFetchCmd(), restoreApplyCmd())
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

func restoreFetchCmd() *cobra.Command {
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
			journal := restore.Journal{Path: filepath.Join(plan.Root, ".restore-apply.jsonl")}
			transactionID, err := restore.NewTransactionID()
			if err != nil {
				return err
			}
			runRecords := make([]restore.JournalRecord, 0, len(plan.Entries)*2)
			recoverFailure := func(cause error) error {
				if recoveryErr := restore.RecoverCreateOnly(plan.Root, runRecords); recoveryErr != nil {
					return fmt.Errorf("%v; recover staged files: %w", cause, recoveryErr)
				}
				return cause
			}
			for _, entry := range plan.Entries {
				record := restore.JournalRecord{
					TransactionID: transactionID,
					Entry:         entry.RelativePath, Action: "create", Before: "absent", After: "staged",
					SourceDigest: entry.SourceDigest,
				}
				if err := journal.Append(record); err != nil {
					return recoverFailure(err)
				}
				runRecords = append(runRecords, record)
				if err := restore.StageFile(plan, entry); err != nil {
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

func restoreApplyCmd() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:     "apply",
		Short:   "Keep live restore apply owner-gated",
		Long:    "Live create or replace is a separate data-owner gate. This command currently exposes only an explicit dry-run block and never mutates a live source.",
		Example: "  better-drive restore apply --dry-run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dryRun {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("restore apply requires a named data-owner gate; live mutation is disabled")), "run restore plan/fetch in an isolated root, then obtain the owner gate")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "dry-run: restore apply not executed")
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "show the live-apply gate without mutating")
	return c
}
