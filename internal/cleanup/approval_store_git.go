package cleanup

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	SchemaVersion uint64                  `json:"schema_version"`
	ApprovalID    string                  `json:"approval_id"`
	IntentRef     string                  `json:"intent_ref"`
	IntentOID     string                  `json:"intent_oid"`
	State         string                  `json:"state"`
	ExecutionID   string                  `json:"execution_id,omitempty"`
	Timestamp     time.Time               `json:"timestamp"`
	PreviousOID   string                  `json:"previous_oid,omitempty"`
	OwnerRisk     *OwnerRiskStateMetadata `json:"owner_risk,omitempty"`
}

type ApprovalSnapshot struct {
	Intent     Intent             `json:"intent"`
	Sealed     SealedIntentRecord `json:"sealed_intent"`
	State      StateRecord        `json:"state"`
	IntentOID  string             `json:"intent_oid"`
	StateOID   string             `json:"state_oid"`
	JournalOID string             `json:"journal_oid,omitempty"`
	LeaseOID   string             `json:"lease_oid,omitempty"`
}

// LeaseRecord is the authoritative destination lease record in refs/destination-leases/<hash>.
type LeaseRecord struct {
	SchemaVersion    uint64    `json:"schema_version"`
	IdentityHash     string    `json:"identity_hash"`
	ApprovalID       string    `json:"approval_id"`
	Owner            string    `json:"owner"`
	ExecutionID      string    `json:"execution_id,omitempty"`
	ClaimID          string    `json:"claim_id,omitempty"`
	RequestDigest    string    `json:"request_digest,omitempty"`
	ClaimDigest      string    `json:"claim_digest,omitempty"`
	OutcomeDigest    string    `json:"outcome_digest,omitempty"`
	ProviderRequests []string  `json:"provider_requests,omitempty"`
	Authority        string    `json:"authority,omitempty"`
	Generation       uint64    `json:"generation"`
	Fence            uint64    `json:"fence,omitempty"`
	State            string    `json:"state"`
	ExpiresAt        time.Time `json:"expires_at,omitempty"`
	Timestamp        time.Time `json:"timestamp"`
	PreviousOID      string    `json:"previous_oid,omitempty"`
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
	CreateSealedIntentAndState(intentRef, intentData, stateRef string, state StateRecord) (intentOID, stateOID string, err error)
	ReadRef(ref string) (oid string, exists bool, err error)
	ReadBlob(oid string) ([]byte, error)
}

type RuntimeRefMutation struct {
	Ref         string
	ExpectedOID string
	Data        string
}

type RuntimeAuthorityTransition struct {
	State   RuntimeRefMutation
	Journal RuntimeRefMutation
	Lease   RuntimeRefMutation
}

type RuntimeAuthorityTransitionResult struct {
	StateOID   string
	JournalOID string
	LeaseOID   string
}

// RuntimeAuthorityTransport commits state, journal, and destination lease refs
// as one CAS transaction. It cannot write drafts or create sealed intents.
type RuntimeAuthorityTransport interface {
	ReadRef(ref string) (oid string, exists bool, err error)
	ReadBlob(oid string) ([]byte, error)
	CommitRuntimeTransition(RuntimeAuthorityTransition) (RuntimeAuthorityTransitionResult, error)
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

func (t *LocalGitTransport) CreateSealedIntentAndState(intentRef, intentData, stateRef string, state StateRecord) (string, string, error) {
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
	state.IntentOID = intentOID
	stateData, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "", "", err
	}
	stateOID, err := t.Repo.WriteBlob(append(stateData, '\n'))
	if err != nil {
		return "", "", err
	}
	if err := t.Repo.AtomicUpdateRefs(
		GitRefUpdate{Ref: intentRef, NewOID: intentOID},
		GitRefUpdate{Ref: stateRef, NewOID: stateOID},
	); err != nil {
		return "", "", err
	}
	return intentOID, stateOID, nil
}

func (t *LocalGitTransport) CommitRuntimeTransition(transition RuntimeAuthorityTransition) (RuntimeAuthorityTransitionResult, error) {
	if t == nil || t.Repo == nil {
		return RuntimeAuthorityTransitionResult{}, errors.New("git repo is not configured")
	}
	for _, check := range []struct {
		name     string
		mutation RuntimeRefMutation
		prefix   string
	}{
		{name: "state", mutation: transition.State, prefix: "refs/cleanup-states/"},
		{name: "journal", mutation: transition.Journal, prefix: "refs/cleanup-journals/"},
		{name: "lease", mutation: transition.Lease, prefix: "refs/destination-leases/"},
	} {
		if !strings.HasPrefix(check.mutation.Ref, check.prefix) {
			return RuntimeAuthorityTransitionResult{}, fmt.Errorf(
				"runtime transport denied writing %s outside %s: %q",
				check.name,
				check.prefix,
				check.mutation.Ref,
			)
		}
		if check.mutation.Data == "" {
			return RuntimeAuthorityTransitionResult{}, fmt.Errorf("runtime %s data is required", check.name)
		}
	}

	stateOID, err := t.Repo.WriteBlob([]byte(transition.State.Data))
	if err != nil {
		return RuntimeAuthorityTransitionResult{}, err
	}
	journalOID, err := t.Repo.WriteBlob([]byte(transition.Journal.Data))
	if err != nil {
		return RuntimeAuthorityTransitionResult{}, err
	}
	leaseOID, err := t.Repo.WriteBlob([]byte(transition.Lease.Data))
	if err != nil {
		return RuntimeAuthorityTransitionResult{}, err
	}
	if err := t.Repo.AtomicUpdateRefs(
		GitRefUpdate{
			Ref:         transition.State.Ref,
			ExpectedOID: transition.State.ExpectedOID,
			NewOID:      stateOID,
		},
		GitRefUpdate{
			Ref:         transition.Journal.Ref,
			ExpectedOID: transition.Journal.ExpectedOID,
			NewOID:      journalOID,
		},
		GitRefUpdate{
			Ref:         transition.Lease.Ref,
			ExpectedOID: transition.Lease.ExpectedOID,
			NewOID:      leaseOID,
		},
	); err != nil {
		return RuntimeAuthorityTransitionResult{}, err
	}
	return RuntimeAuthorityTransitionResult{
		StateOID:   stateOID,
		JournalOID: journalOID,
		LeaseOID:   leaseOID,
	}, nil
}

// ApprovalStore manages cleanup drafts, sealed intents, and states via private Git.
type ApprovalStore struct {
	DraftTransport      DraftAuthorityTransport
	ActivationTransport ActivationAuthorityTransport
	RuntimeTransport    RuntimeAuthorityTransport
	Root                string // Root directory for local bare-repo backend
	Now                 func() time.Time
}

// NewApprovalStore creates an ApprovalStore backed by a protected local bare
// Git repository.
func NewApprovalStore(root string) (*ApprovalStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("approval store root is required")
	}
	repo, err := NewGitRepo(root)
	if err != nil {
		return nil, fmt.Errorf("open approval store: %w", err)
	}
	local := NewLocalGitTransport(repo)
	return &ApprovalStore{
		DraftTransport:      local,
		ActivationTransport: local,
		RuntimeTransport:    local,
		Root:                repo.Root,
		Now:                 time.Now,
	}, nil
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
		if err := decodeStrictJSONRecord(existingBytes, &current); err != nil {
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
	if err := decodeStrictJSONRecord(draftData, &draft); err != nil {
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
		IntentOID:     "",
		State:         ApprovalApproved,
		Timestamp:     now,
	}

	// 3. Create the intent blob, bind its authoritative OID into the initial
	// state blob, then create both refs through the activation authority.
	intentOID, stateOID, createErr := store.ActivationTransport.CreateSealedIntentAndState(
		intentRef, string(append(sealedData, '\n')),
		stateRef, stateRecord,
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
			if decodeStrictJSONRecord(exIntentBytes, &curIntent) == nil && decodeStrictJSONRecord(exStateBytes, &curState) == nil {
				curCanonical, _ := CanonicalApproval(curIntent.Approval)
				if bytes.Equal(curCanonical, canonical) && curIntent.IntentDigest == intent.IntentDigest &&
					curIntent.SignatureHex == intent.SignatureHex && curState.State == ApprovalApproved &&
					curState.IntentOID == exIntentOID {
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
	snapshot, err := store.ReadSnapshot(approvalID)
	if err != nil {
		return Intent{}, err
	}
	return snapshot.Intent, nil
}

func (store *ApprovalStore) ReadSnapshot(approvalID string) (ApprovalSnapshot, error) {
	if store == nil {
		return ApprovalSnapshot{}, errors.New("approval store is nil")
	}
	if err := validateOpaqueApprovalID(approvalID); err != nil {
		return ApprovalSnapshot{}, err
	}
	transport := store.RuntimeTransport
	if transport == nil && store.ActivationTransport != nil {
		if runtimeTransport, ok := store.ActivationTransport.(RuntimeAuthorityTransport); ok {
			transport = runtimeTransport
		}
	}
	if transport == nil {
		return ApprovalSnapshot{}, errors.New("runtime authority transport is not configured")
	}

	stateRef := StateRef(approvalID)
	stateOID, exists, err := transport.ReadRef(stateRef)
	if err != nil {
		return ApprovalSnapshot{}, err
	}
	if !exists {
		return ApprovalSnapshot{}, fmt.Errorf("state ref %q does not exist", stateRef)
	}
	if !gitOIDPattern.MatchString(stateOID) {
		return ApprovalSnapshot{}, errors.New("approval state ref does not resolve to an exact Git OID")
	}
	stateData, err := transport.ReadBlob(stateOID)
	if err != nil {
		return ApprovalSnapshot{}, fmt.Errorf("read state blob %q: %w", stateOID, err)
	}
	var stateRecord StateRecord
	if err := decodeStrictJSONRecord(stateData, &stateRecord); err != nil {
		return ApprovalSnapshot{}, fmt.Errorf("decode approval state: %w", err)
	}
	if stateRecord.SchemaVersion != CurrentApprovalSchemaVersion ||
		stateRecord.ApprovalID != approvalID ||
		stateRecord.IntentRef != IntentRef(approvalID) ||
		!gitOIDPattern.MatchString(stateRecord.IntentOID) {
		return ApprovalSnapshot{}, errors.New("approval state does not bind the expected sealed intent")
	}
	switch stateRecord.State {
	case ApprovalApproved, ApprovalClaimed, ApprovalConsumed, ApprovalNeedsReconciliation:
	default:
		return ApprovalSnapshot{}, fmt.Errorf("approval state %q is invalid", stateRecord.State)
	}

	currentIntentOID, intentExists, err := transport.ReadRef(stateRecord.IntentRef)
	if err != nil || !intentExists {
		return ApprovalSnapshot{}, fmt.Errorf("sealed intent ref %q absent for state %q: %w", stateRecord.IntentRef, approvalID, err)
	}
	if currentIntentOID != stateRecord.IntentOID {
		return ApprovalSnapshot{}, fmt.Errorf("sealed intent ref %q moved from bound intent OID %q to %q", stateRecord.IntentRef, stateRecord.IntentOID, currentIntentOID)
	}
	intentData, err := transport.ReadBlob(stateRecord.IntentOID)
	if err != nil {
		return ApprovalSnapshot{}, fmt.Errorf("read intent blob %q: %w", stateRecord.IntentOID, err)
	}
	var sealed SealedIntentRecord
	if err := decodeStrictJSONRecord(intentData, &sealed); err != nil {
		return ApprovalSnapshot{}, fmt.Errorf("decode sealed intent: %w", err)
	}
	canonicalApproval, err := CanonicalApproval(sealed.Approval)
	if err != nil ||
		sealed.SchemaVersion != CurrentApprovalSchemaVersion ||
		sealed.Approval.ApprovalID != approvalID ||
		sealed.IntentDigest != Digest(canonicalApproval) ||
		sealed.DraftRef != DraftRef(approvalID) ||
		!gitOIDPattern.MatchString(sealed.DraftOID) ||
		sealed.Issuer != sealed.Approval.Issuer ||
		sealed.CreatedAt.IsZero() {
		return ApprovalSnapshot{}, errors.New("sealed cleanup intent lineage is invalid")
	}
	signature, err := hex.DecodeString(sealed.SignatureHex)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return ApprovalSnapshot{}, errors.New("sealed cleanup intent signature is invalid")
	}

	journalOID, journalExists, err := transport.ReadRef(JournalRef(approvalID))
	if err != nil {
		return ApprovalSnapshot{}, err
	}
	if journalExists && !gitOIDPattern.MatchString(journalOID) {
		return ApprovalSnapshot{}, errors.New("cleanup journal ref does not resolve to an exact Git OID")
	}
	identityHash, err := QuarantineIdentityHash(sealed.Approval.QuarantineTarget)
	if err != nil {
		return ApprovalSnapshot{}, err
	}
	leaseOID, leaseExists, err := transport.ReadRef(LeaseRef(identityHash))
	if err != nil {
		return ApprovalSnapshot{}, err
	}
	if leaseExists && !gitOIDPattern.MatchString(leaseOID) {
		return ApprovalSnapshot{}, errors.New("cleanup lease ref does not resolve to an exact Git OID")
	}

	intent := Intent{
		SchemaVersion: sealed.SchemaVersion,
		IntentDigest:  sealed.IntentDigest,
		Approval:      sealed.Approval,
		SignatureHex:  sealed.SignatureHex,
		State:         stateRecord.State,
		CreatedAt:     sealed.CreatedAt,
	}
	return ApprovalSnapshot{
		Intent:     intent,
		Sealed:     sealed,
		State:      stateRecord,
		IntentOID:  stateRecord.IntentOID,
		StateOID:   stateOID,
		JournalOID: journalOID,
		LeaseOID:   leaseOID,
	}, nil
}
