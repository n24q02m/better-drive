package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/artifactcrypto"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/restore"
	"github.com/spf13/cobra"
)

// RestoreDependencies packages typed services for restore command execution.
type RestoreDependencies struct {
	ArtifactResolver   artifactcrypto.Resolver
	StagingVerifier    restore.StagingVerifier
	SourceProvider     restore.SourceProvider
	CheckpointVerifier restore.CheckpointVerifier
	CleanupVerifier    restore.CleanupClaimVerifier
}

func restoreCmd() *cobra.Command {
	return restoreCmdWithDependencies(RuntimeDependencies{})
}

func restoreCmdWithDependencies(deps RuntimeDependencies) *cobra.Command {
	return restoreCmdWithRestoreDependencies(RestoreDependencies{
		ArtifactResolver: deps.ArtifactResolver,
		StagingVerifier:  deps.StagingVerifier,
	})
}

func restoreCmdWithRestoreDependencies(deps RestoreDependencies) *cobra.Command {
	c := &cobra.Command{
		Use:     "restore",
		Short:   "Plan and stage safe isolated restores",
		Long:    "Plan is read-only. Fetch and apply write only below an explicit isolated root with create-only no-overwrite defaults. Live replace requires an explicit signed machine checkpoint.",
		Example: "  better-drive restore plan --root C:/staging --manifest restore.json --format json",
	}
	c.AddCommand(
		restorePlanCmd(),
		restoreFetchCmd(deps.ArtifactResolver, deps.StagingVerifier, deps.SourceProvider),
		restoreApplyCmd(deps.ArtifactResolver, deps.StagingVerifier, deps.SourceProvider, deps.CheckpointVerifier),
		restoreRecoverCmd(),
		restoreCleanupCmd(deps.CleanupVerifier),
	)
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

func requireRestoreDependencies(resolver artifactcrypto.Resolver, verifier restore.StagingVerifier) error {
	missing := make([]string, 0, 2)
	if resolver == nil {
		missing = append(missing, "artifact resolver")
	}
	if verifier == nil {
		missing = append(missing, "staging verifier")
	}
	if len(missing) == 0 {
		return nil
	}
	return exitcode.WithRemediation(
		exitcode.ConfigError(fmt.Errorf("restore execution requires %s", strings.Join(missing, " and "))),
		"configure an artifact resolver and staging verifier before executing restore",
	)
}

func preflightRestore(plan restore.Plan, resolver artifactcrypto.Resolver, verifier restore.StagingVerifier) error {
	if err := requireRestoreDependencies(resolver, verifier); err != nil {
		return err
	}
	if _, err := restore.VerifyStagingEvidence(plan.Root, plan.RootIdentity, verifier); err != nil {
		return exitcode.WithRemediation(
			exitcode.ConfigError(err),
			"verify encrypted-at-rest, owner-only, non-inherited ACL, and backup-excluded staging evidence before executing restore",
		)
	}
	return nil
}

func renderRestorePlan(cmd *cobra.Command, format string, plan restore.Plan) error {
	if format == output.FormatJSON {
		return output.RenderJSON(cmd.OutOrStdout(), plan)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "restore root: %s\nentries: %d\nconflicts: %d\ncapacity bytes: %d\n", plan.Root, len(plan.Entries), len(plan.Conflicts), plan.CapacityBytes)
	return nil
}

func restorePlanCmd() *cobra.Command {
	var root string
	var manifest string
	var format string
	c := &cobra.Command{
		Use:     "plan",
		Short:   "Validate a restore manifest without writing",
		Long:    "Validate canonical relative paths, duplicate destinations, capacity, and existing conflicts without touching the restore root.",
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

func restoreFetchCmd(resolver artifactcrypto.Resolver, verifier restore.StagingVerifier, provider restore.SourceProvider) *cobra.Command {
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
			if err := preflightRestore(plan, resolver, verifier); err != nil {
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
				if err := restore.StageFileWithProvider(context.Background(), plan, entry, resolver, verifier, provider); err != nil {
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

func restoreApplyCmd(resolver artifactcrypto.Resolver, verifier restore.StagingVerifier, provider restore.SourceProvider, checkpointVerifier restore.CheckpointVerifier) *cobra.Command {
	var root string
	var manifest string
	var transactionID string
	var checkpointPath string
	var format string
	var dryRun bool
	var execute bool
	var replace bool
	c := &cobra.Command{
		Use:     "apply",
		Short:   "Apply a create or replace restore in an explicit isolated root",
		Long:    "Apply requires an absolute isolated root, a JSON manifest, and one transaction ID. Replace requires a signed machine checkpoint.",
		Example: "  better-drive restore apply --root C:/staging --manifest restore.json --transaction tx-1",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if replace && checkpointPath == "" {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("restore apply replace mode is disabled without a signed machine checkpoint")), "supply a verified --checkpoint or use create-only apply in a new isolated root")
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
			if !replace && len(plan.Conflicts) != 0 {
				return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("restore has %d existing destination conflicts", len(plan.Conflicts))), "remove conflicts or use a new isolated staging root")
			}
			if err := preflightRestore(plan, resolver, verifier); err != nil {
				return err
			}

			// If checkpoint is provided, verify it.
			if checkpointPath != "" {
				cpData, err := os.ReadFile(checkpointPath)
				if err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("read checkpoint: %w", err)), "provide a valid checkpoint file")
				}
				var cp restore.MachineCheckpoint
				if err := json.Unmarshal(cpData, &cp); err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("parse checkpoint: %w", err)), "provide a valid JSON checkpoint")
				}
				intent := restore.ApplyIntent{
					Plan:          plan,
					CapacityBytes: plan.CapacityBytes,
					TotalObjects:  len(plan.Entries),
				}
				if err := restore.VerifyCheckpoint(context.Background(), cp, intent, checkpointVerifier); err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(err), "checkpoint verification failed")
				}
			}

			tx, err := restore.BeginTransaction(plan.Root, transactionID)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "choose a new transaction ID and preserve prior transaction evidence")
			}
			if replace {
				if err := applyRestorePlanWithReplace(plan, tx, resolver, verifier, provider); err != nil {
					return err
				}
			} else {
				if err := applyRestorePlan(plan, tx, resolver, verifier, provider); err != nil {
					return err
				}
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
	c.Flags().StringVar(&checkpointPath, "checkpoint", "", "path to signed machine checkpoint")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "validate and preview without writing")
	c.Flags().BoolVar(&execute, "execute", false, "accepted for explicit create-only apply")
	c.Flags().BoolVar(&replace, "replace", false, "replace existing destinations with machine checkpoint")
	return c
}

func applyRestorePlan(plan restore.Plan, tx *restore.Transaction, resolver artifactcrypto.Resolver, verifier restore.StagingVerifier, provider restore.SourceProvider) error {
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
		if err := restore.StageFileWithProvider(context.Background(), plan, entry, resolver, verifier, provider); err != nil {
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

func applyRestorePlanWithReplace(plan restore.Plan, tx *restore.Transaction, resolver artifactcrypto.Resolver, verifier restore.StagingVerifier, provider restore.SourceProvider) error {
	rollbackDir := filepath.Join(plan.Root, ".restore-rollback", tx.ID)
	runRecords := make([]restore.JournalRecord, 0, len(plan.Entries)*2)
	recoverFailure := func(cause error) error {
		if len(runRecords) == 0 {
			return cause
		}
		if recoveryErr := restore.RecoverWithIdentity(plan.Root, tx.RootIdentity, runRecords); recoveryErr != nil {
			return fmt.Errorf("%v; recover files: %w", cause, recoveryErr)
		}
		return cause
	}
	for _, entry := range plan.Entries {
		clean := entry.RelativePath
		dest := filepath.Join(plan.Root, filepath.FromSlash(clean))
		destInfo, statErr := os.Lstat(dest)
		isReplace := statErr == nil && destInfo.Mode().IsRegular()

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
		if isReplace {
			record.Action = "replace"
			record.Before = "existing"
		}
		if err := tx.Append(record); err != nil {
			return recoverFailure(err)
		}
		runRecords = append(runRecords, record)

		if isReplace {
			rollbackSnapshot, err := restore.StageAndReplaceFile(context.Background(), plan, entry, resolver, verifier, provider, rollbackDir)
			if err != nil {
				return recoverFailure(err)
			}
			record.After = "replaced"
			record.RollbackPath = rollbackSnapshot
		} else {
			if err := restore.StageFileWithProvider(context.Background(), plan, entry, resolver, verifier, provider); err != nil {
				return recoverFailure(err)
			}
			record.After = "created"
		}
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
		Long:    "Recover removes or rolls back files recorded by the named transaction. Live source replacement and cross-root deletion are disabled.",
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

func restoreCleanupCmd(verifier restore.CleanupClaimVerifier) *cobra.Command {
	var root string
	var claimPath string
	var intentPath string
	var checkTTL bool
	var ttlDuration time.Duration
	var format string
	c := &cobra.Command{
		Use:     "cleanup",
		Short:   "Clean up plaintext staging and rollback data with verified claim",
		Long:    "Cleanup verifies a signed cleanup claim before destroying plaintext staging and rollback data. Without a valid claim, data is preserved.",
		Example: "  better-drive restore cleanup --root C:/staging --claim claim.json --intent intent.json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			if checkTTL {
				if root == "" {
					return exitcode.WithRemediation(exitcode.ConfigError(errors.New("cleanup --check-ttl requires --root")), "provide --root")
				}
				if ttlDuration == 0 {
					ttlDuration = restore.PlaintextTTL
				}
				if err := restore.CheckPlaintextTTL(root, ttlDuration); err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(err), "clean up stale plaintext or provide a signed cleanup claim")
				}
				if format == output.FormatJSON {
					return output.RenderJSON(cmd.OutOrStdout(), map[string]string{"root": root, "status": "ttl_ok"})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "plaintext TTL ok for root %s\n", root)
				return nil
			}
			if root == "" || claimPath == "" || intentPath == "" {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("cleanup requires --root, --claim, and --intent (or --check-ttl)")), "provide --claim and --intent, or --check-ttl")
			}
			claimData, err := os.ReadFile(claimPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("read claim: %w", err)), "provide a readable claim file")
			}
			var claim restore.CleanupClaim
			if err := json.Unmarshal(claimData, &claim); err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("parse claim: %w", err)), "provide a valid JSON claim")
			}
			intentData, err := os.ReadFile(intentPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("read intent: %w", err)), "provide a readable intent file")
			}
			var intent restore.CleanupIntent
			if err := json.Unmarshal(intentData, &intent); err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("parse intent: %w", err)), "provide a valid JSON intent")
			}
			if err := restore.CleanupPlaintextWithClaim(context.Background(), intent, claim, verifier); err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "verify the cleanup claim and preserve plaintext until claim is valid")
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), map[string]string{"root": root, "status": "cleaned"})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cleaned plaintext in root %s\n", root)
			return nil
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().StringVar(&root, "root", "", "isolated absolute restore root")
	c.Flags().StringVar(&claimPath, "claim", "", "path to signed cleanup claim")
	c.Flags().StringVar(&intentPath, "intent", "", "path to cleanup intent")
	c.Flags().BoolVar(&checkTTL, "check-ttl", false, "check if staging/rollback plaintext exceeds retention TTL")
	c.Flags().DurationVar(&ttlDuration, "ttl", 0, "custom TTL duration for --check-ttl (defaults to 24h)")
	return c
}
