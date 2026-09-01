package cleanup

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	CurrentCandidateControlSchemaVersion = 1
	CandidateControlIssuerPurpose        = "BD-CANDIDATE-CONTROL-V1"
	CandidateControlReadbackPurpose      = "BD-CANDIDATE-CONTROL-READBACK-V1"
	CandidateControlClaimPurpose         = "BD-CANDIDATE-CONTROL-CLAIM-V1"
	CandidateControlExercisedRecordType  = "BD-CANDIDATE-CONTROL-EXERCISED"
	candidateControlMaxInputBytes        = 1 << 20
	candidateControlMaxOperations        = 16
	candidateControlMaxValidity          = 15 * time.Minute
)

var (
	candidateControlIDPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+-]{0,127}$`)
	candidateControlOIDPattern       = regexp.MustCompile(`^[a-f0-9]{40}$`)
	candidateControlRefPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@+-]*$`)
	candidateControlSignaturePattern = regexp.MustCompile(`^[a-f0-9]{128}$`)
	candidateControlTimestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,6})?Z$`)
)

type CandidateControlOperation struct {
	Ref         string `json:"ref"`
	ExpectedOID string `json:"expected_oid"`
	DesiredOID  string `json:"desired_oid"`
}

type CandidateControlCapability struct {
	SchemaVersion        int                         `json:"schema_version"`
	Purpose              string                      `json:"purpose"`
	TransactionID        string                      `json:"transaction_id"`
	Issuer               string                      `json:"issuer"`
	ExecutorID           string                      `json:"executor_id"`
	ExecutorPublicKey    string                      `json:"executor_public_key"`
	ClientID             string                      `json:"client_id"`
	Remote               string                      `json:"remote"`
	RemoteURLDigest      string                      `json:"remote_url_digest"`
	RefNamespace         string                      `json:"ref_namespace"`
	ClaimRef             string                      `json:"claim_ref"`
	CompletionRef        string                      `json:"completion_ref"`
	Operations           []CandidateControlOperation `json:"operations"`
	OperationBudget      int                         `json:"operation_budget"`
	RefEscapeProbe       string                      `json:"ref_escape_probe"`
	ProductionRemote     string                      `json:"production_remote"`
	ProductionRef        string                      `json:"production_ref"`
	TeardownIntentDigest string                      `json:"teardown_intent_digest"`
	IssuedAt             string                      `json:"issued_at"`
	ExpiresAt            string                      `json:"expires_at"`
	Nonce                string                      `json:"nonce"`
	Signature            string                      `json:"signature"`
}

type CandidateControlClaimPayload struct {
	SchemaVersion     int    `json:"schema_version"`
	Purpose           string `json:"purpose"`
	TransactionID     string `json:"transaction_id"`
	CapabilityDigest  string `json:"capability_digest"`
	ExecutorID        string `json:"executor_id"`
	ExecutorPublicKey string `json:"executor_public_key"`
	InvocationID      string `json:"invocation_id"`
	ClaimedAt         string `json:"claimed_at"`
}

type CandidateControlRefReadback struct {
	Ref          string `json:"ref"`
	OID          string `json:"oid"`
	InvocationID string `json:"invocation_id"`
	Created      bool   `json:"created"`
}

type CandidateControlOperationReadback struct {
	Ref         string `json:"ref"`
	ExpectedOID string `json:"expected_oid"`
	DesiredOID  string `json:"desired_oid"`
	BeforeOID   string `json:"before_oid"`
	AfterOID    string `json:"after_oid"`
}

type CandidateControlAtomicUpdate struct {
	Kind        string  `json:"kind"`
	Ref         string  `json:"ref"`
	ExpectedOID *string `json:"expected_oid"`
	DesiredOID  string  `json:"desired_oid"`
	BeforeOID   *string `json:"before_oid"`
	AfterOID    string  `json:"after_oid"`
}

type CandidateControlAtomicPush struct {
	Attempted  bool                           `json:"attempted"`
	ExitCode   int                            `json:"exit_code"`
	Reconciled bool                           `json:"reconciled"`
	Updates    []CandidateControlAtomicUpdate `json:"updates"`
}

type CandidateControlRefOID struct {
	Ref string `json:"ref"`
	OID string `json:"oid"`
}

type CandidateControlReplayProbe struct {
	Attempted             bool                           `json:"attempted"`
	ProviderInvoked       bool                           `json:"provider_invoked"`
	ProviderCallsBefore   int64                          `json:"provider_calls_before"`
	ProviderCallsAfter    int64                          `json:"provider_calls_after"`
	ExitCode              int                            `json:"exit_code"`
	Outcome               string                         `json:"outcome"`
	WinningClaimOID       string                         `json:"winning_claim_oid"`
	AttemptedClaimOID     string                         `json:"attempted_claim_oid"`
	AttemptedInvocationID string                         `json:"attempted_invocation_id"`
	AttemptedClaimPayload CandidateControlClaimPayload   `json:"attempted_claim_payload"`
	ClaimOIDBefore        string                         `json:"claim_oid_before"`
	ClaimOIDAfter         string                         `json:"claim_oid_after"`
	CompletionOIDBefore   string                         `json:"completion_oid_before"`
	CompletionOIDAfter    string                         `json:"completion_oid_after"`
	OperationsBefore      []CandidateControlRefOID       `json:"operations_before"`
	OperationsAfter       []CandidateControlRefOID       `json:"operations_after"`
	Updates               []CandidateControlAtomicUpdate `json:"updates"`
}

type CandidateControlDenialProbe struct {
	Remote              string `json:"remote"`
	Ref                 string `json:"ref"`
	ProviderInvoked     bool   `json:"provider_invoked"`
	ProviderCallsBefore int64  `json:"provider_calls_before"`
	ProviderCallsAfter  int64  `json:"provider_calls_after"`
	Outcome             string `json:"outcome"`
}

type CandidateControlReadback struct {
	SchemaVersion         int                                 `json:"schema_version"`
	Purpose               string                              `json:"purpose"`
	TransactionID         string                              `json:"transaction_id"`
	CapabilityDigest      string                              `json:"capability_digest"`
	Issuer                string                              `json:"issuer"`
	ExecutorID            string                              `json:"executor_id"`
	ClientID              string                              `json:"client_id"`
	Remote                string                              `json:"remote"`
	RemoteURLDigest       string                              `json:"remote_url_digest"`
	Transport             string                              `json:"transport"`
	ClaimPayload          CandidateControlClaimPayload        `json:"claim_payload"`
	Claim                 CandidateControlRefReadback         `json:"claim"`
	Completion            CandidateControlRefReadback         `json:"completion"`
	Operations            []CandidateControlOperationReadback `json:"operations"`
	OperationCount        int                                 `json:"operation_count"`
	AtomicPush            CandidateControlAtomicPush          `json:"atomic_push"`
	ReplayProbe           CandidateControlReplayProbe         `json:"replay_probe"`
	RefEscapeProbe        CandidateControlDenialProbe         `json:"ref_escape_probe"`
	ProductionRemoteProbe CandidateControlDenialProbe         `json:"production_remote_probe"`
	ProductionRefProbe    CandidateControlDenialProbe         `json:"production_ref_probe"`
	ObservedAt            string                              `json:"observed_at"`
	Signature             string                              `json:"signature"`
}

type CandidateControlExercisedMarker struct {
	SchemaVersion           int       `json:"schema_version"`
	RecordType              string    `json:"record_type"`
	TransactionID           string    `json:"transaction_id"`
	CapabilityDigest        string    `json:"capability_digest"`
	ReadbackDigest          string    `json:"readback_digest"`
	TrustBundleDigest       string    `json:"trust_bundle_digest"`
	Remote                  string    `json:"remote"`
	RemoteURLDigest         string    `json:"remote_url_digest"`
	Transport               string    `json:"transport"`
	ClaimRef                string    `json:"claim_ref"`
	ClaimOID                string    `json:"claim_oid"`
	CompletionRef           string    `json:"completion_ref"`
	CompletionOID           string    `json:"completion_oid"`
	OperationCount          int       `json:"operation_count"`
	Atomic                  bool      `json:"atomic"`
	ReplayDenied            bool      `json:"replay_denied"`
	RefEscapeDenied         bool      `json:"ref_escape_denied"`
	ProductionRemoteDenied  bool      `json:"production_remote_denied"`
	ProductionRefDenied     bool      `json:"production_ref_denied"`
	ControlRootFingerprint  string    `json:"control_root_fingerprint"`
	ReadbackRootFingerprint string    `json:"readback_root_fingerprint"`
	TeardownIntentDigest    string    `json:"teardown_intent_digest"`
	ObservedAt              time.Time `json:"observed_at"`
}

type candidateControlSignedJSON struct {
	canonical []byte
	unsigned  []byte
	signature []byte
	object    map[string]any
}

var candidateControlCapabilityKeys = []string{
	"schema_version", "purpose", "transaction_id", "issuer", "executor_id",
	"executor_public_key", "client_id", "remote", "remote_url_digest",
	"ref_namespace", "claim_ref", "completion_ref", "operations",
	"operation_budget", "ref_escape_probe", "production_remote", "production_ref",
	"teardown_intent_digest", "issued_at", "expires_at", "nonce", "signature",
}

var candidateControlReadbackKeys = []string{
	"schema_version", "purpose", "transaction_id", "capability_digest", "issuer",
	"executor_id", "client_id", "remote", "remote_url_digest", "transport",
	"claim_payload", "claim", "completion", "operations", "operation_count",
	"atomic_push", "replay_probe", "ref_escape_probe", "production_remote_probe",
	"production_ref_probe", "observed_at", "signature",
}

var candidateControlClaimPayloadKeys = []string{
	"schema_version", "purpose", "transaction_id", "capability_digest",
	"executor_id", "executor_public_key", "invocation_id", "claimed_at",
}

var candidateControlAtomicUpdateKeys = []string{
	"kind", "ref", "expected_oid", "desired_oid", "before_oid", "after_oid",
}

func VerifyCandidateControlExercise(
	capabilityData []byte,
	capabilityRoot TrustRoot,
	readbackData []byte,
	readbackRoot TrustRoot,
	trustBundleDigest string,
	now time.Time,
) (CandidateControlExercisedMarker, error) {
	if err := validateSHA256Hex(trustBundleDigest, "candidate control trust_bundle_digest"); err != nil {
		return CandidateControlExercisedMarker{}, err
	}
	capabilityJSON, err := decodeCandidateControlSignedJSON(capabilityData, candidateControlCapabilityKeys)
	if err != nil {
		return CandidateControlExercisedMarker{}, fmt.Errorf("candidate control capability: %w", err)
	}
	if err := validateCandidateControlCapabilityShape(capabilityJSON.object); err != nil {
		return CandidateControlExercisedMarker{}, err
	}
	var capability CandidateControlCapability
	if err := decodeStrictJSONRecord(capabilityJSON.canonical, &capability); err != nil {
		return CandidateControlExercisedMarker{}, fmt.Errorf("candidate control capability shape: %w", err)
	}
	issuedAt, expiresAt, err := validateCandidateControlCapability(capability, now)
	if err != nil {
		return CandidateControlExercisedMarker{}, err
	}
	if !capabilityRoot.EnrolledAt.Before(issuedAt) {
		return CandidateControlExercisedMarker{}, errors.New("candidate control capability root must be enrolled before issuance")
	}
	capabilityPublicKey, err := capabilityRoot.PublicKeyForPurpose(CandidateControlIssuerPurpose, capability.Issuer, issuedAt)
	if err != nil {
		return CandidateControlExercisedMarker{}, fmt.Errorf("candidate control capability root: %w", err)
	}
	if !ed25519.Verify(capabilityPublicKey, capabilityJSON.unsigned, capabilityJSON.signature) {
		return CandidateControlExercisedMarker{}, errors.New("candidate control capability signature is invalid")
	}
	capabilityDigest := sha256HexBytes(capabilityJSON.unsigned)

	readbackJSON, err := decodeCandidateControlSignedJSON(readbackData, candidateControlReadbackKeys)
	if err != nil {
		return CandidateControlExercisedMarker{}, fmt.Errorf("candidate control readback: %w", err)
	}
	if err := validateCandidateControlReadbackShape(readbackJSON.object); err != nil {
		return CandidateControlExercisedMarker{}, err
	}
	var readback CandidateControlReadback
	if err := decodeStrictJSONRecord(readbackJSON.canonical, &readback); err != nil {
		return CandidateControlExercisedMarker{}, fmt.Errorf("candidate control readback shape: %w", err)
	}
	rootObservedAt, err := parseCandidateControlTimestamp(readback.ObservedAt)
	if err != nil {
		return CandidateControlExercisedMarker{}, errors.New("candidate control readback observed_at is invalid")
	}
	if !readbackRoot.EnrolledAt.Before(rootObservedAt) {
		return CandidateControlExercisedMarker{}, errors.New("candidate control readback root must be enrolled before observation")
	}
	readbackPublicKey, err := readbackRoot.PublicKeyForPurpose(CandidateControlReadbackPurpose, readback.Issuer, rootObservedAt)
	if err != nil {
		return CandidateControlExercisedMarker{}, fmt.Errorf("candidate control readback root: %w", err)
	}
	if hex.EncodeToString(readbackPublicKey) != capability.ExecutorPublicKey {
		return CandidateControlExercisedMarker{}, errors.New("candidate control executor key does not match the capability")
	}
	if !ed25519.Verify(readbackPublicKey, readbackJSON.unsigned, readbackJSON.signature) {
		return CandidateControlExercisedMarker{}, errors.New("candidate control readback signature is invalid")
	}
	observedAt, err := validateCandidateControlReadback(readback, capability, capabilityDigest, issuedAt, expiresAt, now)
	if err != nil {
		return CandidateControlExercisedMarker{}, err
	}

	return CandidateControlExercisedMarker{
		SchemaVersion:           CurrentCandidateControlSchemaVersion,
		RecordType:              CandidateControlExercisedRecordType,
		TransactionID:           capability.TransactionID,
		CapabilityDigest:        capabilityDigest,
		ReadbackDigest:          sha256HexBytes(readbackJSON.canonical),
		TrustBundleDigest:       trustBundleDigest,
		Remote:                  capability.Remote,
		RemoteURLDigest:         capability.RemoteURLDigest,
		Transport:               readback.Transport,
		ClaimRef:                capability.ClaimRef,
		ClaimOID:                readback.Claim.OID,
		CompletionRef:           capability.CompletionRef,
		CompletionOID:           readback.Completion.OID,
		OperationCount:          len(capability.Operations),
		Atomic:                  true,
		ReplayDenied:            true,
		RefEscapeDenied:         true,
		ProductionRemoteDenied:  true,
		ProductionRefDenied:     true,
		ControlRootFingerprint:  capabilityRoot.Fingerprint,
		ReadbackRootFingerprint: readbackRoot.Fingerprint,
		TeardownIntentDigest:    capability.TeardownIntentDigest,
		ObservedAt:              observedAt,
	}, nil
}

func decodeCandidateControlSignedJSON(data []byte, expectedKeys []string) (candidateControlSignedJSON, error) {
	if len(data) == 0 || len(data) > candidateControlMaxInputBytes {
		return candidateControlSignedJSON{}, errors.New("input size is invalid")
	}
	normalized := data
	if normalized[len(normalized)-1] == '\n' {
		normalized = normalized[:len(normalized)-1]
	}
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return candidateControlSignedJSON{}, fmt.Errorf("decode canonical JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return candidateControlSignedJSON{}, errors.New("trailing JSON is not allowed")
	}
	object, ok := decoded.(map[string]any)
	if !ok || !hasExactCandidateControlKeys(object, expectedKeys) {
		return candidateControlSignedJSON{}, errors.New("signed JSON shape is invalid")
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return candidateControlSignedJSON{}, fmt.Errorf("canonicalize JSON: %w", err)
	}
	if !bytes.Equal(normalized, canonical) {
		return candidateControlSignedJSON{}, errors.New("signed JSON must use canonical encoding")
	}
	signatureHex, ok := object["signature"].(string)
	if !ok || !candidateControlSignaturePattern.MatchString(signatureHex) {
		return candidateControlSignedJSON{}, errors.New("signature encoding is invalid")
	}
	signature, err := hex.DecodeString(signatureHex)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return candidateControlSignedJSON{}, errors.New("signature encoding is invalid")
	}
	delete(object, "signature")
	unsigned, err := json.Marshal(object)
	if err != nil {
		return candidateControlSignedJSON{}, fmt.Errorf("canonicalize unsigned JSON: %w", err)
	}
	return candidateControlSignedJSON{
		canonical: canonical,
		unsigned:  unsigned,
		signature: signature,
		object:    object,
	}, nil
}

func validateCandidateControlCapabilityShape(object map[string]any) error {
	operations, ok := object["operations"].([]any)
	if !ok || len(operations) == 0 || len(operations) > candidateControlMaxOperations {
		return errors.New("candidate control capability operation shape is invalid")
	}
	for _, raw := range operations {
		operation, ok := raw.(map[string]any)
		if !ok || !hasExactCandidateControlKeys(operation, []string{"ref", "expected_oid", "desired_oid"}) {
			return errors.New("candidate control capability operation shape is invalid")
		}
	}
	return nil
}

func validateCandidateControlReadbackShape(object map[string]any) error {
	if !candidateControlObjectHasKeys(object["claim_payload"], candidateControlClaimPayloadKeys) {
		return errors.New("candidate control readback claim_payload shape is invalid")
	}
	for _, field := range []string{"claim", "completion"} {
		if !candidateControlObjectHasKeys(object[field], []string{"ref", "oid", "invocation_id", "created"}) {
			return fmt.Errorf("candidate control readback %s shape is invalid", field)
		}
	}
	atomicPush, ok := object["atomic_push"].(map[string]any)
	if !ok || !hasExactCandidateControlKeys(
		atomicPush,
		[]string{"attempted", "exit_code", "reconciled", "updates"},
	) || !candidateControlUpdatesHaveExactShape(atomicPush["updates"]) {
		return errors.New("candidate control readback atomic_push shape is invalid")
	}
	operations, ok := object["operations"].([]any)
	if !ok || len(operations) == 0 || len(operations) > candidateControlMaxOperations {
		return errors.New("candidate control readback operation shape is invalid")
	}
	for _, raw := range operations {
		if !candidateControlObjectHasKeys(raw, []string{"ref", "expected_oid", "desired_oid", "before_oid", "after_oid"}) {
			return errors.New("candidate control readback operation shape is invalid")
		}
	}
	replay, ok := object["replay_probe"].(map[string]any)
	if !ok || !hasExactCandidateControlKeys(replay, []string{
		"attempted", "provider_invoked", "provider_calls_before", "provider_calls_after",
		"exit_code", "outcome", "winning_claim_oid", "attempted_claim_oid",
		"attempted_invocation_id", "attempted_claim_payload",
		"claim_oid_before", "claim_oid_after", "completion_oid_before", "completion_oid_after",
		"operations_before", "operations_after", "updates",
	}) || !candidateControlObjectHasKeys(
		replay["attempted_claim_payload"],
		candidateControlClaimPayloadKeys,
	) || !candidateControlUpdatesHaveExactShape(replay["updates"]) {
		return errors.New("candidate control readback replay_probe shape is invalid")
	}
	for _, field := range []string{"operations_before", "operations_after"} {
		operations, ok := replay[field].([]any)
		if !ok || len(operations) == 0 {
			return errors.New("candidate control readback replay operation shape is invalid")
		}
		for _, raw := range operations {
			if !candidateControlObjectHasKeys(raw, []string{"ref", "oid"}) {
				return errors.New("candidate control readback replay operation shape is invalid")
			}
		}
	}
	for _, field := range []string{"ref_escape_probe", "production_remote_probe", "production_ref_probe"} {
		if !candidateControlObjectHasKeys(object[field], []string{
			"remote", "ref", "provider_invoked", "provider_calls_before", "provider_calls_after", "outcome",
		}) {
			return fmt.Errorf("candidate control readback %s shape is invalid", field)
		}
	}
	return nil
}

func candidateControlUpdatesHaveExactShape(raw any) bool {
	updates, ok := raw.([]any)
	if !ok || len(updates) == 0 || len(updates) > candidateControlMaxOperations+2 {
		return false
	}
	for _, update := range updates {
		if !candidateControlObjectHasKeys(update, candidateControlAtomicUpdateKeys) {
			return false
		}
	}
	return true
}

func candidateControlObjectHasKeys(raw any, keys []string) bool {
	object, ok := raw.(map[string]any)
	return ok && hasExactCandidateControlKeys(object, keys)
}

func hasExactCandidateControlKeys(object map[string]any, keys []string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func validateCandidateControlCapability(capability CandidateControlCapability, now time.Time) (time.Time, time.Time, error) {
	if capability.SchemaVersion != CurrentCandidateControlSchemaVersion || capability.Purpose != CandidateControlIssuerPurpose {
		return time.Time{}, time.Time{}, errors.New("candidate control capability schema or purpose is invalid")
	}
	for name, value := range map[string]string{
		"transaction_id": capability.TransactionID,
		"issuer":         capability.Issuer,
		"executor_id":    capability.ExecutorID,
		"client_id":      capability.ClientID,
		"nonce":          capability.Nonce,
	} {
		if !candidateControlIDPattern.MatchString(value) {
			return time.Time{}, time.Time{}, fmt.Errorf("candidate control capability %s is invalid", name)
		}
	}
	if err := validateSHA256Hex(capability.ExecutorPublicKey, "candidate control executor_public_key"); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !repositoryPattern.MatchString(capability.Remote) ||
		!repositoryPattern.MatchString(capability.ProductionRemote) ||
		capability.Remote != strings.ToLower(capability.Remote) ||
		capability.ProductionRemote != strings.ToLower(capability.ProductionRemote) ||
		capability.Remote == capability.ProductionRemote {
		return time.Time{}, time.Time{}, errors.New("candidate control remote binding is invalid")
	}
	if err := validateSHA256Hex(capability.RemoteURLDigest, "candidate control remote_url_digest"); err != nil {
		return time.Time{}, time.Time{}, err
	}
	expectedRemoteURLDigest := sha256HexBytes(
		[]byte("https://github.com/" + capability.Remote + ".git"),
	)
	if capability.RemoteURLDigest != expectedRemoteURLDigest {
		return time.Time{}, time.Time{}, errors.New("candidate control remote_url_digest is not canonical GitHub")
	}
	if !strings.HasPrefix(capability.RefNamespace, "refs/heads/") ||
		!strings.HasSuffix(capability.RefNamespace, "/") ||
		!candidateControlRefPattern.MatchString(capability.RefNamespace) ||
		validateSafeRef(strings.TrimSuffix(capability.RefNamespace, "/")) != nil {
		return time.Time{}, time.Time{}, errors.New("candidate control ref namespace is invalid")
	}
	for name, ref := range map[string]string{
		"claim":      capability.ClaimRef,
		"completion": capability.CompletionRef,
		"ref escape": capability.RefEscapeProbe,
		"production": capability.ProductionRef,
	} {
		if !candidateControlRefPattern.MatchString(ref) {
			return time.Time{}, time.Time{}, fmt.Errorf("candidate control %s ref is not canonical ASCII", name)
		}
		if err := validateSafeRef(ref); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("candidate control %s ref is invalid: %w", name, err)
		}
	}
	if capability.ClaimRef == capability.CompletionRef || !strings.HasPrefix(capability.ClaimRef, capability.RefNamespace) || !strings.HasPrefix(capability.CompletionRef, capability.RefNamespace) {
		return time.Time{}, time.Time{}, errors.New("candidate control claim or completion binding is invalid")
	}
	if strings.HasPrefix(capability.RefEscapeProbe, capability.RefNamespace) || strings.HasPrefix(capability.ProductionRef, capability.RefNamespace) {
		return time.Time{}, time.Time{}, errors.New("candidate control denial probe overlaps the allowed namespace")
	}
	if len(capability.Operations) == 0 || len(capability.Operations) > candidateControlMaxOperations || capability.OperationBudget != len(capability.Operations) {
		return time.Time{}, time.Time{}, errors.New("candidate control operation budget is not exact")
	}
	seen := make(map[string]struct{}, len(capability.Operations))
	previousRef := ""
	for _, operation := range capability.Operations {
		if !candidateControlRefPattern.MatchString(operation.Ref) ||
			validateSafeRef(operation.Ref) != nil ||
			!strings.HasPrefix(operation.Ref, capability.RefNamespace) {
			return time.Time{}, time.Time{}, errors.New("candidate control operation ref escapes the namespace")
		}
		if operation.Ref <= previousRef {
			return time.Time{}, time.Time{}, errors.New("candidate control operations must be uniquely sorted by ref")
		}
		if _, exists := seen[operation.Ref]; exists || operation.Ref == capability.ClaimRef || operation.Ref == capability.CompletionRef {
			return time.Time{}, time.Time{}, errors.New("candidate control operation ref is duplicate or reserved")
		}
		if !candidateControlOIDPattern.MatchString(operation.ExpectedOID) ||
			!candidateControlOIDPattern.MatchString(operation.DesiredOID) ||
			operation.ExpectedOID == operation.DesiredOID {
			return time.Time{}, time.Time{}, errors.New("candidate control operation OID binding is invalid")
		}
		seen[operation.Ref] = struct{}{}
		previousRef = operation.Ref
	}
	if err := validateSHA256Hex(capability.TeardownIntentDigest, "candidate control teardown_intent_digest"); err != nil {
		return time.Time{}, time.Time{}, err
	}
	issuedAt, err := parseCandidateControlTimestamp(capability.IssuedAt)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("candidate control issued_at: %w", err)
	}
	expiresAt, err := parseCandidateControlTimestamp(capability.ExpiresAt)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("candidate control expires_at: %w", err)
	}
	if !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > candidateControlMaxValidity || issuedAt.After(now.Add(time.Minute)) || !now.Before(expiresAt) {
		return time.Time{}, time.Time{}, errors.New("candidate control capability validity interval is invalid")
	}
	return issuedAt, expiresAt, nil
}

func validateCandidateControlReadback(
	readback CandidateControlReadback,
	capability CandidateControlCapability,
	capabilityDigest string,
	issuedAt time.Time,
	expiresAt time.Time,
	now time.Time,
) (time.Time, error) {
	if readback.SchemaVersion != CurrentCandidateControlSchemaVersion ||
		readback.Purpose != CandidateControlReadbackPurpose {
		return time.Time{}, errors.New("candidate control readback schema or purpose is invalid")
	}
	if readback.TransactionID != capability.TransactionID ||
		readback.CapabilityDigest != capabilityDigest ||
		readback.Issuer != capability.ExecutorID ||
		readback.ExecutorID != capability.ExecutorID ||
		readback.ClientID != capability.ClientID {
		return time.Time{}, errors.New("candidate control readback identity binding is invalid")
	}
	if readback.Remote != capability.Remote ||
		readback.RemoteURLDigest != capability.RemoteURLDigest ||
		readback.Transport != "github" {
		return time.Time{}, errors.New("candidate control readback is not live GitHub evidence")
	}
	observedAt, err := parseCandidateControlTimestamp(readback.ObservedAt)
	if err != nil || observedAt.Before(issuedAt) ||
		!observedAt.Before(expiresAt) || observedAt.After(now.Add(time.Minute)) {
		return time.Time{}, errors.New("candidate control readback observed_at is outside the capability")
	}
	if readback.Claim.Ref != capability.ClaimRef ||
		readback.Completion.Ref != capability.CompletionRef ||
		!readback.Claim.Created || !readback.Completion.Created ||
		!candidateControlOIDPattern.MatchString(readback.Claim.OID) ||
		readback.Claim.OID != readback.Completion.OID ||
		!candidateControlIDPattern.MatchString(readback.Claim.InvocationID) ||
		readback.Claim.InvocationID != readback.Completion.InvocationID {
		return time.Time{}, errors.New("candidate control claim or completion readback is invalid")
	}
	claimTime, err := validateCandidateControlClaimPayload(
		readback.ClaimPayload,
		capability,
		capabilityDigest,
		readback.Claim.InvocationID,
		issuedAt,
		observedAt,
	)
	if err != nil {
		return time.Time{}, err
	}
	claimOID, err := CandidateControlClaimCommitOID(readback.ClaimPayload)
	if err != nil || claimOID != readback.Claim.OID {
		return time.Time{}, errors.New("candidate control claim commit binding is invalid")
	}
	if !readback.AtomicPush.Attempted ||
		readback.AtomicPush.ExitCode != 0 ||
		readback.AtomicPush.Reconciled {
		return time.Time{}, errors.New("candidate control positive atomic CAS is absent")
	}
	if readback.OperationCount != len(capability.Operations) ||
		len(readback.Operations) != len(capability.Operations) {
		return time.Time{}, errors.New("candidate control operation readback count is invalid")
	}
	expectedOIDs := make([]string, len(capability.Operations))
	desiredOIDs := make([]string, len(capability.Operations))
	for index, operation := range capability.Operations {
		observed := readback.Operations[index]
		if observed.Ref != operation.Ref ||
			observed.ExpectedOID != operation.ExpectedOID ||
			observed.DesiredOID != operation.DesiredOID ||
			observed.BeforeOID != operation.ExpectedOID ||
			observed.AfterOID != operation.DesiredOID {
			return time.Time{}, fmt.Errorf("candidate control operation %d readback is invalid", index)
		}
		expectedOIDs[index] = operation.ExpectedOID
		desiredOIDs[index] = operation.DesiredOID
	}
	if err := validateCandidateControlAtomicUpdates(
		readback.AtomicPush.Updates,
		capability,
		readback.Claim.OID,
		nil,
		readback.Claim.OID,
		expectedOIDs,
		desiredOIDs,
	); err != nil {
		return time.Time{}, err
	}
	if err := validateCandidateControlReplay(
		readback.ReplayProbe,
		readback.Claim.OID,
		claimTime,
		capability,
		capabilityDigest,
		expiresAt,
		observedAt,
		desiredOIDs,
	); err != nil {
		return time.Time{}, err
	}
	lastProviderCall := readback.ReplayProbe.ProviderCallsAfter
	if err := validateCandidateControlDenial(readback.RefEscapeProbe, capability.Remote, capability.RefEscapeProbe, "REF_OUTSIDE_NAMESPACE", lastProviderCall); err != nil {
		return time.Time{}, err
	}
	if err := validateCandidateControlDenial(readback.ProductionRemoteProbe, capability.ProductionRemote, capability.ProductionRef, "REMOTE_OUTSIDE_ALLOWLIST", lastProviderCall); err != nil {
		return time.Time{}, err
	}
	if err := validateCandidateControlDenial(readback.ProductionRefProbe, capability.Remote, capability.ProductionRef, "REF_OUTSIDE_NAMESPACE", lastProviderCall); err != nil {
		return time.Time{}, err
	}
	return observedAt, nil
}

func validateCandidateControlReplay(
	probe CandidateControlReplayProbe,
	claimOID string,
	claimTime time.Time,
	capability CandidateControlCapability,
	capabilityDigest string,
	expiresAt time.Time,
	observedAt time.Time,
	desiredOIDs []string,
) error {
	if !probe.Attempted || !probe.ProviderInvoked ||
		probe.ProviderCallsBefore < 0 ||
		probe.ProviderCallsAfter <= probe.ProviderCallsBefore ||
		probe.ExitCode <= 0 || probe.Outcome != "ALREADY_COMPLETED" {
		return errors.New("candidate control replay denial is invalid")
	}
	if probe.WinningClaimOID != claimOID ||
		!candidateControlOIDPattern.MatchString(probe.AttemptedClaimOID) ||
		probe.AttemptedClaimOID == claimOID ||
		probe.ClaimOIDBefore != claimOID ||
		probe.ClaimOIDAfter != claimOID ||
		probe.CompletionOIDBefore != claimOID ||
		probe.CompletionOIDAfter != claimOID {
		return errors.New("candidate control replay claim binding is invalid")
	}
	attemptedAt, err := validateCandidateControlClaimPayload(
		probe.AttemptedClaimPayload,
		capability,
		capabilityDigest,
		probe.AttemptedInvocationID,
		claimTime,
		observedAt,
	)
	if err != nil || !attemptedAt.Before(expiresAt) {
		return errors.New("candidate control replay claim payload is invalid")
	}
	attemptedOID, err := CandidateControlClaimCommitOID(
		probe.AttemptedClaimPayload,
	)
	if err != nil || attemptedOID != probe.AttemptedClaimOID {
		return errors.New("candidate control replay claim commit binding is invalid")
	}
	expected := make([]CandidateControlRefOID, len(capability.Operations))
	for index, operation := range capability.Operations {
		expected[index] = CandidateControlRefOID{
			Ref: operation.Ref,
			OID: operation.DesiredOID,
		}
	}
	if !slices.Equal(probe.OperationsBefore, expected) ||
		!slices.Equal(probe.OperationsAfter, expected) {
		return errors.New("candidate control replay denial changed bound operation state")
	}
	winning := claimOID
	if err := validateCandidateControlAtomicUpdates(
		probe.Updates,
		capability,
		probe.AttemptedClaimOID,
		&winning,
		claimOID,
		desiredOIDs,
		desiredOIDs,
	); err != nil {
		return fmt.Errorf("candidate control replay request: %w", err)
	}
	return nil
}

func validateCandidateControlClaimPayload(
	payload CandidateControlClaimPayload,
	capability CandidateControlCapability,
	capabilityDigest string,
	invocationID string,
	earliest time.Time,
	latest time.Time,
) (time.Time, error) {
	if payload.SchemaVersion != CurrentCandidateControlSchemaVersion ||
		payload.Purpose != CandidateControlClaimPurpose ||
		payload.TransactionID != capability.TransactionID ||
		payload.CapabilityDigest != capabilityDigest ||
		payload.ExecutorID != capability.ExecutorID ||
		payload.ExecutorPublicKey != capability.ExecutorPublicKey ||
		payload.InvocationID != invocationID ||
		!candidateControlIDPattern.MatchString(payload.InvocationID) {
		return time.Time{}, errors.New("candidate control claim payload binding is invalid")
	}
	claimedAt, err := parseCandidateControlTimestamp(payload.ClaimedAt)
	if err != nil || claimedAt.Before(earliest) || claimedAt.After(latest) {
		return time.Time{}, errors.New("candidate control claim payload time is invalid")
	}
	return claimedAt, nil
}

func validateCandidateControlAtomicUpdates(
	updates []CandidateControlAtomicUpdate,
	capability CandidateControlCapability,
	requestedClaimOID string,
	observedClaimBefore *string,
	observedClaimAfter string,
	operationBefore []string,
	operationAfter []string,
) error {
	if len(updates) != len(capability.Operations)+2 ||
		len(operationBefore) != len(capability.Operations) ||
		len(operationAfter) != len(capability.Operations) {
		return errors.New("candidate control atomic update count is invalid")
	}
	if !candidateControlAtomicUpdateMatches(
		updates[0],
		"claim",
		capability.ClaimRef,
		nil,
		requestedClaimOID,
		observedClaimBefore,
		observedClaimAfter,
	) {
		return errors.New("candidate control atomic claim update is invalid")
	}
	for index, operation := range capability.Operations {
		expected := operation.ExpectedOID
		before := operationBefore[index]
		if !candidateControlAtomicUpdateMatches(
			updates[index+1],
			"operation",
			operation.Ref,
			&expected,
			operation.DesiredOID,
			&before,
			operationAfter[index],
		) {
			return fmt.Errorf("candidate control atomic operation update %d is invalid", index)
		}
	}
	if !candidateControlAtomicUpdateMatches(
		updates[len(updates)-1],
		"completion",
		capability.CompletionRef,
		nil,
		requestedClaimOID,
		observedClaimBefore,
		observedClaimAfter,
	) {
		return errors.New("candidate control atomic completion update is invalid")
	}
	return nil
}

func candidateControlAtomicUpdateMatches(
	update CandidateControlAtomicUpdate,
	kind string,
	ref string,
	expectedOID *string,
	desiredOID string,
	beforeOID *string,
	afterOID string,
) bool {
	return update.Kind == kind &&
		update.Ref == ref &&
		candidateControlOptionalStringEqual(update.ExpectedOID, expectedOID) &&
		update.DesiredOID == desiredOID &&
		candidateControlOptionalStringEqual(update.BeforeOID, beforeOID) &&
		update.AfterOID == afterOID
}

func candidateControlOptionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func CandidateControlClaimCommitOID(
	payload CandidateControlClaimPayload,
) (string, error) {
	canonical, err := canonicalCandidateControlValue(payload)
	if err != nil {
		return "", err
	}
	blobOID := candidateControlGitObjectOID("blob", canonical)
	treeData := append(
		[]byte("100644 claim.json\x00"),
		blobOID[:]...,
	)
	treeOID := candidateControlGitObjectOID("tree", treeData)
	commitData := []byte(fmt.Sprintf(
		"tree %s\nauthor Skret Candidate Executor <candidate-executor@example.invalid> 0 +0000\ncommitter Skret Candidate Executor <candidate-executor@example.invalid> 0 +0000\n\ncandidate control claim\n",
		hex.EncodeToString(treeOID[:]),
	))
	commitOID := candidateControlGitObjectOID("commit", commitData)
	return hex.EncodeToString(commitOID[:]), nil
}

func canonicalCandidateControlValue(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}

func candidateControlGitObjectOID(kind string, data []byte) [sha1.Size]byte {
	object := make(
		[]byte,
		0,
		len(kind)+1+20+1+len(data),
	)
	object = append(object, kind...)
	object = append(object, ' ')
	object = append(object, fmt.Sprintf("%d", len(data))...)
	object = append(object, 0)
	object = append(object, data...)
	return sha1.Sum(object)
}

func validateCandidateControlDenial(probe CandidateControlDenialProbe, remote, ref, outcome string, expectedProviderCalls int64) error {
	if expectedProviderCalls < 0 || probe.Remote != remote || probe.Ref != ref ||
		probe.Outcome != outcome || probe.ProviderInvoked ||
		probe.ProviderCallsBefore != expectedProviderCalls ||
		probe.ProviderCallsAfter != expectedProviderCalls {
		return errors.New("candidate control denial probe is invalid")
	}
	return nil
}

func parseCandidateControlTimestamp(value string) (time.Time, error) {
	if !candidateControlTimestampPattern.MatchString(value) {
		return time.Time{}, errors.New("timestamp must be canonical UTC RFC3339")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("timestamp must be canonical UTC RFC3339")
	}
	return parsed.UTC(), nil
}

func sha256HexBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
