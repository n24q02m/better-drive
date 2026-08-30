package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	cleanupTrustBundleSchema = 1
	cleanupTrustBundleFDEnv  = "BETTER_DRIVE_CLEANUP_TRUST_BUNDLE_FD"
)

type cleanupTrustBundle struct {
	SchemaVersion     int                 `json:"schema_version"`
	ApprovalRoot      cleanup.TrustRoot   `json:"approval_root"`
	AuthorityRoot     cleanup.TrustRoot   `json:"authority_root"`
	Broker            cleanupBrokerConfig `json:"broker"`
	BrokerServerCAPEM string              `json:"broker_server_ca_pem"`
}

func cleanupTrustCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "trust",
		Short: "Enroll or rotate the protected cleanup trust bundle",
		Long:  "Install the public approval root, authority root, and broker binding as one fixed-path protected bundle. Input is accepted only through an inherited file descriptor.",
	}
	command.AddCommand(cleanupTrustEnrollCmd(), cleanupTrustRotateCmd())
	return command
}

func cleanupTrustEnrollCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enroll",
		Short: "Create the protected cleanup trust bundle",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			data, err := readSecretFD(cleanupTrustBundleFDEnv, maxCleanupSecurityFile)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			defer zeroBytes(data)
			digest, err := writeCleanupTrustBundle(data, nil)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "cleanup trust bundle enrolled: sha256=%s\n", digest)
			return err
		},
	}
}

func cleanupTrustRotateCmd() *cobra.Command {
	var expectedCurrentDigest string
	command := &cobra.Command{
		Use:   "rotate",
		Short: "CAS-rotate the protected cleanup trust bundle",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			expectedCurrentDigest = strings.ToLower(strings.TrimSpace(expectedCurrentDigest))
			if !isSHA256Hex(expectedCurrentDigest) {
				return exitcode.ConfigError(errors.New("--expected-current-digest must be a 64-character SHA-256 hex digest"))
			}
			data, err := readSecretFD(cleanupTrustBundleFDEnv, maxCleanupSecurityFile)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			defer zeroBytes(data)
			digest, err := writeCleanupTrustBundle(data, &expectedCurrentDigest)
			if err != nil {
				return exitcode.ConfigError(err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "cleanup trust bundle rotated: sha256=%s\n", digest)
			return err
		},
	}
	command.Flags().StringVar(&expectedCurrentDigest, "expected-current-digest", "", "exact SHA-256 of the currently enrolled trust bundle")
	_ = command.MarkFlagRequired("expected-current-digest")
	return command
}

func readProtectedTrustBundle() (cleanupTrustBundle, error) {
	data, err := readProtectedFile(paths.CleanupTrustBundleFile(), maxCleanupSecurityFile)
	if err != nil {
		return cleanupTrustBundle{}, fmt.Errorf("read protected cleanup trust bundle: %w", err)
	}
	bundle, err := decodeAndValidateCleanupTrustBundle(data, time.Now().UTC())
	if err != nil {
		return cleanupTrustBundle{}, fmt.Errorf("decode protected cleanup trust bundle: %w", err)
	}
	return bundle, nil
}

func writeCleanupTrustBundle(data []byte, expectedCurrentDigest *string) (string, error) {
	bundle, err := decodeAndValidateCleanupTrustBundle(data, time.Now().UTC())
	if err != nil {
		return "", err
	}
	canonical, err := json.Marshal(bundle)
	if err != nil {
		return "", errors.New("encode cleanup trust bundle")
	}
	canonical = append(canonical, '\n')
	digest := cleanupTrustDigest(canonical)

	securityRoot, err := ensureCleanupSecurityDir()
	if err != nil {
		return "", err
	}
	lock, err := acquireCleanupTrustLock(securityRoot)
	if err != nil {
		return "", err
	}
	result, installErr := installCleanupTrustBundleLocked(
		securityRoot,
		canonical,
		digest,
		expectedCurrentDigest,
	)
	releaseErr := lock.release()
	if releaseErr != nil {
		return "", errors.Join(installErr, fmt.Errorf("release cleanup trust bundle lock: %w", releaseErr))
	}
	return result, installErr
}

func installCleanupTrustBundleLocked(
	securityRoot string,
	canonical []byte,
	digest string,
	expectedCurrentDigest *string,
) (string, error) {
	target := paths.CleanupTrustBundleFile()
	if expectedCurrentDigest == nil {
		if _, err := os.Lstat(target); err == nil {
			return "", errors.New("cleanup trust bundle is already enrolled; use trust rotate with its exact current digest")
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	} else {
		expected := strings.ToLower(strings.TrimSpace(*expectedCurrentDigest))
		if !isSHA256Hex(expected) {
			return "", errors.New("expected current cleanup trust bundle digest is invalid")
		}
		currentData, err := readProtectedFile(target, maxCleanupSecurityFile)
		if err != nil {
			return "", err
		}
		if cleanupTrustDigest(currentData) != expected {
			return "", errors.New("cleanup trust bundle changed since the expected current digest")
		}
		current, err := decodeAndValidateCleanupTrustBundle(currentData, time.Now().UTC())
		if err != nil {
			return "", fmt.Errorf("validate current cleanup trust bundle: %w", err)
		}
		bundle, err := decodeAndValidateCleanupTrustBundle(canonical, time.Now().UTC())
		if err != nil {
			return "", err
		}
		if err := validateCleanupTrustRotation(current, bundle); err != nil {
			return "", err
		}
		if digest == expected {
			return "", errors.New("cleanup trust bundle rotation is identical to the current bundle")
		}
	}

	temporary, err := protectedfs.CreatePrivateFile(
		filepath.Join(securityRoot, fmt.Sprintf(".trust-bundle-%d.tmp", time.Now().UnixNano())),
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
				return "", errors.Join(errors.New("cleanup trust bundle was concurrently enrolled"), cleanupErr)
			}
			return "", errors.Join(fmt.Errorf("create cleanup trust bundle: %w", err), cleanupErr)
		}
		if err := removeCleanupTrustTemporary(temporaryPath); err != nil {
			return "", fmt.Errorf("remove cleanup trust bundle temporary link: %w", err)
		}
	} else if err := atomicReplaceFile(temporaryPath, target); err != nil {
		return "", errors.Join(
			fmt.Errorf("replace cleanup trust bundle: %w", err),
			removeCleanupTrustTemporary(temporaryPath),
		)
	}
	installed, err := readProtectedFile(target, maxCleanupSecurityFile)
	if err != nil {
		return "", fmt.Errorf("read back cleanup trust bundle: %w", err)
	}
	if cleanupTrustDigest(installed) != digest {
		return "", errors.New("cleanup trust bundle readback digest mismatch")
	}
	return digest, nil
}

func removeCleanupTrustTemporary(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func decodeAndValidateCleanupTrustBundle(data []byte, now time.Time) (cleanupTrustBundle, error) {
	var bundle cleanupTrustBundle
	if err := decodeStrictJSON(data, &bundle); err != nil {
		return cleanupTrustBundle{}, err
	}
	if err := validateCleanupTrustBundle(bundle, now); err != nil {
		return cleanupTrustBundle{}, err
	}
	return bundle, nil
}

func validateCleanupTrustBundle(bundle cleanupTrustBundle, now time.Time) error {
	if bundle.SchemaVersion != cleanupTrustBundleSchema {
		return fmt.Errorf("unsupported cleanup trust bundle schema_version %d", bundle.SchemaVersion)
	}
	if bundle.Broker.SchemaVersion != cleanupBrokerConfigSchema ||
		strings.TrimSpace(bundle.Broker.Repository) == "" ||
		strings.TrimSpace(bundle.Broker.Authority) == "" ||
		strings.TrimSpace(bundle.Broker.Owner) == "" ||
		strings.ContainsAny(bundle.Broker.Authority+bundle.Broker.Owner, "/\\\x00\r\n\t") {
		return errors.New("protected cleanup broker config is invalid")
	}
	if _, err := cleanupBrokerListenAddress(bundle.Broker); err != nil {
		return err
	}
	if err := cleanup.ValidateOwnerRiskRepository(bundle.Broker.Repository); err != nil {
		return err
	}
	if _, err := newCleanupCAPool([]byte(bundle.BrokerServerCAPEM), "server", now); err != nil {
		return err
	}
	approvalKey, err := bundle.ApprovalRoot.PublicKeyForPurpose(cleanup.CleanupTrustPurpose, bundle.ApprovalRoot.Issuer, now)
	if err != nil {
		return fmt.Errorf("validate cleanup approval trust root: %w", err)
	}
	authorityKey, err := bundle.AuthorityRoot.PublicKeyForPurpose(cleanup.OwnerRiskAuthorityPurpose, bundle.Broker.Authority, now)
	if err != nil {
		return fmt.Errorf("validate cleanup authority trust root: %w", err)
	}
	if bundle.ApprovalRoot.RootID == bundle.AuthorityRoot.RootID ||
		bundle.ApprovalRoot.Issuer == bundle.AuthorityRoot.Issuer ||
		bytes.Equal(approvalKey, authorityKey) {
		return errors.New("cleanup approval and authority trust roots must use separate IDs, issuers, and keys")
	}
	return nil
}

func validateCleanupTrustRotation(current, next cleanupTrustBundle) error {
	if err := validateCleanupRootRotation(current.ApprovalRoot, next.ApprovalRoot, "approval"); err != nil {
		return err
	}
	return validateCleanupRootRotation(current.AuthorityRoot, next.AuthorityRoot, "authority")
}

func validateCleanupRootRotation(current, next cleanup.TrustRoot, purpose string) error {
	if current.Fingerprint == next.Fingerprint {
		if current != next {
			return fmt.Errorf("unchanged cleanup %s key must retain its exact trust-root record", purpose)
		}
		return nil
	}
	if !next.EnrolledAt.After(current.EnrolledAt) {
		return fmt.Errorf("rotated cleanup %s trust root must have a later enrollment time", purpose)
	}
	return nil
}

func cleanupTrustDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func isSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func ensureCleanupSecurityDir() (string, error) {
	root, err := filepath.Abs(paths.CleanupSecurityDir())
	if err != nil {
		return "", err
	}
	if err := protectedfs.EnsurePrivateDir(root); err != nil {
		return "", fmt.Errorf("protect cleanup security root: %w", err)
	}
	return root, nil
}

type cleanupTrustLock struct {
	file *os.File
	path string
}

func acquireCleanupTrustLock(securityRoot string) (*cleanupTrustLock, error) {
	path := filepath.Join(securityRoot, ".trust-bundle.lock")
	file, err := protectedfs.CreatePrivateFile(path)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, errors.New("cleanup trust bundle enrollment is already in progress; remove the lock only after verifying no enrollment process is running")
		}
		return nil, err
	}
	if _, err := io.WriteString(file, fmt.Sprintf("pid=%d\n", os.Getpid())); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return &cleanupTrustLock{file: file, path: path}, nil
}

func (lock *cleanupTrustLock) release() error {
	if lock == nil {
		return nil
	}
	var closeErr error
	if lock.file != nil {
		closeErr = lock.file.Close()
	}
	removeErr := os.Remove(lock.path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(closeErr, removeErr)
}
