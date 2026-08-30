package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
	"github.com/n24q02m/better-drive/internal/driveapi"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/protectedfs"
	"github.com/spf13/cobra"
)

func cleanupCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "cleanup",
		Short: "Inventory, validate, and broker-execute exact-ID cleanup manifests",
		Long:  "Inventory and validation are read-only. Apply defaults to preview; --execute requires a protected signed approval, remotely consumed owner-risk claim, and exact provider readback.",
		Example: "  better-drive cleanup validate --manifest cleanup.json " +
			"--inventory inventory-aggregate.json --all-roots all-roots.json --format json",
	}
	command.AddCommand(cleanupInventoryCmd(), cleanupValidateCmd(), cleanupApplyCmd(), cleanupApprovalCmd(), cleanupBrokerCmd(), cleanupTrustCmd(), cleanupFixtureLifecycleCmd())
	return command
}

func cleanupApprovalCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "approval",
		Short: "Manage cleanup approvals and intents",
		Long:  "Prepare drafts, canonicalize approvals for offline signing, and activate sealed intents against enrolled trust roots.",
	}
	command.AddCommand(
		cleanupApprovalPrepareCmd(),
		cleanupApprovalCanonicalizeCmd(),
		cleanupApprovalActivateCmd(),
	)
	return command
}

func cleanupApprovalPrepareCmd() *cobra.Command {
	var approvalPath string
	var storePath string
	var format string
	command := &cobra.Command{
		Use:   "prepare",
		Short: "Store a create-only cleanup approval draft",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			approval, err := readApproval(approvalPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "provide a valid approval JSON record")
			}
			if strings.TrimSpace(storePath) == "" {
				storePath = filepath.Join(os.TempDir(), "bdrive-approval-store")
			}
			store, err := cleanup.NewApprovalStore(storePath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "open the protected approval store")
			}
			draft, err := store.Prepare(approval)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "fix the approval draft or resolve store conflicts")
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), draft)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "prepared draft: id=%s digest=%s\n", draft.Approval.ApprovalID, draft.DraftDigest)
			return err
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&approvalPath, "approval", "", "approval JSON to prepare")
	command.Flags().StringVar(&storePath, "store", "", "approval store root path")
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
	var signatureHex string
	var rootPath string
	var storePath string
	var format string
	command := &cobra.Command{
		Use:   "activate",
		Short: "Activate a sealed cleanup intent against an enrolled trust root",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			approval, err := readApproval(approvalPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "provide a valid approval JSON record")
			}
			sigBytes, err := hex.DecodeString(strings.TrimSpace(signatureHex))
			if err != nil || len(sigBytes) == 0 {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("invalid signature hex encoding")), "provide a valid hex-encoded Ed25519 signature")
			}
			root, err := readTrustRoot(rootPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "provide an enrolled trust root record")
			}
			now := time.Now().UTC()
			intent, err := cleanup.ActivateApproval(approval, sigBytes, root, now)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "ensure the signature and trust root match the canonical approval")
			}
			if strings.TrimSpace(storePath) == "" {
				storePath = filepath.Join(os.TempDir(), "bdrive-approval-store")
			}
			store, err := cleanup.NewApprovalStore(storePath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "open the protected approval store")
			}
			if err := store.Activate(intent); err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "activate intent in approval store")
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), intent)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "activated intent: id=%s digest=%s state=%s\n", intent.Approval.ApprovalID, intent.IntentDigest, intent.State)
			return err
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&approvalPath, "approval", "", "approval JSON to activate")
	command.Flags().StringVar(&signatureHex, "signature", "", "hex-encoded detached Ed25519 signature")
	command.Flags().StringVar(&rootPath, "root", "", "enrolled trust root JSON file")
	command.Flags().StringVar(&storePath, "store", "", "approval store root path")
	return command
}

type cleanupInventoryCapturer interface {
	Capture(context.Context, driveapi.InventoryPlan) (cleanup.RootSet, cleanup.InventoryAggregate, error)
}

type cleanupInventoryFactory func(*http.Client, driveapi.AccessTokenSource) (cleanupInventoryCapturer, error)

func cleanupInventoryCmd() *cobra.Command {
	return cleanupInventoryCommand(func(client *http.Client, tokenSource driveapi.AccessTokenSource) (cleanupInventoryCapturer, error) {
		return driveapi.NewInventoryClientWithTokenSource(client, tokenSource)
	})
}

func cleanupInventoryCommand(factory cleanupInventoryFactory) *cobra.Command {
	var planPath string
	var capturePath string
	var statePath string
	var outputPath string
	var format string
	command := &cobra.Command{
		Use:     "inventory",
		Short:   "Capture and join every declared Drive root and page",
		Long:    "Use the dedicated Drive API client to recursively enumerate an immutable all-roots plan, capture every provider page and exact metadata readback, classify newly discovered unbound objects as unknown, and reject missing or scope-mismatched declared bindings.",
		Example: "  better-drive cleanup inventory --plan inventory-plan.json --capture all-roots.json --state inventory-state.json --output inventory-aggregate.json --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			if factory == nil || strings.TrimSpace(planPath) == "" || strings.TrimSpace(capturePath) == "" ||
				strings.TrimSpace(statePath) == "" || strings.TrimSpace(outputPath) == "" {
				return exitcode.WithRemediation(
					exitcode.ConfigError(errors.New("cleanup inventory requires --plan, --capture, --state, and --output")),
					"provide one immutable all-roots plan and distinct capture, state, and aggregate outputs",
				)
			}
			data, err := os.ReadFile(planPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "provide the immutable Drive inventory plan")
			}
			plan, err := driveapi.DecodeInventoryPlan(data)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "freeze the exact account/root/binding plan before capture")
			}
			driveClient := &http.Client{Timeout: 30 * time.Second}
			tokenSource, err := readDriveOAuthTokenSource(driveClient)
			if err != nil {
				return exitcode.WithRemediation(
					exitcode.ConfigError(err),
					"pass the refresh-capable Drive OAuth credential through its inherited descriptor",
				)
			}
			client, err := factory(driveClient, tokenSource)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			rootSet, aggregate, err := client.Capture(cmd.Context(), plan)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "resolve incomplete roots/pages or stale bindings, then rerun the exact plan")
			}
			state := cleanup.BuildState(rootSet, aggregate, nil)
			for _, result := range []struct {
				path  string
				value any
			}{
				{path: capturePath, value: rootSet},
				{path: statePath, value: state},
				{path: outputPath, value: aggregate},
			} {
				if err := writeJSONAtomically(result.path, result.value); err != nil {
					return err
				}
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), aggregate)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "inventory %s: roots=%d pages=%d objects=%d bytes=%d\n", aggregate.Status, aggregate.RootCount, aggregate.PageCount, aggregate.ObjectCount, aggregate.ByteCount)
			return err
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&planPath, "plan", "", "immutable exact Drive account/root/object-binding plan")
	command.Flags().StringVar(&capturePath, "capture", "", "complete provider all-roots capture output")
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
	var approvalID string
	var format string
	var execute bool
	var journalPath string
	command := &cobra.Command{
		Use:     "apply",
		Short:   "Preview or broker-execute an approved cleanup manifest",
		Long:    "Apply defaults to preview. --execute requires protected trust roots, mTLS broker identity, an exact approved snapshot, and a remotely consumed owner-risk claim.",
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
				if journalPath != "" {
					return exitcode.WithRemediation(exitcode.ConfigError(errors.New("--journal is preview-only; protected execution journals remotely")), "remove --journal and use the broker-owned immutable execution journal")
				}
				result, executeErr := executeProtectedCleanup(cmd.Context(), manifest, validation, approvalID)
				if executeErr != nil {
					return classifyCleanupExecutionError(executeErr)
				}
				if format == output.FormatJSON {
					return output.RenderJSON(cmd.OutOrStdout(), result)
				}
				_, executeErr = fmt.Fprintf(cmd.OutOrStdout(), "cleanup %s: claim=%s objects=%d outcome=%s\n", result.Settlement, result.ClaimID, len(result.Moves), result.OutcomeDigest)
				return executeErr
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
	command.Flags().StringVar(&approvalID, "approval-id", "", "exact protected cleanup approval ID (required with --execute)")
	command.Flags().BoolVar(&execute, "execute", false, "consume a protected owner-risk claim and execute one exact leaf move")
	command.Flags().StringVar(&journalPath, "journal", "", "append-only preview/apply journal path")
	return command
}
func classifyCleanupExecutionError(err error) error {
	if errors.Is(err, driveapi.ErrSettlementUnknown) ||
		errors.Is(err, cleanup.ErrOwnerRiskAuthorityUnknown) {
		return exitcode.WithRemediation(
			exitcode.SyncFailed(err),
			"read the authoritative broker state, journal, lease, and provider object; reconcile the unknown settlement and do not retry this claim",
		)
	}
	return exitcode.WithRemediation(
		exitcode.ConfigError(err),
		"reconcile the protected approval, broker snapshot, capability, and provider readback before retry",
	)
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
	if err := decodeStrictJSON(data, &approval); err != nil {
		return cleanup.Approval{}, fmt.Errorf("decode approval: %w", err)
	}
	if _, err := cleanup.CanonicalApproval(approval); err != nil {
		return cleanup.Approval{}, err
	}
	return approval, nil
}

func readTrustRoot(path string) (cleanup.TrustRoot, error) {
	if strings.TrimSpace(path) == "" {
		return cleanup.TrustRoot{}, errors.New("--root is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cleanup.TrustRoot{}, err
	}
	var root cleanup.TrustRoot
	if err := decodeStrictJSON(data, &root); err != nil {
		return cleanup.TrustRoot{}, fmt.Errorf("decode trust root: %w", err)
	}
	return root, nil
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
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	tempID, err := newCleanupExecutionID("cleanup")
	if err != nil {
		return err
	}
	tempName := filepath.Join(parent, "."+tempID+".tmp")
	temp, err := protectedfs.CreatePrivateFile(tempName)
	if err != nil {
		return err
	}
	cleanupFailure := func(primary error) error {
		return errors.Join(primary, temp.Close(), removeTemporaryJSON(tempName))
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		return cleanupFailure(err)
	}
	if err := temp.Sync(); err != nil {
		return cleanupFailure(err)
	}
	if err := temp.Close(); err != nil {
		return errors.Join(err, removeTemporaryJSON(tempName))
	}
	if err := atomicReplaceFile(tempName, path); err != nil {
		return errors.Join(err, removeTemporaryJSON(tempName))
	}
	return nil
}

func removeTemporaryJSON(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
