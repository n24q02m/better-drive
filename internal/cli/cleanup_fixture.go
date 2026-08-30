package cli

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
	"github.com/n24q02m/better-drive/internal/driveapi"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/spf13/cobra"
)

const maxFixtureLifecycleRequestBytes = 1 << 20

func cleanupFixtureLifecycleCmd() *cobra.Command {
	var requestPath string
	var execute bool
	var format string
	command := &cobra.Command{
		Use:   "fixture-cycle",
		Short: "Preview or execute a signed candidate fixture lifecycle",
		Long:  "Verify a fresh isolated fixture capability bound to one artifact, fixture digest, exact quarantine/restore/requarantine sequence, and explicit production-root denial. Execution uses the dedicated three-phase fixture path; it never widens generic cleanup mutation eligibility.",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			data, err := readFixtureLifecycleRequest(requestPath)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "provide one bounded signed fixture lifecycle request")
			}
			request, err := driveapi.DecodeFixtureLifecycleRequest(data)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			bundle, err := readProtectedTrustBundle()
			if err != nil {
				return exitcode.ConfigError(err)
			}
			now := time.Now().UTC()
			publicKey, err := bundle.ApprovalRoot.PublicKeyForPurpose(cleanup.CleanupTrustPurpose, request.Capability.Issuer, now)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			signature, err := hex.DecodeString(request.SignatureHex)
			if err != nil || len(signature) != ed25519.SignatureSize {
				return exitcode.ConfigError(errors.New("fixture lifecycle signature is invalid"))
			}
			if err := driveapi.VerifyFixtureLifecycleCapability(request.Capability, signature, publicKey, now); err != nil {
				return exitcode.ConfigError(err)
			}
			capabilityDigest, err := driveapi.FixtureLifecycleCapabilityDigest(request.Capability)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			if !execute {
				preview := struct {
					Status           string   `json:"status"`
					FixtureID        string   `json:"fixture_id"`
					FixtureDigest    string   `json:"fixture_digest"`
					CapabilityDigest string   `json:"capability_digest"`
					Sequence         []string `json:"sequence"`
					ProductionDenied bool     `json:"production_denied"`
				}{
					Status: "preview", FixtureID: request.Capability.FixtureID,
					FixtureDigest: request.Capability.FixtureDigest, CapabilityDigest: capabilityDigest,
					Sequence: append([]string(nil), request.Capability.Sequence...), ProductionDenied: request.Capability.ProductionDenied,
				}
				if format == output.FormatJSON {
					return output.RenderJSON(command.OutOrStdout(), preview)
				}
				_, err = fmt.Fprintf(command.OutOrStdout(), "fixture lifecycle preview: fixture=%s capability=%s production_denied=true\n", preview.FixtureID, preview.CapabilityDigest)
				return err
			}
			token, err := readSecretFD(driveTokenFDEnv, maxDriveTokenBytes)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "pass the candidate Drive token through the inherited token file descriptor")
			}
			defer zeroBytes(token)
			repo, err := cleanup.NewGitRepo(paths.CleanupAuthorityStoreDir())
			if err != nil {
				return exitcode.ConfigError(err)
			}
			receiptStore, err := cleanup.NewGitFixtureLifecycleReceiptStore(repo, time.Now)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			executor, err := driveapi.NewFixtureLifecycleExecutor(
				&http.Client{Timeout: 30 * time.Second},
				string(token),
				publicKey,
				receiptStore,
			)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			result, err := executor.Execute(command.Context(), request)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "preserve the fixture state and reconcile the exact failed phase; never retry an ambiguous provider mutation")
			}
			if format == output.FormatJSON {
				return output.RenderJSON(command.OutOrStdout(), result)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "fixture lifecycle complete: fixture=%s final_parent=%s phases=%d\n", result.FixtureID, result.FinalParentID, len(result.Moves))
			return err
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&requestPath, "request", "", "signed candidate fixture lifecycle request")
	command.Flags().BoolVar(&execute, "execute", false, "consume the capability and execute exactly quarantine, restore, requarantine")
	return command
}

func readFixtureLifecycleRequest(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("--request is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxFixtureLifecycleRequestBytes {
		return nil, errors.New("fixture lifecycle request type or size is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFixtureLifecycleRequestBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxFixtureLifecycleRequestBytes {
		return nil, errors.New("fixture lifecycle request is unreadable or exceeds its bound")
	}
	return data, nil
}
