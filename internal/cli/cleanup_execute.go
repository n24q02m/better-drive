package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
	"github.com/n24q02m/better-drive/internal/driveapi"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/n24q02m/better-drive/internal/protectedfs"
)

const (
	cleanupBrokerConfigSchema    = 1
	maxCleanupSecurityFile       = 64 << 10
	maxCleanupPEMBytes           = 1 << 20
	maxDriveOAuthCredentialBytes = 64 << 10
	cleanupMTLSCertFDEnv         = "BETTER_DRIVE_CLEANUP_MTLS_CERT_FD"
	cleanupMTLSKeyFDEnv          = "BETTER_DRIVE_CLEANUP_MTLS_KEY_FD"
	driveOAuthCredentialFDEnv    = "BETTER_DRIVE_DRIVE_OAUTH_CREDENTIAL_FD"
)

type cleanupBrokerConfig struct {
	SchemaVersion int    `json:"schema_version"`
	Endpoint      string `json:"endpoint"`
	Repository    string `json:"repository"`
	Authority     string `json:"authority"`
	Owner         string `json:"owner"`
}

func readDriveOAuthTokenSource(client *http.Client) (driveapi.AccessTokenSource, error) {
	data, err := readSecretFD(driveOAuthCredentialFDEnv, maxDriveOAuthCredentialBytes)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(data)
	credential, err := driveapi.DecodeOAuthCredential(data)
	if err != nil {
		return nil, err
	}
	return driveapi.NewGoogleOAuthTokenSource(client, credential)
}

func executeProtectedCleanup(ctx context.Context, manifest cleanup.Manifest, validation cleanup.Validation, approvalID string) (driveapi.QuarantineExecutionResult, error) {
	if strings.TrimSpace(approvalID) == "" {
		return driveapi.QuarantineExecutionResult{}, errors.New("protected provider broker execution requires --approval-id")
	}
	bundle, err := readProtectedTrustBundle()
	if err != nil {
		return driveapi.QuarantineExecutionResult{}, err
	}
	config := bundle.Broker
	authorityPublicKey, err := bundle.AuthorityRoot.PublicKeyForPurpose(cleanup.OwnerRiskAuthorityPurpose, config.Authority, time.Now().UTC())
	if err != nil {
		return driveapi.QuarantineExecutionResult{}, err
	}
	brokerClient, err := newCleanupBrokerHTTPClient(bundle.BrokerServerCAPEM)
	if err != nil {
		return driveapi.QuarantineExecutionResult{}, err
	}
	authority, err := cleanup.NewOwnerRiskHTTPAuthority(brokerClient, config.Endpoint)
	if err != nil {
		return driveapi.QuarantineExecutionResult{}, err
	}
	requestID, err := newCleanupExecutionID("request")
	if err != nil {
		return driveapi.QuarantineExecutionResult{}, err
	}
	snapshotRequest := cleanup.OwnerRiskSnapshotRequest{
		SchemaVersion:    cleanup.CurrentOwnerRiskSchemaVersion,
		Repository:       config.Repository,
		ApprovalID:       approvalID,
		ManifestDigest:   validation.ManifestDigest,
		QuarantineTarget: manifest.QuarantineTarget,
		RequestID:        requestID,
	}
	snapshot, err := authority.SnapshotOwnerRisk(ctx, snapshotRequest)
	if err != nil {
		return driveapi.QuarantineExecutionResult{}, fmt.Errorf("read protected cleanup snapshot: %w", err)
	}
	now := time.Now().UTC()
	if err := cleanup.VerifyOwnerRiskSnapshot(snapshot, snapshotRequest, authorityPublicKey, now); err != nil {
		return driveapi.QuarantineExecutionResult{}, fmt.Errorf("verify protected cleanup snapshot: %w", err)
	}
	if snapshot.Authority != config.Authority {
		return driveapi.QuarantineExecutionResult{}, errors.New("cleanup snapshot authority does not match protected configuration")
	}
	approvalPublicKey, err := bundle.ApprovalRoot.PublicKeyForPurpose(cleanup.CleanupTrustPurpose, snapshot.Intent.Approval.Issuer, now)
	if err != nil {
		return driveapi.QuarantineExecutionResult{}, err
	}
	driveClient := &http.Client{Timeout: 30 * time.Second}
	tokenSource, err := readDriveOAuthTokenSource(driveClient)
	if err != nil {
		return driveapi.QuarantineExecutionResult{}, err
	}
	executor, err := driveapi.NewQuarantineExecutorWithTokenSource(
		driveClient,
		tokenSource,
		authority,
		approvalPublicKey,
		authorityPublicKey,
		config.Authority,
	)
	if err != nil {
		return driveapi.QuarantineExecutionResult{}, err
	}
	executionID, err := newCleanupExecutionID("execution")
	if err != nil {
		return driveapi.QuarantineExecutionResult{}, err
	}
	result, err := executor.Execute(ctx, driveapi.QuarantineExecutionRequest{
		Repository:         config.Repository,
		Manifest:           manifest,
		Intent:             snapshot.Intent,
		IntentOID:          snapshot.IntentOID,
		StateExpectedOID:   snapshot.StateOID,
		JournalExpectedOID: snapshot.JournalOID,
		LeaseExpectedOID:   snapshot.LeaseOID,
		Owner:              config.Owner,
		ExecutionID:        executionID,
		RequestID:          requestID,
	})
	if err != nil && result.ClaimID != "" {
		return result, fmt.Errorf(
			"protected cleanup reconciliation evidence claim=%s settlement=%s outcome=%s: %w",
			result.ClaimID,
			result.Settlement,
			result.OutcomeDigest,
			err,
		)
	}
	return result, err
}

func readCleanupBrokerConfig() (cleanupBrokerConfig, error) {
	bundle, err := readProtectedTrustBundle()
	if err != nil {
		return cleanupBrokerConfig{}, err
	}
	return bundle.Broker, nil
}

func readProtectedFile(path string, maximum int64) ([]byte, error) {
	securityRoot, err := filepath.Abs(paths.CleanupSecurityDir())
	if err != nil {
		return nil, err
	}
	if err := protectedfs.VerifyPrivateDir(securityRoot); err != nil {
		return nil, fmt.Errorf("verify cleanup security directory: %w", err)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolutePath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("cleanup security file must not be a symlink")
	}
	resolvedRoot, err := filepath.EvalSymlinks(securityRoot)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, errors.New("cleanup security file is outside the protected directory")
	}
	file, err := protectedfs.OpenPrivateFile(resolvedPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := protectedfs.VerifyPrivateFile(file); err != nil {
		return nil, fmt.Errorf("verify cleanup security file: %w", err)
	}
	info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("cleanup security file type or size is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || len(data) == 0 || int64(len(data)) > maximum {
		return nil, errors.New("cleanup security file changed or exceeds its size bound")
	}
	return data, nil
}

func newCleanupBrokerHTTPClient(serverCAPEM string) (*http.Client, error) {
	certificatePEM, err := readSecretFD(cleanupMTLSCertFDEnv, maxCleanupPEMBytes)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(certificatePEM)
	privateKeyPEM, err := readSecretFD(cleanupMTLSKeyFDEnv, maxCleanupPEMBytes)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(privateKeyPEM)
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		return nil, errors.New("cleanup broker mTLS identity is invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	serverRoots, err := newCleanupCAPool([]byte(serverCAPEM), "server", time.Now().UTC())
	if err != nil {
		return nil, err
	}
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      serverRoots,
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}, nil
}

func readSecretFD(environmentName string, maximum int64) ([]byte, error) {
	value := strings.TrimSpace(os.Getenv(environmentName))
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 {
		return nil, fmt.Errorf("%s must name an inherited secret file descriptor", environmentName)
	}
	file := os.NewFile(uintptr(fd), environmentName)
	if file == nil {
		return nil, fmt.Errorf("%s is not an open file descriptor", environmentName)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || len(data) == 0 || int64(len(data)) > maximum {
		return nil, fmt.Errorf("%s secret input is unreadable or exceeds its bound", environmentName)
	}
	data = bytes.TrimSuffix(data, []byte("\n"))
	data = bytes.TrimSuffix(data, []byte("\r"))
	if len(data) == 0 {
		return nil, fmt.Errorf("%s secret input is empty", environmentName)
	}
	return data, nil
}

func decodeStrictJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func newCleanupExecutionID(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", errors.New("generate cleanup execution identifier")
	}
	return prefix + "-" + hex.EncodeToString(random[:]), nil
}

func zeroBytes(data []byte) {
	for index := range data {
		data[index] = 0
	}
}
