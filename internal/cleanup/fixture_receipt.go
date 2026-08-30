package cleanup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	CurrentFixtureReceiptSchemaVersion = 1
	FixtureReceiptClaimed              = "claimed"
	FixtureReceiptAttempting           = "attempting"
	FixtureReceiptConsumed             = "consumed"
)

type FixtureLifecyclePhaseReceipt struct {
	Index         int    `json:"index"`
	Phase         string `json:"phase"`
	OutcomeDigest string `json:"outcome_digest"`
}

type FixtureLifecycleReceipt struct {
	SchemaVersion    int                            `json:"schema_version"`
	CapabilityDigest string                         `json:"capability_digest"`
	FixtureDigest    string                         `json:"fixture_digest"`
	ArtifactDigest   string                         `json:"artifact_digest"`
	Sequence         []string                       `json:"sequence"`
	State            string                         `json:"state"`
	PhaseIndex       int                            `json:"phase_index"`
	Phase            string                         `json:"phase,omitempty"`
	Completed        []FixtureLifecyclePhaseReceipt `json:"completed,omitempty"`
	Generation       uint64                         `json:"generation"`
	UpdatedAt        time.Time                      `json:"updated_at"`
}

type FixtureLifecycleReceiptStore interface {
	Begin(capabilityDigest, fixtureDigest, artifactDigest string, sequence []string) (FixtureLifecycleReceipt, string, error)
	StartPhase(capabilityDigest, expectedOID string, phaseIndex int, phase string) (FixtureLifecycleReceipt, string, error)
	CompletePhase(capabilityDigest, expectedOID string, phaseIndex int, phase, outcomeDigest string) (FixtureLifecycleReceipt, string, error)
}

type GitFixtureLifecycleReceiptStore struct {
	repo *GitRepo
	now  func() time.Time
}

func NewGitFixtureLifecycleReceiptStore(repo *GitRepo, now func() time.Time) (*GitFixtureLifecycleReceiptStore, error) {
	if repo == nil || strings.TrimSpace(repo.Root) == "" {
		return nil, errors.New("fixture lifecycle receipt Git repository is required")
	}
	if now == nil {
		return nil, errors.New("fixture lifecycle receipt clock is required")
	}
	if now().UTC().IsZero() {
		return nil, errors.New("fixture lifecycle receipt clock returned zero time")
	}
	return &GitFixtureLifecycleReceiptStore{repo: repo, now: now}, nil
}

func (store *GitFixtureLifecycleReceiptStore) Begin(
	capabilityDigest,
	fixtureDigest,
	artifactDigest string,
	sequence []string,
) (FixtureLifecycleReceipt, string, error) {
	if err := validateSHA256Hex(capabilityDigest, "fixture capability digest"); err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	if err := validateSHA256Hex(fixtureDigest, "fixture digest"); err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	if err := validateSHA256Hex(artifactDigest, "fixture artifact digest"); err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	if err := validateFixtureReceiptSequence(sequence); err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	receipt := FixtureLifecycleReceipt{
		SchemaVersion:    CurrentFixtureReceiptSchemaVersion,
		CapabilityDigest: capabilityDigest,
		FixtureDigest:    fixtureDigest,
		ArtifactDigest:   artifactDigest,
		Sequence:         append([]string(nil), sequence...),
		State:            FixtureReceiptClaimed,
		PhaseIndex:       0,
		Generation:       1,
		UpdatedAt:        store.now().UTC(),
	}
	oid, err := store.writeReceipt(receipt)
	if err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	if _, err := store.repo.CreateRef(FixtureLifecycleReceiptRef(capabilityDigest), oid); err != nil {
		current, currentOID, readErr := store.Read(capabilityDigest)
		if readErr == nil {
			return current, currentOID, fmt.Errorf("fixture lifecycle capability was already claimed or consumed: %w", err)
		}
		return FixtureLifecycleReceipt{}, "", err
	}
	return receipt, oid, nil
}

func (store *GitFixtureLifecycleReceiptStore) StartPhase(
	capabilityDigest,
	expectedOID string,
	phaseIndex int,
	phase string,
) (FixtureLifecycleReceipt, string, error) {
	current, currentOID, err := store.readExpected(capabilityDigest, expectedOID)
	if err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	if current.State != FixtureReceiptClaimed || current.PhaseIndex != phaseIndex || phaseIndex < 0 ||
		phaseIndex >= len(current.Sequence) || current.Sequence[phaseIndex] != phase {
		return FixtureLifecycleReceipt{}, "", errors.New("fixture lifecycle receipt is not ready for the requested phase")
	}
	current.State = FixtureReceiptAttempting
	current.Phase = phase
	current.Generation++
	current.UpdatedAt = store.now().UTC()
	return store.casReceipt(capabilityDigest, currentOID, current)
}

func (store *GitFixtureLifecycleReceiptStore) CompletePhase(
	capabilityDigest,
	expectedOID string,
	phaseIndex int,
	phase,
	outcomeDigest string,
) (FixtureLifecycleReceipt, string, error) {
	if err := validateSHA256Hex(outcomeDigest, "fixture phase outcome digest"); err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	current, currentOID, err := store.readExpected(capabilityDigest, expectedOID)
	if err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	if current.State != FixtureReceiptAttempting || current.PhaseIndex != phaseIndex || current.Phase != phase ||
		phaseIndex < 0 || phaseIndex >= len(current.Sequence) || current.Sequence[phaseIndex] != phase {
		return FixtureLifecycleReceipt{}, "", errors.New("fixture lifecycle receipt is not attempting the requested phase")
	}
	current.Completed = append(current.Completed, FixtureLifecyclePhaseReceipt{
		Index: phaseIndex, Phase: phase, OutcomeDigest: outcomeDigest,
	})
	current.PhaseIndex++
	current.Phase = ""
	if current.PhaseIndex == len(current.Sequence) {
		current.State = FixtureReceiptConsumed
	} else {
		current.State = FixtureReceiptClaimed
	}
	current.Generation++
	current.UpdatedAt = store.now().UTC()
	return store.casReceipt(capabilityDigest, currentOID, current)
}

func (store *GitFixtureLifecycleReceiptStore) Read(capabilityDigest string) (FixtureLifecycleReceipt, string, error) {
	if store == nil || store.repo == nil {
		return FixtureLifecycleReceipt{}, "", errors.New("fixture lifecycle receipt store is not configured")
	}
	if err := validateSHA256Hex(capabilityDigest, "fixture capability digest"); err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	oid, exists, err := store.repo.ReadRef(FixtureLifecycleReceiptRef(capabilityDigest))
	if err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	if !exists {
		return FixtureLifecycleReceipt{}, "", errors.New("fixture lifecycle receipt does not exist")
	}
	data, err := store.repo.ReadBlob(oid)
	if err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	var receipt FixtureLifecycleReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return FixtureLifecycleReceipt{}, "", errors.New("fixture lifecycle receipt is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return FixtureLifecycleReceipt{}, "", errors.New("fixture lifecycle receipt contains trailing data")
	}
	if err := validateFixtureLifecycleReceipt(receipt, capabilityDigest); err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	return receipt, oid, nil
}

func FixtureLifecycleReceiptRef(capabilityDigest string) string {
	return "refs/bdrive/fixture-receipts/" + capabilityDigest
}

func (store *GitFixtureLifecycleReceiptStore) readExpected(capabilityDigest, expectedOID string) (FixtureLifecycleReceipt, string, error) {
	if !gitOIDPattern.MatchString(expectedOID) {
		return FixtureLifecycleReceipt{}, "", errors.New("fixture lifecycle expected receipt OID is invalid")
	}
	current, currentOID, err := store.Read(capabilityDigest)
	if err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	if currentOID != expectedOID {
		return FixtureLifecycleReceipt{}, "", errors.New("fixture lifecycle receipt changed from its expected OID")
	}
	return current, currentOID, nil
}

func (store *GitFixtureLifecycleReceiptStore) casReceipt(
	capabilityDigest,
	expectedOID string,
	receipt FixtureLifecycleReceipt,
) (FixtureLifecycleReceipt, string, error) {
	newOID, err := store.writeReceipt(receipt)
	if err != nil {
		return FixtureLifecycleReceipt{}, "", err
	}
	if err := store.repo.CAS(FixtureLifecycleReceiptRef(capabilityDigest), expectedOID, newOID); err != nil {
		return FixtureLifecycleReceipt{}, "", fmt.Errorf("fixture lifecycle receipt CAS failed: %w", err)
	}
	return receipt, newOID, nil
}

func (store *GitFixtureLifecycleReceiptStore) writeReceipt(receipt FixtureLifecycleReceipt) (string, error) {
	if err := validateFixtureLifecycleReceipt(receipt, receipt.CapabilityDigest); err != nil {
		return "", err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	return store.repo.WriteBlob(data)
}

func validateFixtureLifecycleReceipt(receipt FixtureLifecycleReceipt, expectedCapabilityDigest string) error {
	if receipt.SchemaVersion != CurrentFixtureReceiptSchemaVersion || receipt.CapabilityDigest != expectedCapabilityDigest {
		return errors.New("fixture lifecycle receipt identity is invalid")
	}
	for name, digest := range map[string]string{
		"capability": receipt.CapabilityDigest,
		"fixture":    receipt.FixtureDigest,
		"artifact":   receipt.ArtifactDigest,
	} {
		if err := validateSHA256Hex(digest, "fixture receipt "+name+" digest"); err != nil {
			return err
		}
	}
	if err := validateFixtureReceiptSequence(receipt.Sequence); err != nil {
		return err
	}
	if receipt.Generation == 0 || receipt.UpdatedAt.IsZero() || receipt.PhaseIndex < 0 || receipt.PhaseIndex > len(receipt.Sequence) {
		return errors.New("fixture lifecycle receipt generation, time, or phase index is invalid")
	}
	if len(receipt.Completed) != receipt.PhaseIndex {
		return errors.New("fixture lifecycle receipt completed phase count is invalid")
	}
	for index, completed := range receipt.Completed {
		if completed.Index != index || completed.Phase != receipt.Sequence[index] {
			return errors.New("fixture lifecycle receipt completed sequence is invalid")
		}
		if err := validateSHA256Hex(completed.OutcomeDigest, "fixture phase outcome digest"); err != nil {
			return err
		}
	}
	switch receipt.State {
	case FixtureReceiptClaimed:
		if receipt.PhaseIndex >= len(receipt.Sequence) || receipt.Phase != "" {
			return errors.New("claimed fixture lifecycle receipt phase is invalid")
		}
	case FixtureReceiptAttempting:
		if receipt.PhaseIndex >= len(receipt.Sequence) || receipt.Phase != receipt.Sequence[receipt.PhaseIndex] {
			return errors.New("attempting fixture lifecycle receipt phase is invalid")
		}
	case FixtureReceiptConsumed:
		if receipt.PhaseIndex != len(receipt.Sequence) || receipt.Phase != "" {
			return errors.New("consumed fixture lifecycle receipt phase is invalid")
		}
	default:
		return errors.New("fixture lifecycle receipt state is invalid")
	}
	return nil
}

func validateFixtureReceiptSequence(sequence []string) error {
	if len(sequence) != 3 {
		return errors.New("fixture lifecycle receipt sequence must contain exactly three phases")
	}
	for index, phase := range sequence {
		if err := validateOpaqueTransactionField(phase, fmt.Sprintf("fixture phase %d", index)); err != nil {
			return err
		}
	}
	return nil
}
