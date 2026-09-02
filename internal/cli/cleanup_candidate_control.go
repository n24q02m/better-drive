package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/spf13/cobra"
)

const candidateControlInputLimit = 1 << 20

func cleanupCandidateControlCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "candidate-control",
		Short: "Verify signed Skret candidate-control evidence",
	}
	command.AddCommand(cleanupCandidateControlVerifyCmd(), cleanupCandidateControlTrustCmd())
	return command
}

func cleanupCandidateControlVerifyCmd() *cobra.Command {
	var capabilityPath string
	var capabilityRootPath string
	var readbackPath string
	var readbackRootPath string
	var format string
	command := &cobra.Command{
		Use:   "verify",
		Short: "Derive a candidate-control marker from exact signed readback",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			capabilityData, err := readCandidateControlFile(capabilityPath, "--capability")
			if err != nil {
				return candidateControlInputError(err)
			}
			capabilityRoot, err := readCandidateControlTrustRoot(capabilityRootPath, "--capability-root")
			if err != nil {
				return candidateControlInputError(fmt.Errorf("capability root: %w", err))
			}
			readbackData, err := readCandidateControlFile(readbackPath, "--readback")
			if err != nil {
				return candidateControlInputError(err)
			}
			readbackRoot, err := readCandidateControlTrustRoot(readbackRootPath, "--readback-root")
			if err != nil {
				return candidateControlInputError(fmt.Errorf("readback root: %w", err))
			}
			protectedRoots, trustBundleDigest, err := readProtectedCandidateControlTrustBundle()
			if err != nil {
				return candidateControlInputError(err)
			}
			if capabilityRoot != protectedRoots.CapabilityRoot ||
				readbackRoot != protectedRoots.ReadbackRoot {
				return candidateControlInputError(errors.New("candidate-control evidence roots do not match the protected candidate-control trust bundle"))
			}
			marker, err := cleanup.VerifyCandidateControlExercise(
				capabilityData,
				capabilityRoot,
				readbackData,
				readbackRoot,
				trustBundleDigest,
				time.Now().UTC(),
			)
			if err != nil {
				return exitcode.WithRemediation(
					exitcode.ConfigError(err),
					"provide exact live GitHub capability/readback evidence signed by the enrolled Skret issuer and executor roots",
				)
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), marker)
			}
			_, err = fmt.Fprintf(
				cmd.OutOrStdout(),
				"candidate control exercised: transaction=%s remote=%s operations=%d claim=%s trust_bundle=%s\n",
				marker.TransactionID,
				marker.Remote,
				marker.OperationCount,
				marker.ClaimOID,
				marker.TrustBundleDigest,
			)
			return err
		},
	}
	output.AddFormatFlag(command, &format)
	command.Flags().StringVar(&capabilityPath, "capability", "", "signed BD-CANDIDATE-CONTROL-V1 JSON file")
	command.Flags().StringVar(&capabilityRootPath, "capability-root", "", "Skret capability root record that must exactly match the protected bundle")
	command.Flags().StringVar(&readbackPath, "readback", "", "signed BD-CANDIDATE-CONTROL-READBACK-V1 JSON file")
	command.Flags().StringVar(&readbackRootPath, "readback-root", "", "Skret executor root record that must exactly match the protected bundle")
	return command
}

func readCandidateControlFile(path, flag string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%s is required", flag)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", flag, err)
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() ||
		info.Size() <= 0 || info.Size() > candidateControlInputLimit {
		_ = file.Close()
		return nil, fmt.Errorf("%s must be a non-empty regular file no larger than 1 MiB", flag)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, candidateControlInputLimit+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("read %s: %w", flag, err)
	}
	if len(data) == 0 || len(data) > candidateControlInputLimit {
		return nil, fmt.Errorf("%s size changed while reading", flag)
	}
	return data, nil
}

func readCandidateControlTrustRoot(path, flag string) (cleanup.TrustRoot, error) {
	data, err := readCandidateControlFile(path, flag)
	if err != nil {
		return cleanup.TrustRoot{}, err
	}
	var root cleanup.TrustRoot
	if err := decodeStrictJSON(data, &root); err != nil {
		return cleanup.TrustRoot{}, fmt.Errorf("decode %s: %w", flag, err)
	}
	return root, nil
}

func candidateControlInputError(err error) error {
	return exitcode.WithRemediation(
		exitcode.ConfigError(err),
		"enroll the fixed protected candidate-control trust bundle through its inherited descriptor, then provide the exact matching root records with the signed capability and executor readback",
	)
}
