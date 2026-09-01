package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/n24q02m/better-drive/internal/protectedfs"
	"github.com/spf13/cobra"
)

const (
	candidateControlTrustBundleSchema = 1
	candidateControlTrustBundleFDEnv  = "BETTER_DRIVE_CANDIDATE_CONTROL_TRUST_BUNDLE_FD"
)

type candidateControlTrustBundle struct {
	SchemaVersion  int               `json:"schema_version"`
	CapabilityRoot cleanup.TrustRoot `json:"capability_root"`
	ReadbackRoot   cleanup.TrustRoot `json:"readback_root"`
}

func cleanupCandidateControlTrustCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "trust",
		Short: "Enroll or rotate protected candidate-control trust roots",
		Long:  "Install the Skret capability and executor public roots as one fixed-path protected bundle. Input is accepted only through an inherited file descriptor.",
	}
	command.AddCommand(cleanupCandidateControlTrustEnrollCmd(), cleanupCandidateControlTrustRotateCmd())
	return command
}

func cleanupCandidateControlTrustEnrollCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enroll",
		Short: "Create the protected candidate-control trust bundle",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			data, err := readSecretFD(candidateControlTrustBundleFDEnv, maxCleanupSecurityFile)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			defer zeroBytes(data)
			digest, err := writeCandidateControlTrustBundle(data, nil)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "candidate-control trust bundle enrolled: sha256=%s\n", digest)
			return err
		},
	}
}

func cleanupCandidateControlTrustRotateCmd() *cobra.Command {
	var expectedCurrentDigest string
	command := &cobra.Command{
		Use:   "rotate",
		Short: "CAS-rotate the protected candidate-control trust bundle",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			expectedCurrentDigest = strings.ToLower(strings.TrimSpace(expectedCurrentDigest))
			if !isSHA256Hex(expectedCurrentDigest) {
				return exitcode.ConfigError(errors.New("--expected-current-digest must be a 64-character SHA-256 hex digest"))
			}
			data, err := readSecretFD(candidateControlTrustBundleFDEnv, maxCleanupSecurityFile)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			defer zeroBytes(data)
			digest, err := writeCandidateControlTrustBundle(data, &expectedCurrentDigest)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "candidate-control trust bundle rotated: sha256=%s\n", digest)
			return err
		},
	}
	command.Flags().StringVar(&expectedCurrentDigest, "expected-current-digest", "", "exact SHA-256 of the currently enrolled candidate-control trust bundle")
	_ = command.MarkFlagRequired("expected-current-digest")
	return command
}

func readProtectedCandidateControlTrustBundle() (candidateControlTrustBundle, string, error) {
	data, err := readProtectedFile(paths.CleanupCandidateControlTrustBundleFile(), maxCleanupSecurityFile)
	if err != nil {
		return candidateControlTrustBundle{}, "", fmt.Errorf("read protected candidate-control trust bundle: %w", err)
	}
	bundle, err := decodeAndValidateCandidateControlTrustBundle(data, time.Now().UTC())
	if err != nil {
		return candidateControlTrustBundle{}, "", fmt.Errorf("decode protected candidate-control trust bundle: %w", err)
	}
	canonical, err := canonicalCandidateControlTrustBundle(bundle)
	if err != nil {
		return candidateControlTrustBundle{}, "", err
	}
	if !bytes.Equal(data, canonical) {
		return candidateControlTrustBundle{}, "", errors.New("protected candidate-control trust bundle is not canonical")
	}
	return bundle, cleanupTrustDigest(canonical), nil
}

func writeCandidateControlTrustBundle(data []byte, expectedCurrentDigest *string) (string, error) {
	bundle, err := decodeAndValidateCandidateControlTrustBundle(data, time.Now().UTC())
	if err != nil {
		return "", err
	}
	canonical, err := canonicalCandidateControlTrustBundle(bundle)
	if err != nil {
		return "", err
	}
	digest := cleanupTrustDigest(canonical)
	securityRoot, err := ensureCleanupSecurityDir()
	if err != nil {
		return "", err
	}
	lock, err := acquireCleanupTrustLock(securityRoot)
	if err != nil {
		return "", err
	}
	result, installErr := installCandidateControlTrustBundleLocked(
		securityRoot,
		bundle,
		canonical,
		digest,
		expectedCurrentDigest,
	)
	releaseErr := lock.release()
	if releaseErr != nil {
		return "", errors.Join(installErr, fmt.Errorf("release candidate-control trust bundle lock: %w", releaseErr))
	}
	return result, installErr
}

func installCandidateControlTrustBundleLocked(
	securityRoot string,
	bundle candidateControlTrustBundle,
	canonical []byte,
	digest string,
	expectedCurrentDigest *string,
) (string, error) {
	target := paths.CleanupCandidateControlTrustBundleFile()
	if expectedCurrentDigest == nil {
		if _, err := os.Lstat(target); err == nil {
			return "", errors.New("candidate-control trust bundle is already enrolled; use trust rotate with its exact current digest")
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	} else {
		expected := strings.ToLower(strings.TrimSpace(*expectedCurrentDigest))
		if !isSHA256Hex(expected) {
			return "", errors.New("expected current candidate-control trust bundle digest is invalid")
		}
		currentData, err := readProtectedFile(target, maxCleanupSecurityFile)
		if err != nil {
			return "", err
		}
		if cleanupTrustDigest(currentData) != expected {
			return "", errors.New("candidate-control trust bundle changed since the expected current digest")
		}
		current, err := decodeAndValidateCandidateControlTrustBundle(currentData, time.Now().UTC())
		if err != nil {
			return "", fmt.Errorf("validate current candidate-control trust bundle: %w", err)
		}
		currentCanonical, err := canonicalCandidateControlTrustBundle(current)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(currentData, currentCanonical) {
			return "", errors.New("current candidate-control trust bundle is not canonical")
		}
		if err := validateCandidateControlTrustRotation(current, bundle); err != nil {
			return "", err
		}
		if digest == expected {
			return "", errors.New("candidate-control trust bundle rotation is identical to the current bundle")
		}
	}

	temporary, err := protectedfs.CreatePrivateFile(
		filepath.Join(securityRoot, fmt.Sprintf(".candidate-control-trust-bundle-%d.tmp", time.Now().UnixNano())),
	)
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	if _, err := temporary.Write(canonical); err != nil {
		return "", errors.Join(err, temporary.Close(), removeCleanupTrustTemporary(temporaryPath))
	}
	if err := temporary.Sync(); err != nil {
		return "", errors.Join(err, temporary.Close(), removeCleanupTrustTemporary(temporaryPath))
	}
	if err := temporary.Close(); err != nil {
		return "", errors.Join(err, removeCleanupTrustTemporary(temporaryPath))
	}
	if expectedCurrentDigest == nil {
		if err := os.Link(temporaryPath, target); err != nil {
			cleanupErr := removeCleanupTrustTemporary(temporaryPath)
			if errors.Is(err, os.ErrExist) {
				return "", errors.Join(errors.New("candidate-control trust bundle was concurrently enrolled"), cleanupErr)
			}
			return "", errors.Join(fmt.Errorf("create candidate-control trust bundle: %w", err), cleanupErr)
		}
		if err := removeCleanupTrustTemporary(temporaryPath); err != nil {
			return "", fmt.Errorf("remove candidate-control trust bundle temporary link: %w", err)
		}
	} else if err := atomicReplaceFile(temporaryPath, target); err != nil {
		return "", errors.Join(
			fmt.Errorf("replace candidate-control trust bundle: %w", err),
			removeCleanupTrustTemporary(temporaryPath),
		)
	}
	installed, err := readProtectedFile(target, maxCleanupSecurityFile)
	if err != nil {
		return "", fmt.Errorf("read back candidate-control trust bundle: %w", err)
	}
	if !bytes.Equal(installed, canonical) || cleanupTrustDigest(installed) != digest {
		return "", errors.New("candidate-control trust bundle readback mismatch")
	}
	return digest, nil
}

func decodeAndValidateCandidateControlTrustBundle(data []byte, now time.Time) (candidateControlTrustBundle, error) {
	var bundle candidateControlTrustBundle
	if err := decodeStrictJSON(data, &bundle); err != nil {
		return candidateControlTrustBundle{}, err
	}
	if err := validateCandidateControlTrustBundle(bundle, now); err != nil {
		return candidateControlTrustBundle{}, err
	}
	return bundle, nil
}

func validateCandidateControlTrustBundle(bundle candidateControlTrustBundle, now time.Time) error {
	if bundle.SchemaVersion != candidateControlTrustBundleSchema {
		return fmt.Errorf("unsupported candidate-control trust bundle schema_version %d", bundle.SchemaVersion)
	}
	capabilityKey, err := bundle.CapabilityRoot.PublicKeyForPurpose(
		cleanup.CandidateControlIssuerPurpose,
		bundle.CapabilityRoot.Issuer,
		now,
	)
	if err != nil {
		return fmt.Errorf("validate candidate-control capability root: %w", err)
	}
	readbackKey, err := bundle.ReadbackRoot.PublicKeyForPurpose(
		cleanup.CandidateControlReadbackPurpose,
		bundle.ReadbackRoot.Issuer,
		now,
	)
	if err != nil {
		return fmt.Errorf("validate candidate-control readback root: %w", err)
	}
	if bundle.CapabilityRoot.RootID == bundle.ReadbackRoot.RootID ||
		bundle.CapabilityRoot.Issuer == bundle.ReadbackRoot.Issuer ||
		bytes.Equal(capabilityKey, readbackKey) {
		return errors.New("candidate-control capability and readback trust roots must use separate IDs, issuers, and keys")
	}
	return nil
}

func validateCandidateControlTrustRotation(current, next candidateControlTrustBundle) error {
	if err := validateCleanupRootRotation(current.CapabilityRoot, next.CapabilityRoot, "candidate-control capability"); err != nil {
		return err
	}
	return validateCleanupRootRotation(current.ReadbackRoot, next.ReadbackRoot, "candidate-control readback")
}

func canonicalCandidateControlTrustBundle(bundle candidateControlTrustBundle) ([]byte, error) {
	canonical, err := json.Marshal(bundle)
	if err != nil {
		return nil, errors.New("encode candidate-control trust bundle")
	}
	return append(canonical, '\n'), nil
}
