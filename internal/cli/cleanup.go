package cli

import (
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

func cleanupCmd() *cobra.Command {
	command := &cobra.Command{
		Use:     "cleanup",
		Short:   "Inventory and validate exact-ID cleanup manifests",
		Long:    "Cleanup inventory and validation are read-only. Apply is preview-only and never performs live mutation.",
		Example: "  better-drive cleanup validate --manifest cleanup.json --format json",
	}
	command.AddCommand(cleanupInventoryCmd(), cleanupValidateCmd(), cleanupApplyCmd(), cleanupApprovalCmd())
	return command
}

func cleanupApprovalCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "approval",
		Short: "Canonicalize cleanup approvals",
		Long:  "Canonicalize approvals for offline signing; protected approval preparation and activation are unavailable in this client.",
	}
	command.AddCommand(cleanupApprovalCanonicalizeCmd())
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
				return exitcode.WithRemediation(exitcode.ConfigError(err), "regenerate the all-roots capture with current schema_version 3")
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
	var allRootsPath string
	var format string
	command := &cobra.Command{
		Use:     "validate",
		Short:   "Validate an exact-ID cleanup manifest without mutation",
		Long:    "Validate safe classes, canonical provider scope, restore evidence, expiry, and object/byte budgets.",
		Example: "  better-drive cleanup validate --manifest cleanup.json --inventory inventory-aggregate.json --all-roots all-roots.json --format json",
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
				if strings.TrimSpace(allRootsPath) == "" {
					return exitcode.WithRemediation(exitcode.ConfigError(errors.New("--all-roots is required with --inventory")), "provide the exact current-schema all-roots capture used to build the aggregate")
				}
				rootData, rootErr := os.ReadFile(allRootsPath)
				if rootErr != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(rootErr), "provide the exact all-roots capture used for this aggregate")
				}
				rootSet, rootErr := cleanup.DecodeRootSet(rootData)
				if rootErr != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(rootErr), "provide a complete current-schema all-roots capture")
				}
				inventoryData, inventoryErr := os.ReadFile(inventoryPath)
				if inventoryErr != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(inventoryErr), "provide the inventory aggregate produced from the exact root capture")
				}
				inventory, inventoryErr := cleanup.DecodeAggregate(inventoryData, rootSet, manifest.AccountID)
				if inventoryErr != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(inventoryErr), "rebuild the aggregate from the exact all-roots capture before manifest binding")
				}
				validation, err = cleanup.ValidateManifestAgainstInventory(manifest, rootSet, inventory, time.Now().UTC())
				if err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(err), "refresh the exact inventory capture and regenerate the manifest before any owner-risk request")
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
	command.Flags().StringVar(&allRootsPath, "all-roots", "", "exact current-schema root/page capture used to build --inventory")
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
		Long:    "Apply is preview-only. Live mutation is unavailable until a protected provider broker is bound.",
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
				return exitcode.WithRemediation(exitcode.ConfigError(err), "fix validation failures before requesting protected execution")
			}
			if execute {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("cleanup apply execution is unavailable until a protected provider broker is bound")), "use preview only; no local capability can authorize Drive mutation")
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
	command.Flags().BoolVar(&execute, "execute", false, "request unavailable live mutation (always fail-closed)")
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
