package cleanup

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DraftRefFormat: refs/cleanup-drafts/<approval-id>
func DraftRef(approvalID string) string {
	return "refs/cleanup-drafts/" + approvalID
}

// IntentRefFormat: refs/cleanup-intents/<approval-id>
func IntentRef(approvalID string) string {
	return "refs/cleanup-intents/" + approvalID
}

// StateRefFormat: refs/cleanup-states/<approval-id>
func StateRef(approvalID string) string {
	return "refs/cleanup-states/" + approvalID
}

// LeaseRefFormat: refs/destination-leases/<identity-hash>
func LeaseRef(identityHash string) string {
	return "refs/destination-leases/" + identityHash
}

// JournalRefFormat: refs/cleanup-journals/<approval-id>
func JournalRef(approvalID string) string {
	return "refs/cleanup-journals/" + approvalID
}

// DraftRecord is the canonical envelope stored in refs/cleanup-drafts/<id>.
type DraftRecord struct {
	SchemaVersion int       `json:"schema_version"`
	DraftDigest   string    `json:"draft_digest"`
	Approval      Approval  `json:"approval"`
	CreatedAt     time.Time `json:"created_at"`
}

// SealedIntentRecord is the canonical sealed record stored in refs/cleanup-intents/<id>.
type SealedIntentRecord struct {
	SchemaVersion int       `json:"schema_version"`
	IntentDigest  string    `json:"intent_digest"`
	DraftRef      string    `json:"draft_ref"`
	DraftOID      string    `json:"draft_oid"`
	Approval      Approval  `json:"approval"`
	SignatureHex  string    `json:"signature"`
	Issuer        string    `json:"issuer"`
	CreatedAt     time.Time `json:"created_at"`
}

// StateRecord is the hash-chained lifecycle state in refs/cleanup-states/<id>.
type StateRecord struct {
	SchemaVersion uint64    `json:"schema_version"`
	ApprovalID    string    `json:"approval_id"`
	IntentRef     string    `json:"intent_ref"`
	IntentOID     string    `json:"intent_oid"`
	State         string    `json:"state"`
	ExecutionID   string    `json:"execution_id,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
	PreviousOID   string    `json:"previous_oid,omitempty"`
}

// LeaseRecord is the authoritative destination lease record in refs/destination-leases/<hash>.
type LeaseRecord struct {
	SchemaVersion uint64    `json:"schema_version"`
	IdentityHash  string    `json:"identity_hash"`
	ApprovalID    string    `json:"approval_id"`
	Owner         string    `json:"owner"`
	ExecutionID   string    `json:"execution_id,omitempty"`
	Generation    uint64    `json:"generation"`
	State         string    `json:"state"` // free, claimed, needs_reconciliation, consumed
	Timestamp     time.Time `json:"timestamp"`
	PreviousOID   string    `json:"previous_oid,omitempty"`
}

// DraftAuthorityTransport allows create-only draft writing. It is restricted from
// writing sealed intent, state, lease, or journal refs.
type DraftAuthorityTransport interface {
	CreateDraft(ref string, data []byte) (oid string, err error)
	ReadRef(ref string) (oid string, exists bool, err error)
	ReadBlob(oid string) ([]byte, error)
}

// ActivationAuthorityTransport allows creating sealed intent and initial state atomically.
// It cannot write drafts or runtime journals.
type ActivationAuthorityTransport interface {
	CreateSealedIntentAndState(intentRef, intentData, stateRef, stateData string) (intentOID, stateOID string, err error)
	ReadRef(ref string) (oid string, exists bool, err error)
	ReadBlob(oid string) ([]byte, error)
}

// RuntimeAuthorityTransport allows updating state, journal, and destination lease CAS refs.
// It cannot write drafts or create new sealed intents.
type RuntimeAuthorityTransport interface {
	ReadRef(ref string) (oid string, exists bool, err error)
	ReadBlob(oid string) ([]byte, error)
	CASState(ref, expectedOID, newData string) (newOID string, err error)
	CASLease(ref, expectedOID, newData string) (newOID string, err error)
	AppendJournal(ref, expectedHeadOID, recordData string) (newHeadOID string, err error)
}

// LocalGitTransport wraps *GitRepo into the authority-scoped transport interfaces.
type LocalGitTransport struct {
	Repo *GitRepo
}

func NewLocalGitTransport(repo *GitRepo) *LocalGitTransport {
	return &LocalGitTransport{Repo: repo}
}

func (t *LocalGitTransport) ReadRef(ref string) (string, bool, error) {
	if t.Repo == nil {
		return "", false, errors.New("git repo is not configured")
	}
	return t.Repo.ReadRef(ref)
}

func (t *LocalGitTransport) ReadBlob(oid string) ([]byte, error) {
	if t.Repo == nil {
		return nil, errors.New("git repo is not configured")
	}
	return t.Repo.ReadBlob(oid)
}

func (t *LocalGitTransport) CreateDraft(ref string, data []byte) (string, error) {
	if !strings.HasPrefix(ref, "refs/cleanup-drafts/") {
		return "", fmt.Errorf("draft transport denied writing outside refs/cleanup-drafts/: %q", ref)
	}
	oid, err := t.Repo.WriteBlob(data)
	if err != nil {
		return "", err
	}
	if _, err := t.Repo.CreateRef(ref, oid); err != nil {
		existingOID, exists, readErr := t.Repo.ReadRef(ref)
		if readErr == nil && exists {
			return existingOID, err
		}
		return "", err
	}
	return oid, nil
}

func (t *LocalGitTransport) CreateSealedIntentAndState(intentRef, intentData, stateRef, stateData string) (string, string, error) {
	if !strings.HasPrefix(intentRef, "refs/cleanup-intents/") {
		return "", "", fmt.Errorf("activation transport denied writing intent outside refs/cleanup-intents/: %q", intentRef)
	}
	if !strings.HasPrefix(stateRef, "refs/cleanup-states/") {
		return "", "", fmt.Errorf("activation transport denied writing state outside refs/cleanup-states/: %q", stateRef)
	}
	intentOID, err := t.Repo.WriteBlob([]byte(intentData))
	if err != nil {
		return "", "", err
	}
	stateOID, err := t.Repo.WriteBlob([]byte(stateData))
	if err != nil {
		return "", "", err
	}
	if err := t.Repo.AtomicCreateTwoRefs(intentRef, intentOID, stateRef, stateOID); err != nil {
		return "", "", err
	}
	return intentOID, stateOID, nil
}

func (t *LocalGitTransport) CASState(ref, expectedOID, newData string) (string, error) {
	if !strings.HasPrefix(ref, "refs/cleanup-states/") {
		return "", fmt.Errorf("runtime transport denied writing outside refs/cleanup-states/: %q", ref)
	}
	newOID, err := t.Repo.WriteBlob([]byte(newData))
	if err != nil {
		return "", err
	}
	if err := t.Repo.CAS(ref, expectedOID, newOID); err != nil {
		return "", err
	}
	return newOID, nil
}

func (t *LocalGitTransport) CASLease(ref, expectedOID, newData string) (string, error) {
	if !strings.HasPrefix(ref, "refs/destination-leases/") {
		return "", fmt.Errorf("runtime transport denied writing outside refs/destination-leases/: %q", ref)
	}
	newOID, err := t.Repo.WriteBlob([]byte(newData))
	if err != nil {
		return "", err
	}
	if err := t.Repo.CAS(ref, expectedOID, newOID); err != nil {
		return "", err
	}
	return newOID, nil
}

func (t *LocalGitTransport) AppendJournal(ref, expectedHeadOID, recordData string) (string, error) {
	if !strings.HasPrefix(ref, "refs/cleanup-journals/") {
		return "", fmt.Errorf("runtime transport denied writing outside refs/cleanup-journals/: %q", ref)
	}
	newOID, err := t.Repo.WriteBlob([]byte(recordData))
	if err != nil {
		return "", err
	}
	if err := t.Repo.CAS(ref, expectedHeadOID, newOID); err != nil {
		return "", err
	}
	return newOID, nil
}

// ApprovalStore manages cleanup drafts, sealed intents, and states via private Git.
type ApprovalStore struct {
	DraftTransport      DraftAuthorityTransport
	ActivationTransport ActivationAuthorityTransport
	RuntimeTransport    RuntimeAuthorityTransport
	Root                string // Root directory for local bare-repo backend
	Now                 func() time.Time
}

// NewApprovalStore creates an ApprovalStore backed by a local bare Git repository.
func NewApprovalStore(root string) *ApprovalStore {
	if strings.TrimSpace(root) == "" {
		return &ApprovalStore{Now: time.Now}
	}
	repo, err := NewGitRepo(root)
	if err != nil {
		return &ApprovalStore{Root: root, Now: time.Now}
	}
	local := NewLocalGitTransport(repo)
	return &ApprovalStore{
		DraftTransport:      local,
		ActivationTransport: local,
		RuntimeTransport:    local,
		Root:                root,
		Now:                 time.Now,
	}
}

func (store *ApprovalStore) now() time.Time {
	if store != nil && store.Now != nil {
		return store.Now().UTC()
	}
	return time.Now().UTC()
}

func (store *ApprovalStore) Prepare(approval Approval) (DraftRecord, error) {
	if store == nil || store.DraftTransport == nil {
		return DraftRecord{}, errors.New("draft authority transport is not configured")
	}
	canonical, err := CanonicalApproval(approval)
	if err != nil {
		return DraftRecord{}, err
	}
	draftRecord := DraftRecord{
		SchemaVersion: CurrentApprovalSchemaVersion,
		DraftDigest:   Digest(canonical),
		Approval:      approval,
		CreatedAt:     store.now(),
	}
	data, err := json.MarshalIndent(draftRecord, "", "  ")
	if err != nil {
		return DraftRecord{}, err
	}
	ref := DraftRef(approval.ApprovalID)
	_, createErr := store.DraftTransport.CreateDraft(ref, append(data, '\n'))
	if createErr != nil {
		// If ref exists, read back and verify exact byte equality.
		existingOID, exists, readErr := store.DraftTransport.ReadRef(ref)
		if readErr != nil || !exists {
			return DraftRecord{}, createErr
		}
		existingBytes, blobErr := store.DraftTransport.ReadBlob(existingOID)
		if blobErr != nil {
			return DraftRecord{}, createErr
		}
		var current DraftRecord
		if err := json.Unmarshal(existingBytes, &current); err != nil {
			return DraftRecord{}, fmt.Errorf("decode existing draft: %w", err)
		}
		currentCanonical, err := CanonicalApproval(current.Approval)
		if err != nil || !bytes.Equal(currentCanonical, canonical) || current.DraftDigest != draftRecord.DraftDigest {
			return DraftRecord{}, errors.New("foreign draft exists for approval ID")
		}
		return current, nil
	}
	return draftRecord, nil
}

func (store *ApprovalStore) Activate(intent Intent) error {
	if store == nil || store.ActivationTransport == nil {
		return errors.New("activation authority transport is not configured")
	}
	if intent.SchemaVersion != CurrentApprovalSchemaVersion || intent.IntentDigest == "" || intent.Approval.ApprovalID == "" || intent.State != ApprovalApproved {
		return errors.New("sealed intent is incomplete or not approved")
	}
	canonical, err := CanonicalApproval(intent.Approval)
	if err != nil {
		return fmt.Errorf("sealed intent approval is invalid: %w", err)
	}
	if intent.IntentDigest != Digest(canonical) {
		return errors.New("sealed intent digest does not match canonical approval")
	}
	signature, err := hex.DecodeString(intent.SignatureHex)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("sealed intent signature is not valid Ed25519 hex")
	}

	// 1. Verify draft exists and matches canonical approval.
	draftRef := DraftRef(intent.Approval.ApprovalID)
	draftOID, exists, err := store.ActivationTransport.ReadRef(draftRef)
	if err != nil || !exists {
		return fmt.Errorf("draft must exist before activation: %w", err)
	}
	draftData, err := store.ActivationTransport.ReadBlob(draftOID)
	if err != nil {
		return fmt.Errorf("read draft blob %q: %w", draftOID, err)
	}
	var draft DraftRecord
	if err := json.Unmarshal(draftData, &draft); err != nil {
		return fmt.Errorf("decode existing draft: %w", err)
	}
	draftCanonical, err := CanonicalApproval(draft.Approval)
	if err != nil || draft.SchemaVersion != CurrentApprovalSchemaVersion ||
		draft.DraftDigest != Digest(draftCanonical) || !bytes.Equal(draftCanonical, canonical) {
		return errors.New("draft approval does not match sealed intent")
	}

	// 2. Prepare sealed intent and initial state records.
	now := store.now()
	sealedRecord := SealedIntentRecord{
		SchemaVersion: CurrentApprovalSchemaVersion,
		IntentDigest:  intent.IntentDigest,
		DraftRef:      draftRef,
		DraftOID:      draftOID,
		Approval:      intent.Approval,
		SignatureHex:  intent.SignatureHex,
		Issuer:        intent.Approval.Issuer,
		CreatedAt:     now,
	}
	sealedData, err := json.MarshalIndent(sealedRecord, "", "  ")
	if err != nil {
		return err
	}
	intentRef := IntentRef(intent.Approval.ApprovalID)
	stateRef := StateRef(intent.Approval.ApprovalID)

	stateRecord := StateRecord{
		SchemaVersion: CurrentApprovalSchemaVersion,
		ApprovalID:    intent.Approval.ApprovalID,
		IntentRef:     intentRef,
		IntentOID:     Digest(sealedData),
		State:         ApprovalApproved,
		Timestamp:     now,
	}
	stateData, err := json.MarshalIndent(stateRecord, "", "  ")
	if err != nil {
		return err
	}

	// 3. Atomically create intent and state refs.
	intentOID, stateOID, createErr := store.ActivationTransport.CreateSealedIntentAndState(
		intentRef, string(append(sealedData, '\n')),
		stateRef, string(append(stateData, '\n')),
	)
	if createErr != nil {
		// Idempotency: check if both exist and match.
		exIntentOID, exIntentExists, _ := store.ActivationTransport.ReadRef(intentRef)
		exStateOID, exStateExists, _ := store.ActivationTransport.ReadRef(stateRef)
		if exIntentExists && exStateExists {
			exIntentBytes, _ := store.ActivationTransport.ReadBlob(exIntentOID)
			exStateBytes, _ := store.ActivationTransport.ReadBlob(exStateOID)
			var curIntent SealedIntentRecord
			var curState StateRecord
			if json.Unmarshal(exIntentBytes, &curIntent) == nil && json.Unmarshal(exStateBytes, &curState) == nil {
				curCanonical, _ := CanonicalApproval(curIntent.Approval)
				if bytes.Equal(curCanonical, canonical) && curIntent.IntentDigest == intent.IntentDigest &&
					curIntent.SignatureHex == intent.SignatureHex && curState.State == ApprovalApproved {
					return nil
				}
			}
			return errors.New("foreign sealed intent or state exists")
		}
		if exIntentExists != exStateExists {
			return errors.New("split sealed intent/state error: reconciliation required")
		}
		return createErr
	}
	if intentOID == "" || stateOID == "" {
		return errors.New("sealed intent or state OID is empty")
	}
	return nil
}

func (store *ApprovalStore) ReadState(approvalID string) (Intent, error) {
	if store == nil {
		return Intent{}, errors.New("approval store is nil")
	}
	if err := validateOpaqueApprovalID(approvalID); err != nil {
		return Intent{}, err
	}
	transport := store.RuntimeTransport
	if transport == nil {
		if store.ActivationTransport != nil {
			transport = store.ActivationTransport.(RuntimeAuthorityTransport)
		}
	}
	if transport == nil {
		return Intent{}, errors.New("runtime authority transport is not configured")
	}
	stateRef := StateRef(approvalID)
	stateOID, exists, err := transport.ReadRef(stateRef)
	if err != nil {
		return Intent{}, err
	}
	if !exists {
		return Intent{}, fmt.Errorf("state ref %q does not exist", stateRef)
	}
	stateData, err := transport.ReadBlob(stateOID)
	if err != nil {
		return Intent{}, fmt.Errorf("read state blob %q: %w", stateOID, err)
	}
	var stateRecord StateRecord
	if err := json.Unmarshal(stateData, &stateRecord); err != nil {
		return Intent{}, fmt.Errorf("decode approval state: %w", err)
	}

	intentRef := stateRecord.IntentRef
	intentOID, intentExists, err := transport.ReadRef(intentRef)
	if err != nil || !intentExists {
		return Intent{}, fmt.Errorf("sealed intent ref %q absent for state %q: %w", intentRef, approvalID, err)
	}
	intentData, err := transport.ReadBlob(intentOID)
	if err != nil {
		return Intent{}, fmt.Errorf("read intent blob %q: %w", intentOID, err)
	}
	var sealed SealedIntentRecord
	if err := json.Unmarshal(intentData, &sealed); err != nil {
		return Intent{}, fmt.Errorf("decode sealed intent: %w", err)
	}
	return Intent{
		SchemaVersion: sealed.SchemaVersion,
		IntentDigest:  sealed.IntentDigest,
		Approval:      sealed.Approval,
		SignatureHex:  sealed.SignatureHex,
		State:         stateRecord.State,
		CreatedAt:     sealed.CreatedAt,
	}, nil
}
