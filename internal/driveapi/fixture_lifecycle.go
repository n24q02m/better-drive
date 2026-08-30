package driveapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

const (
	CurrentFixtureLifecycleSchemaVersion = 1
	FixtureMutationSemantics             = "drive_candidate_fixture_quarantine_restore_requarantine_no_cas"
	FixturePhaseQuarantine               = "quarantine"
	FixturePhaseRestore                  = "restore"
	FixturePhaseRequarantine             = "requarantine"
)

type FixtureLifecycleCapability struct {
	SchemaVersion      int            `json:"schema_version"`
	FixtureID          string         `json:"fixture_id"`
	FixtureDigest      string         `json:"fixture_digest"`
	ArtifactDigest     string         `json:"artifact_digest"`
	Issuer             string         `json:"issuer"`
	Provider           string         `json:"provider"`
	AccountID          string         `json:"account_id"`
	RootID             string         `json:"root_id"`
	Namespace          string         `json:"namespace"`
	ObjectID           string         `json:"object_id"`
	OriginalParentID   string         `json:"original_parent_id"`
	QuarantineParentID string         `json:"quarantine_parent_id"`
	ProductionRootIDs  []string       `json:"production_root_ids"`
	Sequence           []string       `json:"sequence"`
	CreatedAt          time.Time      `json:"created_at"`
	ExpiresAt          time.Time      `json:"expires_at"`
	Nonce              string         `json:"nonce"`
	MutationSemantics  string         `json:"mutation_semantics"`
	ProductionDenied   bool           `json:"production_denied"`
	Initial            cleanup.Object `json:"initial"`
}

type FixtureLifecycleRequest struct {
	Capability   FixtureLifecycleCapability `json:"capability"`
	SignatureHex string                     `json:"signature"`
}

type FixtureLifecycleResult struct {
	FixtureID             string       `json:"fixture_id"`
	FixtureDigest         string       `json:"fixture_digest"`
	CapabilityDigest      string       `json:"capability_digest"`
	ArtifactDigest        string       `json:"artifact_digest"`
	ProductionDenialBound bool         `json:"production_denial_bound"`
	ReceiptOID            string       `json:"receipt_oid"`
	ReceiptState          string       `json:"receipt_state"`
	FinalParentID         string       `json:"final_parent_id"`
	Moves                 []MoveResult `json:"moves"`
}

type FixtureLifecycleExecutor struct {
	provider     *quarantineHTTPClient
	publicKey    ed25519.PublicKey
	receiptStore cleanup.FixtureLifecycleReceiptStore
	now          func() time.Time
}

func NewFixtureLifecycleExecutor(
	client *http.Client,
	accessToken string,
	publicKey ed25519.PublicKey,
	receiptStore cleanup.FixtureLifecycleReceiptStore,
) (*FixtureLifecycleExecutor, error) {
	return newFixtureLifecycleExecutor(client, googleDriveAPIBaseURL, accessToken, publicKey, receiptStore, time.Now)
}

func newFixtureLifecycleExecutor(
	client *http.Client,
	endpoint string,
	accessToken string,
	publicKey ed25519.PublicKey,
	receiptStore cleanup.FixtureLifecycleReceiptStore,
	now func() time.Time,
) (*FixtureLifecycleExecutor, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("fixture lifecycle public key is invalid")
	}
	if receiptStore == nil {
		return nil, errors.New("fixture lifecycle receipt store is required")
	}
	if now == nil {
		return nil, errors.New("fixture lifecycle clock is required")
	}
	provider, err := newQuarantineHTTPClient(client, endpoint, accessToken)
	if err != nil {
		return nil, err
	}
	return &FixtureLifecycleExecutor{
		provider:     provider,
		publicKey:    append(ed25519.PublicKey(nil), publicKey...),
		receiptStore: receiptStore,
		now:          now,
	}, nil
}

func FreezeFixtureLifecycleCapability(capability FixtureLifecycleCapability) (FixtureLifecycleCapability, error) {
	capability.ProductionRootIDs = append([]string(nil), capability.ProductionRootIDs...)
	sort.Strings(capability.ProductionRootIDs)
	capability.Sequence = append([]string(nil), capability.Sequence...)
	capability.FixtureDigest = ""
	if err := validateFixtureLifecycleCapability(capability, time.Time{}, false); err != nil {
		return FixtureLifecycleCapability{}, err
	}
	capability.FixtureDigest = fixtureLifecycleIdentityDigest(capability)
	if err := validateFixtureLifecycleCapability(capability, time.Time{}, true); err != nil {
		return FixtureLifecycleCapability{}, err
	}
	return capability, nil
}

func CanonicalFixtureLifecycleCapability(capability FixtureLifecycleCapability) ([]byte, error) {
	if err := validateFixtureLifecycleCapability(capability, time.Time{}, true); err != nil {
		return nil, err
	}
	copyCapability := capability
	copyCapability.ProductionRootIDs = append([]string(nil), capability.ProductionRootIDs...)
	sort.Strings(copyCapability.ProductionRootIDs)
	copyCapability.Sequence = append([]string(nil), capability.Sequence...)
	return json.Marshal(copyCapability)
}

func DecodeFixtureLifecycleRequest(data []byte) (FixtureLifecycleRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request FixtureLifecycleRequest
	if err := decoder.Decode(&request); err != nil {
		return FixtureLifecycleRequest{}, fmt.Errorf("decode fixture lifecycle request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return FixtureLifecycleRequest{}, errors.New("decode fixture lifecycle request: trailing JSON is not allowed")
	}
	if _, err := CanonicalFixtureLifecycleCapability(request.Capability); err != nil {
		return FixtureLifecycleRequest{}, err
	}
	signature, err := hex.DecodeString(request.SignatureHex)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return FixtureLifecycleRequest{}, errors.New("fixture lifecycle signature encoding is invalid")
	}
	return request, nil
}

func FixtureLifecycleCapabilityDigest(capability FixtureLifecycleCapability) (string, error) {
	canonical, err := CanonicalFixtureLifecycleCapability(capability)
	if err != nil {
		return "", err
	}
	return cleanup.Digest(canonical), nil
}

func VerifyFixtureLifecycleCapability(
	capability FixtureLifecycleCapability,
	signature []byte,
	publicKey ed25519.PublicKey,
	now time.Time,
) error {
	if len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return errors.New("fixture lifecycle signature material is invalid")
	}
	if err := validateFixtureLifecycleCapability(capability, now, true); err != nil {
		return err
	}
	canonical, err := CanonicalFixtureLifecycleCapability(capability)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, canonical, signature) {
		return errors.New("fixture lifecycle signature verification failed")
	}
	return nil
}

func (executor *FixtureLifecycleExecutor) Execute(ctx context.Context, request FixtureLifecycleRequest) (FixtureLifecycleResult, error) {
	if executor == nil || executor.provider == nil || executor.receiptStore == nil || executor.now == nil {
		return FixtureLifecycleResult{}, errors.New("fixture lifecycle executor is not configured")
	}
	if ctx == nil {
		return FixtureLifecycleResult{}, errors.New("context is nil")
	}
	now := executor.now().UTC()
	if err := validateFixtureLifecycleCapability(request.Capability, now, true); err != nil {
		return FixtureLifecycleResult{}, err
	}
	signature, err := hex.DecodeString(request.SignatureHex)
	if err != nil {
		return FixtureLifecycleResult{}, errors.New("fixture lifecycle signature encoding is invalid")
	}
	if err := VerifyFixtureLifecycleCapability(request.Capability, signature, executor.publicKey, now); err != nil {
		return FixtureLifecycleResult{}, err
	}
	canonical, err := CanonicalFixtureLifecycleCapability(request.Capability)
	if err != nil {
		return FixtureLifecycleResult{}, err
	}
	capabilityDigest := cleanup.Digest(canonical)
	receipt, receiptOID, err := executor.receiptStore.Begin(
		capabilityDigest,
		request.Capability.FixtureDigest,
		request.Capability.ArtifactDigest,
		request.Capability.Sequence,
	)
	if err != nil {
		return FixtureLifecycleResult{}, err
	}

	result := FixtureLifecycleResult{
		FixtureID:             request.Capability.FixtureID,
		FixtureDigest:         request.Capability.FixtureDigest,
		CapabilityDigest:      capabilityDigest,
		ArtifactDigest:        request.Capability.ArtifactDigest,
		ProductionDenialBound: request.Capability.ProductionDenied,
		Moves:                 make([]MoveResult, 0, len(request.Capability.Sequence)),
		ReceiptOID:            receiptOID,
		ReceiptState:          receipt.State,
	}
	expected := request.Capability.Initial
	targets := []string{
		request.Capability.QuarantineParentID,
		request.Capability.OriginalParentID,
		request.Capability.QuarantineParentID,
	}
	for index, phase := range request.Capability.Sequence {
		_, attemptingOID, err := executor.receiptStore.StartPhase(capabilityDigest, receiptOID, index, phase)
		if err != nil {
			return result, fmt.Errorf("%w: start fixture lifecycle phase %q: %w", ErrSettlementUnknown, phase, err)
		}
		result.ReceiptOID = attemptingOID
		result.ReceiptState = cleanup.FixtureReceiptAttempting
		move, err := executor.provider.move(ctx, moveRequest{
			Expected:            expected,
			DestinationParentID: targets[index],
			AttemptID:           fmt.Sprintf("fixture-%s-%d-%s", capabilityDigest[:16], index+1, phase),
		})
		result.Moves = append(result.Moves, move)
		if err != nil {
			return result, fmt.Errorf("%w: fixture lifecycle phase %q failed: %w", ErrSettlementUnknown, phase, err)
		}
		expected = fixtureExpectedState(expected, move.After)
		outcomeData, marshalErr := json.Marshal(move)
		if marshalErr != nil {
			return result, fmt.Errorf("%w: encode fixture phase outcome: %w", ErrSettlementUnknown, marshalErr)
		}
		receipt, receiptOID, err = executor.receiptStore.CompletePhase(
			capabilityDigest,
			attemptingOID,
			index,
			phase,
			cleanup.Digest(outcomeData),
		)
		if err != nil {
			return result, fmt.Errorf(
				"%w: persist fixture lifecycle phase %q settlement: %w",
				ErrSettlementUnknown,
				phase,
				err,
			)
		}
		result.ReceiptOID = receiptOID
		result.ReceiptState = receipt.State
	}
	result.FinalParentID = expected.ParentID
	if result.FinalParentID != request.Capability.QuarantineParentID {
		return result, fmt.Errorf("%w: fixture lifecycle final parent is not the exact quarantine parent", ErrSettlementUnknown)
	}
	if result.ReceiptState != cleanup.FixtureReceiptConsumed {
		return result, fmt.Errorf("%w: fixture lifecycle receipt did not reach consumed state", ErrSettlementUnknown)
	}
	return result, nil
}

func fixtureExpectedState(template cleanup.Object, state FileState) cleanup.Object {
	template.ID = state.ID
	template.ParentID = state.ParentID
	template.ContentHash = state.ContentHash
	template.Size = state.Size
	template.ModifiedAt = state.ModifiedAt
	template.Version = state.Version
	template.Generation = state.Generation
	template.ETag = state.ETag
	template.Trashed = state.Trashed
	return template
}

func validateFixtureLifecycleCapability(capability FixtureLifecycleCapability, now time.Time, requireDigest bool) error {
	if capability.SchemaVersion != CurrentFixtureLifecycleSchemaVersion {
		return fmt.Errorf("unsupported fixture lifecycle schema_version %d", capability.SchemaVersion)
	}
	for name, value := range map[string]string{
		"fixture ID": capability.FixtureID,
		"issuer":     capability.Issuer,
		"account ID": capability.AccountID,
		"namespace":  capability.Namespace,
		"nonce":      capability.Nonce,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "*?[]/\\\x00\r\n\t") {
			return fmt.Errorf("fixture lifecycle %s is invalid", name)
		}
	}
	if capability.Provider != "drive" || capability.MutationSemantics != FixtureMutationSemantics {
		return errors.New("fixture lifecycle provider or mutation semantics is invalid")
	}
	for name, value := range map[string]string{
		"root ID":              capability.RootID,
		"object ID":            capability.ObjectID,
		"original parent ID":   capability.OriginalParentID,
		"quarantine parent ID": capability.QuarantineParentID,
	} {
		if err := validateDriveID(value, "fixture lifecycle "+name); err != nil {
			return err
		}
	}
	if !capability.ProductionDenied || len(capability.ProductionRootIDs) == 0 {
		return errors.New("fixture lifecycle production denial is required")
	}
	seenProductionRoots := make(map[string]struct{}, len(capability.ProductionRootIDs))
	for _, rootID := range capability.ProductionRootIDs {
		if err := validateDriveID(rootID, "fixture lifecycle production root ID"); err != nil {
			return err
		}
		if _, duplicate := seenProductionRoots[rootID]; duplicate {
			return errors.New("fixture lifecycle production root IDs contain a duplicate")
		}
		seenProductionRoots[rootID] = struct{}{}
	}
	for _, candidateID := range []string{capability.RootID, capability.ObjectID, capability.OriginalParentID, capability.QuarantineParentID} {
		if _, production := seenProductionRoots[candidateID]; production {
			return errors.New("fixture lifecycle target overlaps a production root")
		}
	}
	if capability.RootID == capability.ObjectID || capability.OriginalParentID == capability.QuarantineParentID ||
		capability.ObjectID == capability.OriginalParentID || capability.ObjectID == capability.QuarantineParentID {
		return errors.New("fixture lifecycle object, root, and parent IDs are not isolated")
	}
	wantSequence := []string{FixturePhaseQuarantine, FixturePhaseRestore, FixturePhaseRequarantine}
	if len(capability.Sequence) != len(wantSequence) {
		return errors.New("fixture lifecycle sequence must contain exactly three phases")
	}
	for index := range wantSequence {
		if capability.Sequence[index] != wantSequence[index] {
			return errors.New("fixture lifecycle sequence must be quarantine, restore, requarantine")
		}
	}
	if !isExactSHA256(capability.ArtifactDigest) {
		return errors.New("fixture lifecycle artifact digest is invalid")
	}
	if capability.CreatedAt.IsZero() || capability.ExpiresAt.IsZero() || !capability.ExpiresAt.After(capability.CreatedAt) ||
		capability.ExpiresAt.Sub(capability.CreatedAt) > 2*time.Hour {
		return errors.New("fixture lifecycle validity interval is invalid")
	}
	if !now.IsZero() && (now.Before(capability.CreatedAt) || !now.Before(capability.ExpiresAt)) {
		return errors.New("fixture lifecycle capability is not currently valid")
	}
	initial := capability.Initial
	if initial.ID != capability.ObjectID || initial.ParentID != capability.OriginalParentID || initial.Provider != capability.Provider ||
		initial.AccountID != capability.AccountID || initial.RootID != capability.RootID || initial.Namespace != capability.Namespace {
		return errors.New("fixture lifecycle initial object is outside the exact fixture scope")
	}
	if initial.Class != cleanup.ClassExpectedFixture || initial.ObjectType != cleanup.ObjectTypeFile || initial.Trashed ||
		strings.TrimSpace(initial.ContentHash) == "" || initial.Size < 0 || initial.ModifiedAt.IsZero() ||
		strings.TrimSpace(initial.Version) == "" || strings.TrimSpace(initial.Generation) == "" || strings.TrimSpace(initial.ETag) == "" {
		return errors.New("fixture lifecycle initial object is not a fresh active fixture leaf")
	}
	if initial.OwnershipMarker != "fixture:"+capability.FixtureID ||
		initial.RestoreEvidence != "fixture:"+capability.FixtureID+":"+capability.ArtifactDigest {
		return errors.New("fixture lifecycle ownership or restore evidence is invalid")
	}
	if requireDigest {
		if !isExactSHA256(capability.FixtureDigest) || capability.FixtureDigest != fixtureLifecycleIdentityDigest(capability) {
			return errors.New("fixture lifecycle fixture digest mismatch")
		}
	}
	return nil
}

func fixtureLifecycleIdentityDigest(capability FixtureLifecycleCapability) string {
	copyCapability := capability
	copyCapability.FixtureDigest = ""
	copyCapability.ProductionRootIDs = append([]string(nil), capability.ProductionRootIDs...)
	sort.Strings(copyCapability.ProductionRootIDs)
	copyCapability.Sequence = append([]string(nil), capability.Sequence...)
	data, _ := json.Marshal(copyCapability)
	return cleanup.Digest(data)
}

func isExactSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}
