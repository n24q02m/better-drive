package cleanup

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type OwnerRiskSnapshotAuthority interface {
	SnapshotOwnerRisk(context.Context, OwnerRiskSnapshotRequest) (OwnerRiskSnapshot, error)
}

type OwnerRiskSnapshotRequest struct {
	SchemaVersion    int              `json:"schema_version"`
	Repository       string           `json:"repository"`
	ApprovalID       string           `json:"approval_id"`
	ManifestDigest   string           `json:"manifest_digest"`
	QuarantineTarget QuarantineTarget `json:"quarantine_target"`
	RequestID        string           `json:"request_id"`
}

type OwnerRiskSnapshot struct {
	SchemaVersion int                      `json:"schema_version"`
	Request       OwnerRiskSnapshotRequest `json:"request"`
	RequestDigest string                   `json:"request_digest"`
	Intent        Intent                   `json:"intent"`
	IntentOID     string                   `json:"intent_oid"`
	StateOID      string                   `json:"state_oid"`
	JournalOID    string                   `json:"journal_oid,omitempty"`
	LeaseOID      string                   `json:"lease_oid,omitempty"`
	Authority     string                   `json:"authority"`
	ObservedAt    time.Time                `json:"observed_at"`
	SignatureHex  string                   `json:"signature"`
}

func CanonicalOwnerRiskSnapshotRequest(request OwnerRiskSnapshotRequest) ([]byte, error) {
	if err := validateOwnerRiskSnapshotRequest(request); err != nil {
		return nil, err
	}
	return marshalCanonical(request)
}

func CanonicalOwnerRiskSnapshot(snapshot OwnerRiskSnapshot) ([]byte, error) {
	snapshot.SignatureHex = ""
	if err := validateOwnerRiskSnapshotShape(snapshot); err != nil {
		return nil, err
	}
	return marshalCanonical(snapshot)
}

func SignOwnerRiskSnapshot(snapshot OwnerRiskSnapshot, privateKey ed25519.PrivateKey) (OwnerRiskSnapshot, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return OwnerRiskSnapshot{}, errors.New("owner-risk authority private key is invalid")
	}
	canonical, err := CanonicalOwnerRiskSnapshot(snapshot)
	if err != nil {
		return OwnerRiskSnapshot{}, err
	}
	snapshot.SignatureHex = hex.EncodeToString(ed25519.Sign(privateKey, canonical))
	return snapshot, nil
}

func VerifyOwnerRiskSnapshot(snapshot OwnerRiskSnapshot, expected OwnerRiskSnapshotRequest, publicKey ed25519.PublicKey, now time.Time) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("owner-risk authority public key is invalid")
	}
	expectedCanonical, err := CanonicalOwnerRiskSnapshotRequest(expected)
	if err != nil {
		return fmt.Errorf("expected owner-risk snapshot request is invalid: %w", err)
	}
	requestCanonical, err := CanonicalOwnerRiskSnapshotRequest(snapshot.Request)
	if err != nil {
		return fmt.Errorf("owner-risk snapshot request is invalid: %w", err)
	}
	if string(requestCanonical) != string(expectedCanonical) || snapshot.RequestDigest != Digest(expectedCanonical) {
		return errors.New("owner-risk snapshot does not bind the exact request")
	}
	if err := validateOwnerRiskSnapshotShape(snapshot); err != nil {
		return err
	}
	if snapshot.ObservedAt.After(now.Add(time.Minute)) || snapshot.ObservedAt.Before(now.Add(-30*time.Second)) {
		return errors.New("owner-risk snapshot is stale or has an invalid observation time")
	}
	signature, err := decodeEd25519Signature(snapshot.SignatureHex)
	if err != nil {
		return err
	}
	canonical, err := CanonicalOwnerRiskSnapshot(snapshot)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, canonical, signature) {
		return errors.New("owner-risk snapshot signature verification failed")
	}
	return nil
}

func validateOwnerRiskSnapshotRequest(request OwnerRiskSnapshotRequest) error {
	if request.SchemaVersion != CurrentOwnerRiskSchemaVersion {
		return fmt.Errorf("unsupported owner-risk snapshot schema version %d", request.SchemaVersion)
	}
	if err := ValidateOwnerRiskRepository(request.Repository); err != nil {
		return err
	}
	if err := validateOpaqueTransactionField(request.ApprovalID, "approval ID"); err != nil {
		return err
	}
	if err := validateOpaqueTransactionField(request.RequestID, "request ID"); err != nil {
		return err
	}
	if err := validateSHA256Hex(request.ManifestDigest, "owner-risk manifest digest"); err != nil {
		return err
	}
	return validateQuarantineTarget(request.QuarantineTarget, request.QuarantineTarget.AccountID)
}

func validateOwnerRiskSnapshotShape(snapshot OwnerRiskSnapshot) error {
	if snapshot.SchemaVersion != CurrentOwnerRiskSchemaVersion {
		return fmt.Errorf("unsupported owner-risk snapshot schema version %d", snapshot.SchemaVersion)
	}
	if _, err := CanonicalOwnerRiskSnapshotRequest(snapshot.Request); err != nil {
		return err
	}
	if err := validateSHA256Hex(snapshot.RequestDigest, "owner-risk snapshot request digest"); err != nil {
		return err
	}
	if err := validateOpaqueTransactionField(snapshot.Authority, "authority"); err != nil {
		return err
	}
	if snapshot.ObservedAt.IsZero() {
		return errors.New("owner-risk snapshot observation time is required")
	}
	if snapshot.Intent.State != ApprovalApproved || snapshot.Intent.Approval.ApprovalID != snapshot.Request.ApprovalID || snapshot.Intent.Approval.ManifestDigest != snapshot.Request.ManifestDigest {
		return errors.New("owner-risk snapshot does not contain the exact approved intent")
	}
	approvalCanonical, err := CanonicalApproval(snapshot.Intent.Approval)
	if err != nil || snapshot.Intent.IntentDigest != Digest(approvalCanonical) {
		return errors.New("owner-risk snapshot intent digest is invalid")
	}
	requestedTarget, err := marshalCanonical(snapshot.Request.QuarantineTarget)
	if err != nil {
		return err
	}
	approvedTarget, err := marshalCanonical(snapshot.Intent.Approval.QuarantineTarget)
	if err != nil || string(requestedTarget) != string(approvedTarget) {
		return errors.New("owner-risk snapshot quarantine target does not match the approval")
	}
	if _, err := decodeEd25519Signature(snapshot.Intent.SignatureHex); err != nil {
		return errors.New("owner-risk snapshot approval signature is invalid")
	}
	for name, oid := range map[string]string{"intent OID": snapshot.IntentOID, "state OID": snapshot.StateOID} {
		if !gitOIDPattern.MatchString(oid) {
			return fmt.Errorf("owner-risk snapshot %s must be an exact Git OID", name)
		}
	}
	for name, oid := range map[string]string{"journal OID": snapshot.JournalOID, "lease OID": snapshot.LeaseOID} {
		if oid != "" && !gitOIDPattern.MatchString(oid) {
			return fmt.Errorf("owner-risk snapshot %s must be empty or an exact Git OID", name)
		}
	}
	return nil
}
