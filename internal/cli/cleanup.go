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
		Long:    "Cleanup inventory and validation are read-only. Apply defaults to preview and refuses live mutation without the named owner-risk capability.",
		Example: "  better-drive cleanup validate --manifest cleanup.json --format json",
	}
	command.AddCommand(cleanupInventoryCmd(), cleanupValidateCmd(), cleanupApplyCmd())
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
