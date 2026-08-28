// Package retention owns provider-neutral retention decisions. It separates
// deterministic planning from capability-gated provider execution.
package retention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/n24q02m/better-drive/internal/r2api"
)

type Provider string

const (
	ProviderDrive Provider = "drive"
	ProviderR2    Provider = "r2"
)

type DeletePolicy string

const (
	DeletePolicyNone       DeletePolicy = "none"
	DeletePolicyQuarantine DeletePolicy = "quarantine"
)

type Policy struct {
	ID                     string        `json:"id"`
	Provider               Provider      `json:"provider"`
	DeletePolicy           DeletePolicy  `json:"delete_policy"`
	MinCompleteRestoreSets int           `json:"min_complete_restore_sets"`
	MaxObjects             int           `json:"max_objects"`
	MaxBytes               int64         `json:"max_bytes"`
	MinimumObjectAge       time.Duration `json:"minimum_object_age"`
	QuarantineBucket       string        `json:"quarantine_bucket,omitempty"`
	QuarantinePrefix       string        `json:"quarantine_prefix,omitempty"`
	ActivatedAt            time.Time     `json:"activated_at"`
}

func (policy Policy) Normalize() Policy {
	if policy.Provider == ProviderDrive && policy.DeletePolicy == "" {
		policy.DeletePolicy = DeletePolicyNone
	}
	if policy.MaxObjects == 0 {
		policy.MaxObjects = 10000
	}
	if policy.MaxBytes == 0 {
		policy.MaxBytes = 1 << 30
	}
	return policy
}

func (policy Policy) Validate(now time.Time) error {
	policy = policy.Normalize()
	if policy.ID == "" || (policy.Provider != ProviderDrive && policy.Provider != ProviderR2) {
		return errors.New("retention policy ID and provider are required")
	}
	if policy.DeletePolicy != DeletePolicyNone && policy.DeletePolicy != DeletePolicyQuarantine {
		return fmt.Errorf("unsupported delete policy %q", policy.DeletePolicy)
	}
	if policy.MinCompleteRestoreSets < 2 {
		return errors.New("min_complete_restore_sets must be >= 2")
	}
	if policy.MaxObjects <= 0 || policy.MaxBytes <= 0 {
		return errors.New("retention restore-set and budget limits are invalid")
	}
	if policy.ActivatedAt.IsZero() {
		return errors.New("retention policy activation time is required")
	}
	if !policy.ActivatedAt.Before(now) && !policy.ActivatedAt.Equal(now) {
		return errors.New("retention policy activation timestamp is in the future")
	}
	if policy.DeletePolicy == DeletePolicyQuarantine {
		if strings.TrimSpace(policy.QuarantineBucket) == "" {
			return errors.New("quarantine bucket is required")
		}
		if policy.MinimumObjectAge <= 0 {
			return errors.New("minimum object age must be > 0 for quarantine")
		}
	}
	return nil
}

func PolicyDigest(policy Policy) string {
	policy = policy.Normalize()
	data, _ := json.Marshal(policy)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Object is inventory metadata only. Hash/version/ETag are immutable evidence
// used to form exact mutation requests.
type Object struct {
	Provider    string    `json:"provider"`
	AccountID   string    `json:"account_id"`
	RootID      string    `json:"root_id"`
	Namespace   string    `json:"namespace"`
	Bucket      string    `json:"bucket"`
	Key         string    `json:"key"`
	ObjectID    string    `json:"object_id"`
	Version     string    `json:"version"`
	Generation  string    `json:"generation"`
	ETag        string    `json:"etag"`
	ParentID    string    `json:"parent_id"`
	Size        int64     `json:"size"`
	Hash        string    `json:"hash"`
	ModifiedAt  time.Time `json:"modified_at"`
	Optional    bool      `json:"optional"`
	Quarantined bool      `json:"quarantined"`
}

type ReplicaEvidence struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	AccountID string `json:"account_id"`
	RootID    string `json:"root_id"`
	Namespace string `json:"namespace"`
	Required  bool   `json:"required"`
	Complete  bool   `json:"complete"`
	Evidence  string `json:"evidence,omitempty"`
}

type RestoreSet struct {
	ID        string            `json:"id"`
	CreatedAt time.Time         `json:"created_at"`
	Complete  bool              `json:"complete"`
	Replicas  []ReplicaEvidence `json:"replicas"`
}

type Inventory struct {
	AccountID   string       `json:"account_id"`
	RootID      string       `json:"root_id"`
	Namespace   string       `json:"namespace"`
	CapturedAt  time.Time    `json:"captured_at"`
	Objects     []Object     `json:"objects"`
	RestoreSets []RestoreSet `json:"restore_sets"`
	Hash        string       `json:"hash,omitempty"`
}

func InventoryDigest(inventory Inventory) string {
	copyInventory := inventory
	copyInventory.Hash = ""
	data, _ := json.Marshal(copyInventory)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateInventoryScope(inventory Inventory, owner OwnershipMarker) error {
	if strings.TrimSpace(inventory.AccountID) == "" || strings.TrimSpace(inventory.RootID) == "" || strings.TrimSpace(inventory.Namespace) == "" {
		return errors.New("inventory ownership scope is required")
	}
	if inventory.AccountID != owner.AccountID || inventory.RootID != owner.RootID || inventory.Namespace != owner.Namespace {
		return errors.New("inventory ownership scope does not match owner")
	}
	return nil
}

func validateObjectScope(policy Policy, inventory Inventory, owner OwnershipMarker, object Object) error {
	if object.Provider != string(policy.Provider) ||
		object.AccountID != owner.AccountID || object.AccountID != inventory.AccountID ||
		object.RootID != owner.RootID || object.RootID != inventory.RootID ||
		object.Namespace != owner.Namespace || object.Namespace != inventory.Namespace {
		return fmt.Errorf("object %q ownership scope does not match policy, owner, and inventory", object.ObjectID)
	}
	return nil
}

func validateRequiredRestoreReplica(inventory Inventory, expectedProvider Provider, setID string, replica ReplicaEvidence) error {
	switch {
	case strings.TrimSpace(replica.ID) == "":
		return fmt.Errorf("restore set %q required replica ID is required", setID)
	case strings.TrimSpace(replica.Provider) == "":
		return fmt.Errorf("restore set %q required replica %q provider is required", setID, replica.ID)
	case replica.Provider != string(expectedProvider):
		return fmt.Errorf("restore set %q required replica %q provider does not match retention policy", setID, replica.ID)
	case strings.TrimSpace(replica.AccountID) == "":
		return fmt.Errorf("restore set %q required replica %q account is required", setID, replica.ID)
	case strings.TrimSpace(replica.RootID) == "":
		return fmt.Errorf("restore set %q required replica %q root is required", setID, replica.ID)
	case strings.TrimSpace(replica.Namespace) == "":
		return fmt.Errorf("restore set %q required replica %q namespace is required", setID, replica.ID)
	case strings.TrimSpace(replica.Evidence) == "":
		return fmt.Errorf("restore set %q required replica %q evidence is required", setID, replica.ID)
	case replica.AccountID != inventory.AccountID || replica.RootID != inventory.RootID || replica.Namespace != inventory.Namespace:
		return fmt.Errorf("restore set %q required replica %q scope does not match inventory", setID, replica.ID)
	case !replica.Complete:
		return fmt.Errorf("restore set %q required replica %q is incomplete", setID, replica.ID)
	default:
		return nil
	}
}

func NewestCompleteRestoreSets(inventory Inventory, minimum int, expectedProvider Provider) ([]RestoreSet, error) {
	if minimum < 0 {
		return nil, errors.New("minimum restore sets must not be negative")
	}
	if expectedProvider != ProviderDrive && expectedProvider != ProviderR2 {
		return nil, fmt.Errorf("restore-set expected provider %q is invalid", expectedProvider)
	}
	sets := append([]RestoreSet(nil), inventory.RestoreSets...)
	sort.SliceStable(sets, func(i, j int) bool {
		if sets[i].CreatedAt.Equal(sets[j].CreatedAt) {
			return sets[i].ID > sets[j].ID
		}
		return sets[i].CreatedAt.After(sets[j].CreatedAt)
	})
	complete := make([]RestoreSet, 0, len(sets))
	for _, set := range sets {
		if set.ID == "" || set.CreatedAt.IsZero() {
			return nil, errors.New("restore set identity and timestamp are required")
		}
		requiredReplicas := 0
		for _, replica := range set.Replicas {
			if !replica.Required {
				continue
			}
			requiredReplicas++
			if err := validateRequiredRestoreReplica(inventory, expectedProvider, set.ID, replica); err != nil {
				return nil, err
			}
		}
		if set.Complete && requiredReplicas == 0 {
			return nil, fmt.Errorf("restore set %q marked complete without a required replica", set.ID)
		}
		if set.Complete {
			complete = append(complete, set)
		}
	}
	if len(complete) < minimum {
		return nil, fmt.Errorf("only %d newest complete restore sets available, require %d", len(complete), minimum)
	}
	return append([]RestoreSet(nil), complete[:minimum]...), nil
}

type OwnershipMarker struct {
	OwnerID   string `json:"owner_id"`
	AccountID string `json:"account_id"`
	RootID    string `json:"root_id"`
	Namespace string `json:"namespace"`
	Marker    string `json:"marker"`
}

func (marker OwnershipMarker) Validate() error {
	for name, value := range map[string]string{"owner": marker.OwnerID, "account": marker.AccountID, "root": marker.RootID, "namespace": marker.Namespace, "marker": marker.Marker} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("ownership %s is required", name)
		}
	}
	return nil
}

type ActionKind string

const (
	ActionNoop                  ActionKind = "noop"
	ActionDriveQuarantineIntent ActionKind = "drive_quarantine_intent"
	ActionQuarantineCopy        ActionKind = "quarantine_copy"
	ActionSourceDelete          ActionKind = "source_delete"
	ActionPurge                 ActionKind = "purge"
)

type DriveQuarantineIntent struct {
	ObjectID     string `json:"object_id"`
	AccountID    string `json:"account_id"`
	RootID       string `json:"root_id"`
	Namespace    string `json:"namespace"`
	ParentID     string `json:"parent_id"`
	ExpectedETag string `json:"expected_etag"`
	Version      string `json:"version"`
	Generation   string `json:"generation"`
	Size         int64  `json:"size"`
	Hash         string `json:"hash"`
	RequestID    string `json:"request_id"`
}

type Action struct {
	ID                 string                 `json:"id"`
	RequestID          string                 `json:"request_id"`
	PlanID             string                 `json:"plan_id"`
	PolicyDigest       string                 `json:"policy_digest"`
	QuarantineTarget   string                 `json:"quarantine_target"`
	Kind               ActionKind             `json:"kind"`
	Provider           Provider               `json:"provider"`
	Object             Object                 `json:"object"`
	Target             Object                 `json:"target,omitempty"`
	Optional           bool                   `json:"optional"`
	DriveIntent        *DriveQuarantineIntent `json:"drive_quarantine_intent,omitempty"`
	R2Copy             r2api.CopyRequest      `json:"r2_copy,omitempty"`
	R2CopyCapability   r2api.CopyCapability   `json:"r2_copy_capability,omitempty"`
	R2Delete           r2api.DeleteRequest    `json:"r2_delete,omitempty"`
	R2DeleteCapability r2api.DeleteCapability `json:"r2_delete_capability,omitempty"`
	R2Purge            r2api.PurgeRequest     `json:"r2_purge,omitempty"`
	R2PurgeCapability  r2api.PurgeCapability  `json:"r2_purge_capability,omitempty"`
}

type Plan struct {
	ID              string          `json:"id"`
	Policy          Policy          `json:"policy"`
	PolicyDigest    string          `json:"policy_digest"`
	InventoryDigest string          `json:"inventory_digest"`
	Owner           OwnershipMarker `json:"owner"`
	Actions         []Action        `json:"actions"`
	CreatedAt       time.Time       `json:"created_at"`
}

func PlanRetention(policy Policy, inventory Inventory, owner OwnershipMarker, now time.Time) (Plan, error) {
	policy = policy.Normalize()
	if policy.ActivatedAt.IsZero() {
		policy.ActivatedAt = now.UTC()
	}
	if err := policy.Validate(now); err != nil {
		return Plan{}, err
	}
	if err := owner.Validate(); err != nil {
		return Plan{}, err
	}
	if err := validateInventoryScope(inventory, owner); err != nil {
		return Plan{}, err
	}
	if !inventory.CapturedAt.IsZero() && inventory.CapturedAt.After(now) {
		return Plan{}, errors.New("inventory timestamp is in the future")
	}
	if inventory.Hash != "" && inventory.Hash != InventoryDigest(inventory) {
		return Plan{}, errors.New("inventory drift detected")
	}
	for _, object := range inventory.Objects {
		if err := validateObjectScope(policy, inventory, owner, object); err != nil {
			return Plan{}, err
		}
		if object.ModifiedAt.IsZero() {
			return Plan{}, fmt.Errorf("object %q modified timestamp is required", object.ObjectID)
		}
		if object.ModifiedAt.After(now) {
			return Plan{}, fmt.Errorf("object %q has future timestamp", object.ObjectID)
		}
		if policy.DeletePolicy == DeletePolicyQuarantine && now.Sub(object.ModifiedAt) < policy.MinimumObjectAge {
			return Plan{}, fmt.Errorf("object %q does not satisfy minimum object age %s", object.ObjectID, policy.MinimumObjectAge)
		}
		if object.Size < 0 || object.Hash == "" || object.ObjectID == "" || object.Version == "" || object.Generation == "" || object.ETag == "" {
			return Plan{}, fmt.Errorf("object %q lacks exact inventory evidence", object.ObjectID)
		}
	}
	if _, err := NewestCompleteRestoreSets(inventory, policy.MinCompleteRestoreSets, policy.Provider); err != nil {
		return Plan{}, err
	}
	if len(inventory.Objects) > policy.MaxObjects {
		return Plan{}, fmt.Errorf("object budget %d exceeded", policy.MaxObjects)
	}
	var bytes int64
	for _, object := range inventory.Objects {
		if object.Size > policy.MaxBytes-bytes {
			return Plan{}, fmt.Errorf("byte budget %d exceeded", policy.MaxBytes)
		}
		bytes += object.Size
	}

	if policy.DeletePolicy == DeletePolicyQuarantine {
		seen := make(map[string]struct{})
		for _, object := range inventory.Objects {
			if object.Provider == string(ProviderR2) {
				if object.Bucket == policy.QuarantineBucket {
					return Plan{}, fmt.Errorf("quarantine overlap for object %q", object.ObjectID)
				}
				key := policy.QuarantinePrefix + object.Key
				destination := policy.QuarantineBucket + "\x00" + key
				if _, exists := seen[destination]; exists {
					return Plan{}, fmt.Errorf("quarantine overlap for object %q", object.ObjectID)
				}
				seen[destination] = struct{}{}
			}
		}
	}

	plan := Plan{Policy: policy, PolicyDigest: PolicyDigest(policy), InventoryDigest: InventoryDigest(inventory), Owner: owner, CreatedAt: now.UTC()}
	plan.ID = digestString(owner.OwnerID + "\x00" + plan.PolicyDigest + "\x00" + plan.InventoryDigest)
	if policy.DeletePolicy == DeletePolicyNone {
		return plan, nil
	}
	objects := append([]Object(nil), inventory.Objects...)
	sort.Slice(objects, func(i, j int) bool { return objectSortKey(objects[i]) < objectSortKey(objects[j]) })
	for _, object := range objects {
		action, err := planAction(policy, owner, object)
		if err != nil {
			return Plan{}, err
		}
		action = bindAction(plan, action)
		plan.Actions = append(plan.Actions, action)
	}
	return plan, nil
}

func planAction(policy Policy, owner OwnershipMarker, object Object) (Action, error) {
	action := Action{ID: digestString(owner.Marker + "\x00" + objectSortKey(object)), Provider: Provider(object.Provider), Object: object, Optional: object.Optional}
	action.RequestID = action.ID
	switch Provider(object.Provider) {
	case ProviderR2:
		source := r2api.ObjectIdentity{AccountID: object.AccountID, Bucket: object.Bucket, Key: object.Key, VersionID: object.Version, ETag: object.ETag}
		destination := r2api.ObjectIdentity{AccountID: object.AccountID, Bucket: policy.QuarantineBucket, Key: policy.QuarantinePrefix + object.Key}
		action.Kind = ActionQuarantineCopy
		action.Target = Object{Provider: string(ProviderR2), AccountID: object.AccountID, Bucket: policy.QuarantineBucket, Key: policy.QuarantinePrefix + object.Key, ObjectID: object.ObjectID, Size: object.Size, Hash: object.Hash}
		action.R2Copy = r2api.CopyRequest{Source: source, Destination: destination, ExpectedSize: object.Size, ExpectedSHA256: object.Hash, RequestID: action.RequestID}
	case ProviderDrive:
		action.Kind = ActionDriveQuarantineIntent
		action.DriveIntent = &DriveQuarantineIntent{
			ObjectID: object.ObjectID, AccountID: object.AccountID, RootID: object.RootID, Namespace: object.Namespace,
			ParentID: object.ParentID, ExpectedETag: object.ETag, Version: object.Version, Generation: object.Generation,
			Size: object.Size, Hash: object.Hash, RequestID: action.RequestID,
		}
	default:
		return Action{}, fmt.Errorf("unsupported retention provider %q", object.Provider)
	}
	return action, nil
}

func bindAction(plan Plan, action Action) Action {
	target := quarantineTarget(plan.Policy, action.Object)
	action.PlanID = plan.ID
	action.PolicyDigest = plan.PolicyDigest
	action.QuarantineTarget = target
	action.ID = digestString(strings.Join([]string{plan.ID, plan.PolicyDigest, target, objectSortKey(action.Object)}, "\x00"))
	action.RequestID = action.ID
	action.R2Copy.RequestID = action.RequestID
	action.R2Delete.RequestID = action.RequestID
	action.R2Purge.RequestID = action.RequestID
	return action
}

func quarantineTarget(policy Policy, object Object) string {
	switch Provider(object.Provider) {
	case ProviderR2:
		return strings.Join([]string{object.AccountID, policy.QuarantineBucket, policy.QuarantinePrefix + object.Key}, "\x00")
	case ProviderDrive:
		return strings.Join([]string{object.AccountID, object.RootID, object.Namespace, policy.QuarantineBucket, policy.QuarantinePrefix, object.ObjectID}, "\x00")
	default:
		return strings.Join([]string{string(policy.Provider), policy.QuarantineBucket, policy.QuarantinePrefix, object.ObjectID}, "\x00")
	}
}

type R2Provider interface {
	Head(context.Context, r2api.ObjectIdentity) (r2api.Object, error)
	Copy(context.Context, r2api.CopyRequest, r2api.CopyCapability) (r2api.CopyReceipt, error)
	Delete(context.Context, r2api.DeleteRequest, r2api.DeleteCapability) (r2api.MutationReceipt, error)
	Purge(context.Context, r2api.PurgeRequest, r2api.PurgeCapability) (r2api.MutationReceipt, error)
}

type LeaseState string

const (
	LeaseClaimed  LeaseState = "claimed"
	LeaseConsumed LeaseState = "consumed"
)

type Lease struct {
	ID    string     `json:"id"`
	Fence string     `json:"fence"`
	State LeaseState `json:"state"`
}

var ErrUnknownSettlement = errors.New("unknown settlement; lease fence retained")

const (
	OutcomeSettled        = "settled"
	OutcomeOptionalFailed = "optional_failed"
	OutcomeUnknown        = "unknown"
)

type JournalRecord struct {
	Sequence         uint64    `json:"sequence"`
	TransactionID    string    `json:"transaction_id"`
	PlanID           string    `json:"plan_id,omitempty"`
	PolicyDigest     string    `json:"policy_digest,omitempty"`
	QuarantineTarget string    `json:"quarantine_target,omitempty"`
	ActionID         string    `json:"action_id"`
	RequestID        string    `json:"request_id"`
	Before           string    `json:"before"`
	After            string    `json:"after"`
	Outcome          string    `json:"outcome"`
	Timestamp        time.Time `json:"timestamp"`
	PreviousHash     string    `json:"previous_hash,omitempty"`
	Hash             string    `json:"hash"`
}

type Journal struct {
	Records    []JournalRecord `json:"records"`
	appendHook func(JournalRecord) error
	mu         sync.Mutex
}

func NewJournal() *Journal { return &Journal{} }

func (journal *Journal) Append(record JournalRecord) error {
	if journal == nil {
		return errors.New("journal is nil")
	}
	if record.TransactionID == "" || record.ActionID == "" || record.RequestID == "" || record.Before == "" || record.After == "" || record.Outcome == "" {
		return errors.New("journal transaction, action, request, and state fields are required")
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	record.Sequence = uint64(len(journal.Records) + 1)
	record.Timestamp = record.Timestamp.UTC()
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Unix(0, 0).UTC()
	}
	if journal.appendHook != nil {
		if err := journal.appendHook(record); err != nil {
			return err
		}
	}
	if len(journal.Records) > 0 {
		record.PreviousHash = journal.Records[len(journal.Records)-1].Hash
	}
	record.Hash = journalHash(record)
	journal.Records = append(journal.Records, record)
	return nil
}

func (journal *Journal) Verify() error {
	if journal == nil {
		return errors.New("journal is nil")
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	var previous string
	for index, record := range journal.Records {
		if record.Sequence != uint64(index+1) || record.PreviousHash != previous || record.Hash != journalHash(record) {
			return fmt.Errorf("journal integrity failure at sequence %d", index+1)
		}
		previous = record.Hash
	}
	return nil
}

func journalHash(record JournalRecord) string {
	copyRecord := record
	copyRecord.Hash = ""
	data, _ := json.Marshal(copyRecord)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type Report struct {
	SettledActions   int      `json:"settled_actions"`
	OptionalFailures []string `json:"optional_failures,omitempty"`
	Lease            Lease    `json:"lease"`
}

type Engine struct {
	R2      R2Provider
	Journal *Journal
	Lease   Lease
	Now     func() time.Time
	blocked map[string]struct{}
}

func NewEngine(r2 R2Provider, journal *Journal) *Engine {
	if journal == nil {
		journal = NewJournal()
	}
	return &Engine{R2: r2, Journal: journal, Now: time.Now, blocked: make(map[string]struct{})}
}

func (engine *Engine) Apply(ctx context.Context, plan Plan, currentPolicy Policy) (Report, error) {
	if engine == nil || engine.Journal == nil {
		return Report{}, errors.New("retention engine is not configured")
	}
	now := time.Now().UTC()
	if engine.Now != nil {
		now = engine.Now().UTC()
	}
	currentPolicy = currentPolicy.Normalize()
	if currentPolicy.ActivatedAt.IsZero() {
		currentPolicy.ActivatedAt = plan.Policy.ActivatedAt
	}
	if err := currentPolicy.Validate(now); err != nil {
		return Report{}, err
	}
	if plan.PolicyDigest != PolicyDigest(currentPolicy) {
		return Report{}, errors.New("policy activation drift detected")
	}
	if err := plan.Owner.Validate(); err != nil {
		return Report{}, err
	}
	if engine.Lease.State == "" {
		engine.Lease = Lease{ID: plan.ID, Fence: digestString(plan.ID + "\x00" + plan.Owner.Marker), State: LeaseClaimed}
	} else if engine.Lease.ID != plan.ID || engine.Lease.Fence == "" {
		return Report{}, errors.New("retention lease fence mismatch")
	}
	if engine.blocked == nil {
		engine.blocked = make(map[string]struct{})
	}
	report := Report{Lease: engine.Lease}
	for _, action := range plan.Actions {
		if _, blocked := engine.blocked[actionJournalKey(plan, action)]; blocked {
			return report, ErrUnknownSettlement
		}
		if record, ok := engine.journalAction(plan, action); ok {
			switch record.Outcome {
			case OutcomeUnknown:
				return report, ErrUnknownSettlement
			case OutcomeSettled, OutcomeOptionalFailed:
				continue
			}
		}
		if err := contextErr(ctx); err != nil {
			return report, err
		}
		if err := engine.applyAction(ctx, action); err != nil {
			if isUnknownSettlement(err) {
				appendErr := engine.Journal.Append(engine.outcomeRecord(plan, action, "unknown", OutcomeUnknown, now))
				if appendErr != nil {
					engine.blocked[actionJournalKey(plan, action)] = struct{}{}
					report.Lease = engine.Lease
					return report, errors.Join(unknownRetentionError(err), appendErr)
				}
				report.Lease = engine.Lease
				return report, unknownRetentionError(err)
			}
			if action.Optional {
				appendErr := engine.Journal.Append(engine.outcomeRecord(plan, action, "optional_failed", OutcomeOptionalFailed, now))
				if appendErr != nil {
					engine.blocked[actionJournalKey(plan, action)] = struct{}{}
					report.Lease = engine.Lease
					return report, errors.Join(err, appendErr)
				}
				report.OptionalFailures = append(report.OptionalFailures, action.ID)
				continue
			}
			return report, err
		}
		if err := engine.Journal.Append(engine.outcomeRecord(plan, action, "settled", OutcomeSettled, now)); err != nil {
			engine.blocked[actionJournalKey(plan, action)] = struct{}{}
			report.Lease = engine.Lease
			return report, errors.Join(ErrUnknownSettlement, err)
		}
		report.SettledActions++
	}
	engine.Lease.State = LeaseConsumed
	report.Lease = engine.Lease
	return report, nil
}

func (engine *Engine) applyAction(ctx context.Context, action Action) error {
	switch action.Kind {
	case ActionNoop:
		return nil
	case ActionDriveQuarantineIntent:
		return errors.New("Drive quarantine execution is unavailable without a protected provider broker")
	case ActionQuarantineCopy:
		if engine.R2 == nil || action.R2CopyCapability.Signature == "" {
			return errors.New("R2 quarantine copy requires exact capability")
		}
		receipt, err := engine.R2.Copy(ctx, action.R2Copy, action.R2CopyCapability)
		if err != nil {
			return err
		}
		if !receipt.ReadbackVerified || receipt.RequestID != action.R2Copy.RequestID ||
			receipt.Source != action.R2Copy.Source || receipt.Destination.AccountID != action.R2Copy.Destination.AccountID ||
			receipt.Destination.Bucket != action.R2Copy.Destination.Bucket || receipt.Destination.Key != action.R2Copy.Destination.Key ||
			receipt.Size != action.R2Copy.ExpectedSize || receipt.SHA256 != action.R2Copy.ExpectedSHA256 {
			return unknownRetentionError(errors.New("R2 quarantine copy requires exact verified checksum readback"))
		}
		return nil
	case ActionSourceDelete:
		if engine.R2 == nil || action.R2DeleteCapability.Signature == "" {
			return errors.New("R2 source delete requires exact capability")
		}
		receipt, err := engine.R2.Delete(ctx, action.R2Delete, action.R2DeleteCapability)
		if err != nil {
			return err
		}
		if receipt.Identity != action.R2Delete.Source || receipt.RequestID != action.R2Delete.RequestID || receipt.State != "deleted" {
			return unknownRetentionError(errors.New("R2 source delete readback does not match exact request"))
		}
		return nil
	case ActionPurge:
		if engine.R2 == nil || action.R2PurgeCapability.Signature == "" {
			return errors.New("R2 purge requires exact capability")
		}
		if action.R2Purge.Lifecycle != "" {
			return errors.New("R2 lifecycle must be empty before explicit purge")
		}
		receipt, err := engine.R2.Purge(ctx, action.R2Purge, action.R2PurgeCapability)
		if err != nil {
			return err
		}
		if receipt.Identity != action.R2Purge.Object || receipt.RequestID != action.R2Purge.RequestID || receipt.State != "purged" {
			return unknownRetentionError(errors.New("R2 purge readback does not match exact request"))
		}
		return nil
	default:
		return fmt.Errorf("unsupported retention action %q", action.Kind)
	}
}

func unknownRetentionError(cause error) error {
	if cause == nil {
		cause = errors.New("mutation settlement could not be verified")
	}
	return fmt.Errorf("retention outcome unknown: %w", errors.Join(ErrUnknownSettlement, cause))
}

func isUnknownSettlement(err error) bool {
	return errors.Is(err, ErrUnknownSettlement) || errors.Is(err, r2api.ErrUnknownSettlement)
}

func (engine *Engine) outcomeRecord(plan Plan, action Action, after, outcome string, now time.Time) JournalRecord {
	return JournalRecord{
		TransactionID: plan.ID, PlanID: action.PlanID, PolicyDigest: action.PolicyDigest, QuarantineTarget: action.QuarantineTarget,
		ActionID: action.ID, RequestID: action.RequestID, Before: "claimed", After: after, Outcome: outcome, Timestamp: now,
	}
}

func actionJournalKey(plan Plan, action Action) string {
	return strings.Join([]string{plan.ID, action.PlanID, action.PolicyDigest, action.QuarantineTarget, action.ID, action.RequestID}, "\x00")
}

func (engine *Engine) journalAction(plan Plan, action Action) (JournalRecord, bool) {
	engine.Journal.mu.Lock()
	defer engine.Journal.mu.Unlock()
	for index := len(engine.Journal.Records) - 1; index >= 0; index-- {
		record := engine.Journal.Records[index]
		if record.TransactionID == plan.ID && record.PlanID == action.PlanID && record.PolicyDigest == action.PolicyDigest &&
			record.QuarantineTarget == action.QuarantineTarget && record.ActionID == action.ID && record.RequestID == action.RequestID {
			return record, true
		}
	}
	return JournalRecord{}, false
}

func objectSortKey(object Object) string {
	return strings.Join([]string{object.Provider, object.AccountID, object.RootID, object.Namespace, object.Bucket, object.Key, object.ObjectID, object.Version, object.Generation, object.ETag}, "\x00")
}

func digestString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
