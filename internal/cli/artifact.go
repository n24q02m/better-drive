package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/n24q02m/better-drive/internal/artifactcrypto"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/spf13/cobra"
)

func artifactCmd() *cobra.Command {
	return artifactCmdWithResolver(nil)
}

func artifactCmdWithResolver(resolver artifactcrypto.Resolver) *cobra.Command {
	c := &cobra.Command{
		Use:     "artifact",
		Short:   "Seal and open encrypted backup artifacts with authenticated metadata",
		Long:    "Seal streams plaintext from stdin/file directly to ciphertext spool and authenticates restore_set_id, component, and key metadata. Open validates all frames before writing.",
		Example: "  better-drive artifact seal --key-ref secret:key --key-version 1 --restore-set-id set-1 --component state < plaintext.bin > sealed.bin",
	}
	c.AddCommand(artifactSealCmd(resolver), artifactOpenCmd(resolver))
	return c
}

func artifactSealCmd(resolver artifactcrypto.Resolver) *cobra.Command {
	var keyRef string
	var keyVersion uint64
	var restoreSetID string
	var component string
	var inputPath string
	var outputPath string
	var format string
	c := &cobra.Command{
		Use:     "seal",
		Short:   "Seal plaintext into an authenticated encrypted artifact",
		Long:    "Stream plaintext to ciphertext spool without leaking plaintext or key bytes.",
		Example: "  better-drive artifact seal --key-ref my-key --key-version 1 --restore-set-id set-1 --component db --input dump.sql --output dump.art --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if resolver == nil {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("artifact resolver is required for seal")), "provide a configured artifact resolver")
			}
			if strings.TrimSpace(keyRef) == "" || keyVersion == 0 || strings.TrimSpace(restoreSetID) == "" || strings.TrimSpace(component) == "" {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("seal requires --key-ref, --key-version, --restore-set-id, and --component")), "specify all required metadata fields")
			}
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			metadata := artifactcrypto.Metadata{
				RestoreSetID: restoreSetID,
				Component:    component,
				KeyRef:       keyRef,
				KeyVersion:   keyVersion,
			}
			var in io.Reader = cmd.InOrStdin()
			if inputPath != "" && inputPath != "-" {
				f, err := os.Open(inputPath)
				if err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("open input: %w", err)), "provide a readable input file")
				}
				defer f.Close()
				in = f
			}
			var out io.Writer = cmd.OutOrStdout()
			if outputPath != "" && outputPath != "-" {
				f, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
				if err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("open output: %w", err)), "provide a writable output path")
				}
				defer f.Close()
				out = f
			}
			res, err := artifactcrypto.Seal(out, in, resolver, metadata)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("artifact seal failed: %w", err)), "check key and input availability")
			}
			if format == output.FormatJSON {
				// Evidence never exposes plaintext digest as per spec!
				return output.RenderJSON(cmd.OutOrStdout(), map[string]interface{}{
					"ciphertext_digest": res.CiphertextDigest,
					"restore_set_id":    restoreSetID,
					"component":         component,
					"key_ref":           keyRef,
					"key_version":       keyVersion,
				})
			}
			return nil
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().StringVar(&keyRef, "key-ref", "", "secret reference or key name")
	c.Flags().Uint64Var(&keyVersion, "key-version", 1, "key version number")
	c.Flags().StringVar(&restoreSetID, "restore-set-id", "", "restore set ID")
	c.Flags().StringVar(&component, "component", "", "component name")
	c.Flags().StringVar(&inputPath, "input", "", "input file path (default stdin)")
	c.Flags().StringVar(&outputPath, "output", "", "output file path (default stdout)")
	return c
}

func artifactOpenCmd(resolver artifactcrypto.Resolver) *cobra.Command {
	var keyRef string
	var keyVersion uint64
	var restoreSetID string
	var component string
	var inputPath string
	var outputPath string
	var format string
	c := &cobra.Command{
		Use:     "open",
		Short:   "Open and authenticate an encrypted artifact",
		Long:    "Authenticate all frames and metadata before writing any plaintext.",
		Example: "  better-drive artifact open --key-ref my-key --key-version 1 --restore-set-id set-1 --component db --input dump.art --output dump.sql",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if resolver == nil {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("artifact resolver is required for open")), "provide a configured artifact resolver")
			}
			if strings.TrimSpace(keyRef) == "" || keyVersion == 0 || strings.TrimSpace(restoreSetID) == "" || strings.TrimSpace(component) == "" {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("open requires --key-ref, --key-version, --restore-set-id, and --component")), "specify all expected metadata fields")
			}
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			expected := artifactcrypto.Metadata{
				RestoreSetID: restoreSetID,
				Component:    component,
				KeyRef:       keyRef,
				KeyVersion:   keyVersion,
			}
			var in io.Reader = cmd.InOrStdin()
			if inputPath != "" && inputPath != "-" {
				f, err := os.Open(inputPath)
				if err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("open input: %w", err)), "provide a readable input file")
				}
				defer f.Close()
				in = f
			}
			var out io.Writer = cmd.OutOrStdout()
			if outputPath != "" && outputPath != "-" {
				tmpPath := outputPath + fmt.Sprintf(".tmp.%d", os.Getpid())
				f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
				if err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("open output: %w", err)), "provide a writable output path")
				}
				openErr := artifactcrypto.Open(f, in, resolver, expected)
				closeErr := f.Close()
				if openErr != nil {
					_ = os.Remove(tmpPath)
					return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("artifact open failed: %w", openErr)), "tampered artifact or wrong key/metadata")
				}
				if closeErr != nil {
					_ = os.Remove(tmpPath)
					return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("close output: %w", closeErr)), "failed to flush plaintext output")
				}
				if err := os.Rename(tmpPath, outputPath); err != nil {
					_ = os.Remove(tmpPath)
					return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("commit output: %w", err)), "failed to commit plaintext output")
				}
			} else {
				if err := artifactcrypto.Open(out, in, resolver, expected); err != nil {
					return exitcode.WithRemediation(exitcode.ConfigError(fmt.Errorf("artifact open failed: %w", err)), "tampered artifact or wrong key/metadata")
				}
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), map[string]string{
					"status":         "authenticated",
					"restore_set_id": restoreSetID,
					"component":      component,
				})
			}
			return nil
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().StringVar(&keyRef, "key-ref", "", "expected key reference")
	c.Flags().Uint64Var(&keyVersion, "key-version", 1, "expected key version")
	c.Flags().StringVar(&restoreSetID, "restore-set-id", "", "expected restore set ID")
	c.Flags().StringVar(&component, "component", "", "expected component name")
	c.Flags().StringVar(&inputPath, "input", "", "input artifact file path (default stdin)")
	c.Flags().StringVar(&outputPath, "output", "", "output plaintext file path (default stdout)")
	return c
}
