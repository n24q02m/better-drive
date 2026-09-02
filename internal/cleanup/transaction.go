package cleanup

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const CurrentOwnerRiskSchemaVersion = 1

const (
	OwnerRiskClaimed             = ApprovalClaimed
	OwnerRiskConsumed            = ApprovalConsumed
	OwnerRiskNeedsReconciliation = ApprovalNeedsReconciliation
)

const OwnerRiskOperationQuarantine = "quarantine"

var (
	gitOIDPattern     = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

// ValidateOwnerRiskRepository accepts one exact owner/repository identity.
func ValidateOwnerRiskRepository(repository string) error {
	if !repositoryPattern.MatchString(repository) {
		return errors.New("owner-risk repository must be exact owner/repository")
	}
	return nil
}

// OwnerRiskAuthority is the only authority accepted by the Drive cleanup
// executor. ClaimOwnerRisk must atomically compare and update the state,
// journal, and destination-lease refs in authoritative private Git before it
// returns a signed readback. It must reject every replay of the approval nonce
// and request ID. SettleOwnerRisk performs the corresponding atomic settlement.
type OwnerRiskAuthority interface {
	ClaimOwnerRisk(context.Context, OwnerRiskClaimRequest) (OwnerRiskClaim, error)
	RecheckOwnerRisk(context.Context, OwnerRiskFenceRequest) (OwnerRiskFenceReadback, error)
	SettleOwnerRisk(context.Context, OwnerRiskSettlementRequest) (OwnerRiskSettlement, error)
}

type OwnerRiskClaimRequest struct {
	SchemaVersion      int              `json:"schema_version"`
	Repository         string           `json:"repository"`
	ApprovalID         string           `json:"approval_id"`
	ManifestDigest     string           `json:"manifest_digest"`
	IntentRef          string           `json:"intent_ref"`
	IntentOID          string           `json:"intent_oid"`
	StateRef           string           `json:"state_ref"`
	StateExpectedOID   string           `json:"state_expected_oid"`
	JournalRef         string           `json:"journal_ref"`
	JournalExpectedOID string           `json:"journal_expected_oid,omitempty"`
	Operation          string           `json:"operation"`
	LeaseRef           string           `json:"lease_ref"`
	LeaseExpectedOID   string           `json:"lease_expected_oid,omitempty"`
	MutationSemantics  string           `json:"mutation_semantics"`
	QuarantineTarget   QuarantineTarget `json:"quarantine_target"`
	MaxObjects         int              `json:"max_objects"`
	MaxBytes           int64            `json:"max_bytes"`
	ExpiresAt          time.Time        `json:"expires_at"`
	Nonce              string           `json:"nonce"`
	Owner              string           `json:"owner"`
	ExecutionID        string           `json:"execution_id"`
	RequestID          string           `json:"request_id"`
}

type OwnerRiskClaim struct {
	SchemaVersion int                   `json:"schema_version"`
	ClaimID       string                `json:"claim_id"`
	Request       OwnerRiskClaimRequest `json:"request"`
	RequestDigest string                `json:"request_digest"`
	State         string                `json:"state"`
	StateOID      string                `json:"state_oid"`
	JournalOID    string                `json:"journal_oid"`
	LeaseOID      string                `json:"lease_oid"`
	Generation    uint64                `json:"generation"`
	Fence         uint64                `json:"fence"`
	Atomic        bool                  `json:"atomic"`
	Authority     string                `json:"authority"`
	IssuedAt      time.Time             `json:"issued_at"`
	SignatureHex  string                `json:"signature"`
}

type OwnerRiskFenceRequest struct {
	SchemaVersion int    `json:"schema_version"`
	ClaimID       string `json:"claim_id"`
	ClaimDigest   string `json:"claim_digest"`
	ApprovalID    string `json:"approval_id"`
	ExecutionID   string `json:"execution_id"`
	RequestID     string `json:"request_id"`
	StateOID      string `json:"state_oid"`
	JournalOID    string `json:"journal_oid"`
	LeaseOID      string `json:"lease_oid"`
	Generation    uint64 `json:"generation"`
	Fence         uint64 `json:"fence"`
}

type OwnerRiskFenceReadback struct {
	SchemaVersion int                   `json:"schema_version"`
	Request       OwnerRiskFenceRequest `json:"request"`
	RequestDigest string                `json:"request_digest"`
	State         string                `json:"state"`
	Authority     string                `json:"authority"`
	ObservedAt    time.Time             `json:"observed_at"`
	SignatureHex  string                `json:"signature"`
}

type OwnerRiskSettlementRequest struct {
	SchemaVersion      int      `json:"schema_version"`
	ClaimID            string   `json:"claim_id"`
	ClaimDigest        string   `json:"claim_digest"`
	ApprovalID         string   `json:"approval_id"`
	ExecutionID        string   `json:"execution_id"`
	RequestID          string   `json:"request_id"`
	StateExpectedOID   string   `json:"state_expected_oid"`
	JournalExpectedOID string   `json:"journal_expected_oid"`
	LeaseExpectedOID   string   `json:"lease_expected_oid"`
	Settlement         string   `json:"settlement"`
	OutcomeDigest      string   `json:"outcome_digest"`
	ProviderRequests   []string `json:"provider_requests"`
}

type OwnerRiskSettlement struct {
	SchemaVersion int                        `json:"schema_version"`
	Request       OwnerRiskSettlementRequest `json:"request"`
	RequestDigest string                     `json:"request_digest"`
	State         string                     `json:"state"`
	StateOID      string                     `json:"state_oid"`
	JournalOID    string                     `json:"journal_oid"`
	LeaseOID      string                     `json:"lease_oid"`
	Authority     string                     `json:"authority"`
	ObservedAt    time.Time                  `json:"observed_at"`
	SignatureHex  string                     `json:"signature"`
}

func QuarantineIdentityHash(target QuarantineTarget) (string, error) {
	if err := validateQuarantineTarget(target, target.AccountID); err != nil {
		return "", err
	}
	canonical, err := marshalCanonical(target)
	if err != nil {
		return "", err
	}
	return Digest(canonical), nil
}

func CanonicalOwnerRiskClaimRequest(request OwnerRiskClaimRequest) ([]byte, error) {
	if err := validateOwnerRiskClaimRequest(request); err != nil {
		return nil, err
	}
	return marshalCanonical(request)
}

func CanonicalOwnerRiskClaim(claim OwnerRiskClaim) ([]byte, error) {
	claim.SignatureHex = ""
	if err := validateOwnerRiskClaimShape(claim); err != nil {
		return nil, err
	}
	return marshalCanonical(claim)
}

func SignOwnerRiskClaim(claim OwnerRiskClaim, privateKey ed25519.PrivateKey) (OwnerRiskClaim, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return OwnerRiskClaim{}, errors.New("owner-risk authority private key is invalid")
	}
	canonical, err := CanonicalOwnerRiskClaim(claim)
	if err != nil {
		return OwnerRiskClaim{}, err
	}
	claim.SignatureHex = hex.EncodeToString(ed25519.Sign(privateKey, canonical))
	return claim, nil
}

func VerifyOwnerRiskClaim(claim OwnerRiskClaim, expected OwnerRiskClaimRequest, publicKey ed25519.PublicKey, now time.Time) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("owner-risk authority public key is invalid")
	}
	expectedCanonical, err := CanonicalOwnerRiskClaimRequest(expected)
	if err != nil {
		return fmt.Errorf("expected owner-risk claim request is invalid: %w", err)
	}
	requestCanonical, err := CanonicalOwnerRiskClaimRequest(claim.Request)
	if err != nil {
		return fmt.Errorf("owner-risk claim request is invalid: %w", err)
	}
	if string(requestCanonical) != string(expectedCanonical) || claim.RequestDigest != Digest(expectedCanonical) {
		return errors.New("owner-risk claim does not bind the exact request")
	}
	if err := validateOwnerRiskClaimShape(claim); err != nil {
		return err
	}
	if !now.Before(claim.Request.ExpiresAt) || claim.IssuedAt.After(now.Add(time.Minute)) || claim.IssuedAt.After(claim.Request.ExpiresAt) {
		return errors.New("owner-risk claim is expired or has an invalid issuance time")
	}
	signature, err := decodeEd25519Signature(claim.SignatureHex)
	if err != nil {
		return err
	}
	canonical, err := CanonicalOwnerRiskClaim(claim)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, canonical, signature) {
		return errors.New("owner-risk claim signature verification failed")
	}
	return nil
}

func CanonicalOwnerRiskFenceRequest(request OwnerRiskFenceRequest) ([]byte, error) {
	if err := validateOwnerRiskFenceRequest(request); err != nil {
		return nil, err
	}
	return marshalCanonical(request)
}

func CanonicalOwnerRiskFenceReadback(readback OwnerRiskFenceReadback) ([]byte, error) {
	readback.SignatureHex = ""
	if err := validateOwnerRiskFenceReadback(readback); err != nil {
		return nil, err
	}
	return marshalCanonical(readback)
}

func SignOwnerRiskFenceReadback(readback OwnerRiskFenceReadback, privateKey ed25519.PrivateKey) (OwnerRiskFenceReadback, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return OwnerRiskFenceReadback{}, errors.New("owner-risk authority private key is invalid")
	}
	canonical, err := CanonicalOwnerRiskFenceReadback(readback)
	if err != nil {
		return OwnerRiskFenceReadback{}, err
	}
	readback.SignatureHex = hex.EncodeToString(ed25519.Sign(privateKey, canonical))
	return readback, nil
}

func VerifyOwnerRiskFenceReadback(readback OwnerRiskFenceReadback, expected OwnerRiskFenceRequest, publicKey ed25519.PublicKey, now time.Time) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("owner-risk authority public key is invalid")
	}
	expectedCanonical, err := CanonicalOwnerRiskFenceRequest(expected)
	if err != nil {
		return fmt.Errorf("expected owner-risk fence request is invalid: %w", err)
	}
	requestCanonical, err := CanonicalOwnerRiskFenceRequest(readback.Request)
	if err != nil {
		return fmt.Errorf("owner-risk fence request is invalid: %w", err)
	}
	if string(requestCanonical) != string(expectedCanonical) || readback.RequestDigest != Digest(expectedCanonical) {
		return errors.New("owner-risk fence readback does not bind the exact request")
	}
	if err := validateOwnerRiskFenceReadback(readback); err != nil {
		return err
	}
	if readback.ObservedAt.After(now.Add(time.Minute)) || readback.ObservedAt.Before(now.Add(-30*time.Second)) {
		return errors.New("owner-risk fence readback is stale or has an invalid observation time")
	}
	signature, err := decodeEd25519Signature(readback.SignatureHex)
	if err != nil {
		return err
	}
	canonical, err := CanonicalOwnerRiskFenceReadback(readback)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, canonical, signature) {
		return errors.New("owner-risk fence readback signature verification failed")
	}
	return nil
}

func CanonicalOwnerRiskSettlementRequest(request OwnerRiskSettlementRequest) ([]byte, error) {
	if err := validateOwnerRiskSettlementRequest(request); err != nil {
		return nil, err
	}
	return marshalCanonical(request)
}

func CanonicalOwnerRiskSettlement(settlement OwnerRiskSettlement) ([]byte, error) {
	settlement.SignatureHex = ""
	if err := validateOwnerRiskSettlementShape(settlement); err != nil {
		return nil, err
	}
	return marshalCanonical(settlement)
}

func SignOwnerRiskSettlement(settlement OwnerRiskSettlement, privateKey ed25519.PrivateKey) (OwnerRiskSettlement, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return OwnerRiskSettlement{}, errors.New("owner-risk authority private key is invalid")
	}
	canonical, err := CanonicalOwnerRiskSettlement(settlement)
	if err != nil {
		return OwnerRiskSettlement{}, err
	}
	settlement.SignatureHex = hex.EncodeToString(ed25519.Sign(privateKey, canonical))
	return settlement, nil
}

func VerifyOwnerRiskSettlement(settlement OwnerRiskSettlement, expected OwnerRiskSettlementRequest, publicKey ed25519.PublicKey, now time.Time) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("owner-risk authority public key is invalid")
	}
	expectedCanonical, err := CanonicalOwnerRiskSettlementRequest(expected)
	if err != nil {
		return fmt.Errorf("expected owner-risk settlement request is invalid: %w", err)
	}
	requestCanonical, err := CanonicalOwnerRiskSettlementRequest(settlement.Request)
	if err != nil {
		return fmt.Errorf("owner-risk settlement request is invalid: %w", err)
	}
	if string(requestCanonical) != string(expectedCanonical) || settlement.RequestDigest != Digest(expectedCanonical) {
		return errors.New("owner-risk settlement does not bind the exact request")
	}
	if err := validateOwnerRiskSettlementShape(settlement); err != nil {
		return err
	}
	if settlement.ObservedAt.After(now.Add(time.Minute)) {
		return errors.New("owner-risk settlement observation time is invalid")
	}
	signature, err := decodeEd25519Signature(settlement.SignatureHex)
	if err != nil {
		return err
	}
	canonical, err := CanonicalOwnerRiskSettlement(settlement)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, canonical, signature) {
		return errors.New("owner-risk settlement signature verification failed")
	}
	return nil
}

func validateOwnerRiskClaimRequest(request OwnerRiskClaimRequest) error {
	if request.SchemaVersion != CurrentOwnerRiskSchemaVersion {
		return fmt.Errorf("unsupported owner-risk claim schema version %d", request.SchemaVersion)
	}
	if err := ValidateOwnerRiskRepository(request.Repository); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"approval ID":  request.ApprovalID,
		"owner":        request.Owner,
		"execution ID": request.ExecutionID,
		"request ID":   request.RequestID,
		"nonce":        request.Nonce,
	} {
		if err := validateOpaqueTransactionField(value, name); err != nil {
			return err
		}
	}
	if err := validateSHA256Hex(request.ManifestDigest, "owner-risk manifest digest"); err != nil {
		return err
	}
	if request.IntentRef != IntentRef(request.ApprovalID) || request.StateRef != StateRef(request.ApprovalID) || request.JournalRef != JournalRef(request.ApprovalID) {
		return errors.New("owner-risk claim refs do not match approval ID")
	}
	identityHash, err := QuarantineIdentityHash(request.QuarantineTarget)
	if err != nil {
		return err
	}
	if request.LeaseRef != LeaseRef(identityHash) {
		return errors.New("owner-risk lease ref does not match quarantine target")
	}
	for name, oid := range map[string]string{
		"intent OID":         request.IntentOID,
		"state expected OID": request.StateExpectedOID,
	} {
		if !gitOIDPattern.MatchString(oid) {
			return fmt.Errorf("%s must be an exact Git OID", name)
		}
	}
	for name, oid := range map[string]string{
		"journal expected OID": request.JournalExpectedOID,
		"lease expected OID":   request.LeaseExpectedOID,
	} {
		if oid != "" && !gitOIDPattern.MatchString(oid) {
			return fmt.Errorf("%s must be empty or an exact Git OID", name)
		}
	}
	if request.Operation != OwnerRiskOperationQuarantine {
		return errors.New("owner-risk claim operation must be exact quarantine")
	}
	if request.MutationSemantics != MutationSemanticsDriveOwnerRisk {
		return errors.New("owner-risk claim must explicitly accept Drive no-CAS mutation semantics")
	}
	if request.MaxObjects <= 0 || request.MaxBytes < 0 {
		return errors.New("owner-risk claim budget is invalid")
	}
	if request.ExpiresAt.IsZero() {
		return errors.New("owner-risk claim expiry is required")
	}
	return nil
}

func validateOwnerRiskClaimShape(claim OwnerRiskClaim) error {
	if claim.SchemaVersion != CurrentOwnerRiskSchemaVersion || claim.State != OwnerRiskClaimed || !claim.Atomic {
		return errors.New("owner-risk claim is not an atomic claimed readback")
	}
	if err := validateOpaqueTransactionField(claim.ClaimID, "claim ID"); err != nil {
		return err
	}
	if err := validateOpaqueTransactionField(claim.Authority, "authority"); err != nil {
		return err
	}
	if _, err := CanonicalOwnerRiskClaimRequest(claim.Request); err != nil {
		return err
	}
	if err := validateSHA256Hex(claim.RequestDigest, "owner-risk request digest"); err != nil {
		return err
	}
	for name, oid := range map[string]string{"state OID": claim.StateOID, "journal OID": claim.JournalOID, "lease OID": claim.LeaseOID} {
		if !gitOIDPattern.MatchString(oid) {
			return fmt.Errorf("owner-risk %s must be an exact Git OID", name)
		}
	}
	if claim.StateOID == claim.Request.StateExpectedOID || claim.JournalOID == claim.Request.JournalExpectedOID || claim.LeaseOID == claim.Request.LeaseExpectedOID {
		return errors.New("owner-risk atomic claim did not advance every authoritative ref")
	}
	if claim.Generation == 0 || claim.Fence == 0 || claim.IssuedAt.IsZero() {
		return errors.New("owner-risk claim fence, generation, and issuance are required")
	}
	return nil
}

func validateOwnerRiskFenceRequest(request OwnerRiskFenceRequest) error {
	if request.SchemaVersion != CurrentOwnerRiskSchemaVersion {
		return fmt.Errorf("unsupported owner-risk fence schema version %d", request.SchemaVersion)
	}
	for name, value := range map[string]string{
		"claim ID":     request.ClaimID,
		"approval ID":  request.ApprovalID,
		"execution ID": request.ExecutionID,
		"request ID":   request.RequestID,
	} {
		if err := validateOpaqueTransactionField(value, name); err != nil {
			return err
		}
	}
	if err := validateSHA256Hex(request.ClaimDigest, "owner-risk claim digest"); err != nil {
		return err
	}
	for name, oid := range map[string]string{
		"state OID":   request.StateOID,
		"journal OID": request.JournalOID,
		"lease OID":   request.LeaseOID,
	} {
		if !gitOIDPattern.MatchString(oid) {
			return fmt.Errorf("owner-risk fence %s must be an exact Git OID", name)
		}
	}
	if request.Generation == 0 || request.Fence == 0 {
		return errors.New("owner-risk fence generation and token are required")
	}
	return nil
}

func validateOwnerRiskFenceReadback(readback OwnerRiskFenceReadback) error {
	if readback.SchemaVersion != CurrentOwnerRiskSchemaVersion || readback.State != OwnerRiskClaimed {
		return errors.New("owner-risk fence readback is not claimed")
	}
	if _, err := CanonicalOwnerRiskFenceRequest(readback.Request); err != nil {
		return err
	}
	if err := validateSHA256Hex(readback.RequestDigest, "owner-risk fence request digest"); err != nil {
		return err
	}
	if err := validateOpaqueTransactionField(readback.Authority, "authority"); err != nil {
		return err
	}
	if readback.ObservedAt.IsZero() {
		return errors.New("owner-risk fence observation time is required")
	}
	return nil
}

func validateOwnerRiskSettlementRequest(request OwnerRiskSettlementRequest) error {
	if request.SchemaVersion != CurrentOwnerRiskSchemaVersion {
		return fmt.Errorf("unsupported owner-risk settlement schema version %d", request.SchemaVersion)
	}
	for name, value := range map[string]string{
		"claim ID":     request.ClaimID,
		"approval ID":  request.ApprovalID,
		"execution ID": request.ExecutionID,
		"request ID":   request.RequestID,
	} {
		if err := validateOpaqueTransactionField(value, name); err != nil {
			return err
		}
	}
	if err := validateSHA256Hex(request.ClaimDigest, "owner-risk claim digest"); err != nil {
		return err
	}
	if err := validateSHA256Hex(request.OutcomeDigest, "owner-risk outcome digest"); err != nil {
		return err
	}
	for name, oid := range map[string]string{
		"state expected OID":   request.StateExpectedOID,
		"journal expected OID": request.JournalExpectedOID,
		"lease expected OID":   request.LeaseExpectedOID,
	} {
		if !gitOIDPattern.MatchString(oid) {
			return fmt.Errorf("%s must be an exact Git OID", name)
		}
	}
	if request.Settlement != OwnerRiskConsumed && request.Settlement != OwnerRiskNeedsReconciliation {
		return errors.New("owner-risk settlement state is invalid")
	}
	if len(request.ProviderRequests) > 10_000 {
		return errors.New("owner-risk provider request list exceeds bound")
	}
	for _, requestID := range request.ProviderRequests {
		if err := validateOpaqueTransactionField(requestID, "provider request ID"); err != nil {
			return err
		}
	}
	return nil
}

func validateOwnerRiskSettlementShape(settlement OwnerRiskSettlement) error {
	if settlement.SchemaVersion != CurrentOwnerRiskSchemaVersion || settlement.State != settlement.Request.Settlement {
		return errors.New("owner-risk settlement readback state is invalid")
	}
	if err := validateOpaqueTransactionField(settlement.Authority, "authority"); err != nil {
		return err
	}
	if _, err := CanonicalOwnerRiskSettlementRequest(settlement.Request); err != nil {
		return err
	}
	if err := validateSHA256Hex(settlement.RequestDigest, "owner-risk settlement request digest"); err != nil {
		return err
	}
	for name, oid := range map[string]string{"state OID": settlement.StateOID, "journal OID": settlement.JournalOID, "lease OID": settlement.LeaseOID} {
		if !gitOIDPattern.MatchString(oid) {
			return fmt.Errorf("owner-risk settlement %s must be an exact Git OID", name)
		}
	}
	if settlement.StateOID == settlement.Request.StateExpectedOID || settlement.JournalOID == settlement.Request.JournalExpectedOID || settlement.LeaseOID == settlement.Request.LeaseExpectedOID {
		return errors.New("owner-risk atomic settlement did not advance every authoritative ref")
	}
	if settlement.ObservedAt.IsZero() {
		return errors.New("owner-risk settlement observation time is required")
	}
	return nil
}

func validateOpaqueTransactionField(value, name string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "/\\\x00\r\n\t") {
		return fmt.Errorf("owner-risk %s is invalid", name)
	}
	return nil
}

func decodeEd25519Signature(value string) ([]byte, error) {
	signature, err := hex.DecodeString(value)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, errors.New("owner-risk authority signature is invalid")
	}
	return signature, nil
}
