package cli

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/spf13/cobra"
)

const (
	cleanupDraftCapability    = "BD-CLEANUP-DRAFT-RW"
	cleanupApprovalCapability = "BD-CLEANUP-APPROVAL-RW"
)

func cleanupCmd() *cobra.Command {
	command := &cobra.Command{
		Use:     "cleanup",
		Short:   "Inventory and validate exact-ID cleanup manifests",
		Long:    "Cleanup inventory and validation are read-only. Apply defaults to preview and refuses live mutation without the named owner-risk capability.",
		Example: "  better-drive cleanup validate --manifest cleanup.json --format json",
	}
	command.AddCommand(cleanupInventoryCmd(), cleanupValidateCmd(), cleanupApplyCmd(), cleanupApprovalCmd())
	return command
}

func cleanupApprovalCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "approval",
		Short: "Prepare and activate signed cleanup approvals",
		Long:  "Approval prepare/canonicalize/activate keep private signing outside the cleanup client and require explicit capability labels.",
	}
	command.AddCommand(cleanupApprovalPrepareCmd(), cleanupApprovalCanonicalizeCmd(), cleanupApprovalActivateCmd())
	return command
}

func cleanupApprovalPrepareCmd() *cobra.Command {
	var approvalPath string
	var storePath string
	var capability string
	var format string
	command := &cobra.Command{
		Use:   "prepare",
		Short: "Create a create-only cleanup approval draft",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			if capability != cleanupDraftCapability {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("cleanup approval prepare requires BD-CLEANUP-DRAFT-RW")), "provide the named draft capability from the protected control plane")
			}
			approval, err := readApproval(approvalPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "provide a canonical approval JSON record")
			}
			record, err := cleanup.NewApprovalStore(storePath).Prepare(approval)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "use a private create-only approval store and reject foreign drafts")
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), record)
			}
			canonical, _ := cleanup.CanonicalApproval(approval)
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "draft prepared: approval=%s digest=%s\n", approval.ApprovalID, cleanup.Digest(canonical))
			return err
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&approvalPath, "approval", "", "canonical approval JSON")
	command.Flags().StringVar(&storePath, "store", "", "private create-only approval store root")
	command.Flags().StringVar(&capability, "capability", "", "exact protected capability")
	return command
}

func cleanupApprovalCanonicalizeCmd() *cobra.Command {
	var approvalPath string
	command := &cobra.Command{
		Use:   "canonicalize",
		Short: "Render canonical approval bytes for offline signing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			approval, err := readApproval(approvalPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "provide a canonical approval JSON record")
			}
			canonical, err := cleanup.CanonicalApproval(approval)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "fix the approval before offline signing")
			}
			_, err = cmd.OutOrStdout().Write(append(canonical, '\n'))
			return err
		},
	}
	command.Flags().StringVar(&approvalPath, "approval", "", "approval JSON to canonicalize")
	return command
}

func cleanupApprovalActivateCmd() *cobra.Command {
	var approvalPath string
	var signaturePath string
	var trustRootPath string
	var storePath string
	var capability string
	var format string
	command := &cobra.Command{
		Use:   "activate",
		Short: "Activate a signed approval against an enrolled trust root",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			if capability != cleanupApprovalCapability {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("cleanup approval activate requires BD-CLEANUP-APPROVAL-RW")), "provide the named protected approval capability")
			}
			approval, err := readApproval(approvalPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "provide a canonical approval JSON record")
			}
			trustRootData, err := os.ReadFile(trustRootPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "provide the enrolled public trust-root record")
			}
			var trustRoot cleanup.TrustRoot
			if err := json.Unmarshal(trustRootData, &trustRoot); err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "provide valid trust-root JSON without private key material")
			}
			signatureHex, err := os.ReadFile(signaturePath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "provide the detached signature from the protected signer")
			}
			signature, err := hex.DecodeString(strings.TrimSpace(string(signatureHex)))
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "signature input must be lowercase or uppercase hexadecimal")
			}
			intent, err := cleanup.ActivateApproval(approval, signature, trustRoot, time.Now().UTC())
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "reject unknown issuer, trust-root drift, expiry, or signature mismatch")
			}
			if err := cleanup.NewApprovalStore(storePath).Activate(intent); err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "keep split/foreign intent state in reconciliation and do not overwrite it")
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), intent)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "approval activated: approval=%s state=%s digest=%s\n", approval.ApprovalID, intent.State, intent.IntentDigest)
			return err
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&approvalPath, "approval", "", "canonical approval JSON")
	command.Flags().StringVar(&signaturePath, "signature", "", "detached signature hex file")
	command.Flags().StringVar(&trustRootPath, "trust-root", "", "enrolled public trust-root JSON")
	command.Flags().StringVar(&storePath, "store", "", "private create-only approval store root")
	command.Flags().StringVar(&capability, "capability", "", "exact protected capability")
	return command
}

func cleanupInventoryCmd() *cobra.Command {
	var account string
	var allRootsPath string
	var statePath string
	var outputPath string
	var format string
	command := &cobra.Command{
		Use:     "inventory",
		Short:   "Join a complete enumerated root/page inventory",
		Long:    "Read an enumerated all-roots capture, require every page to be complete exactly once, and write only aggregate/state evidence.",
		Example: "  better-drive cleanup inventory --account account-1 --all-roots all-roots.json --state inventory-state.json --output inventory-aggregate.json --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			if strings.TrimSpace(account) == "" || strings.TrimSpace(allRootsPath) == "" || strings.TrimSpace(statePath) == "" || strings.TrimSpace(outputPath) == "" {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("cleanup inventory requires --account, --all-roots, --state, and --output")), "provide explicit account, root-set, state, and aggregate paths")
			}
			data, err := os.ReadFile(allRootsPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "capture the provider root/page inventory before joining it")
			}
			rootSet, err := cleanup.DecodeRootSet(data)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "regenerate the all-roots capture with schema_version 1")
			}
			aggregate, aggregateErr := cleanup.BuildAggregate(rootSet, account)
			state := cleanup.BuildState(rootSet, aggregate, aggregateErr)
			if err := writeJSONAtomically(statePath, state); err != nil {
				return err
			}
			if aggregateErr != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(aggregateErr), "complete every root/page checkpoint before cleanup validation")
			}
			if err := writeJSONAtomically(outputPath, aggregate); err != nil {
				return err
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), aggregate)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "inventory %s: roots=%d pages=%d objects=%d bytes=%d\n", aggregate.Status, aggregate.RootCount, aggregate.PageCount, aggregate.ObjectCount, aggregate.ByteCount)
			return err
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&account, "account", "", "exact provider account reference")
	command.Flags().StringVar(&allRootsPath, "all-roots", "", "enumerated root/page capture")
	command.Flags().StringVar(&statePath, "state", "", "inventory checkpoint state output")
	command.Flags().StringVar(&outputPath, "output", "", "inventory aggregate output")
	return command
}

func cleanupValidateCmd() *cobra.Command {
	var manifestPath string
	var inventoryPath string
	var format string
	command := &cobra.Command{
		Use:     "validate",
		Short:   "Validate an exact-ID cleanup manifest without mutation",
		Long:    "Validate safe classes, canonical provider scope, restore evidence, ownership markers, expiry, and object/byte budgets.",
		Example: "  better-drive cleanup validate --manifest cleanup.json --inventory inventory-aggregate.json --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			manifest, err := readManifest(manifestPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "write a canonical JSON cleanup manifest before validation")
			}
			validation, err := cleanup.ValidateManifest(manifest, time.Now().UTC())
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "regenerate the manifest from complete provider metadata and restore evidence")
			}
			if inventoryPath != "" {
				inventory, inventoryErr := readInventoryAggregate(inventoryPath)
				if inventoryErr != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(inventoryErr), "provide a complete current-schema inventory aggregate")
				}
				validation, err = cleanup.ValidateManifestAgainstInventory(manifest, inventory, time.Now().UTC())
				if err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(err), "refresh the live inventory and regenerate the manifest before any owner-risk request")
				}
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), validation)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "manifest valid: digest=%s objects=%d bytes=%d mode=%s\n", validation.ManifestDigest, validation.ObjectCount, validation.ByteCount, validation.Mode)
			return err
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&manifestPath, "manifest", "", "exact-ID cleanup manifest")
	command.Flags().StringVar(&inventoryPath, "inventory", "", "complete provider inventory aggregate for exact metadata binding")
	return command
}

func cleanupApplyCmd() *cobra.Command {
	var manifestPath string
	var format string
	var execute bool
	var journalPath string
	command := &cobra.Command{
		Use:     "apply",
		Short:   "Preview an approved cleanup manifest",
		Long:    "Apply defaults to preview. Live mutation requires the separate BD-DRIVE-MUTATION-RW owner-risk capability and is fail-closed here until a provider broker is bound.",
		Example: "  better-drive cleanup apply --manifest cleanup.json --journal cleanup.jsonl --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			manifest, err := readManifest(manifestPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "write a canonical JSON cleanup manifest before apply preview")
			}
			validation, err := cleanup.ValidateManifest(manifest, time.Now().UTC())
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "fix validation failures before requesting an owner-risk capability")
			}
			if execute {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("cleanup apply --execute is disabled without BD-DRIVE-MUTATION-RW and a provider broker")), "use preview only until the exact signed owner-risk capability and broker readback are present")
			}
			if journalPath != "" {
				journal, journalErr := cleanup.OpenFileJournal(journalPath)
				if journalErr != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(journalErr), "use a writable owner-only journal path")
				}
				for _, object := range manifest.Objects {
					if journalErr := journal.Append(cleanup.JournalRecord{Action: "preview", ObjectID: object.ID, Before: string(object.Class), After: "preview"}); journalErr != nil {
						return journalErr
					}
				}
			}
			preview := struct {
				Status     string             `json:"status"`
				Validation cleanup.Validation `json:"validation"`
			}{Status: "preview", Validation: validation}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), preview)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "preview only: objects=%d bytes=%d mode=%s\n", validation.ObjectCount, validation.ByteCount, validation.Mode)
			return err
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&manifestPath, "manifest", "", "exact-ID cleanup manifest")
	command.Flags().BoolVar(&execute, "execute", false, "request live mutation (requires separate capability; currently fail-closed)")
	command.Flags().StringVar(&journalPath, "journal", "", "append-only preview/apply journal path")
	return command
}

func readApproval(path string) (cleanup.Approval, error) {
	if strings.TrimSpace(path) == "" {
		return cleanup.Approval{}, errors.New("--approval is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cleanup.Approval{}, err
	}
	var approval cleanup.Approval
	if err := json.Unmarshal(data, &approval); err != nil {
		return cleanup.Approval{}, fmt.Errorf("decode approval: %w", err)
	}
	if _, err := cleanup.CanonicalApproval(approval); err != nil {
		return cleanup.Approval{}, err
	}
	return approval, nil
}

func readManifest(path string) (cleanup.Manifest, error) {
	if strings.TrimSpace(path) == "" {
		return cleanup.Manifest{}, errors.New("--manifest is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cleanup.Manifest{}, err
	}
	return cleanup.DecodeManifest(data)
}

func readInventoryAggregate(path string) (cleanup.InventoryAggregate, error) {
	if strings.TrimSpace(path) == "" {
		return cleanup.InventoryAggregate{}, errors.New("--inventory is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cleanup.InventoryAggregate{}, err
	}
	return cleanup.DecodeAggregate(data)
}

func writeJSONAtomically(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".cleanup-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}
