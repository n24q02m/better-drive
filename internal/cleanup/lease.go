package cleanup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	LeaseApproved            = "approved"
	LeaseClaimed             = "claimed"
	LeaseConsumed            = "consumed"
	LeaseNeedsReconciliation = "needs_reconciliation"
)

type Lease struct {
	ID             string `json:"id"`
	ManifestDigest string `json:"manifest_digest"`
	ExecutionID    string `json:"execution_id,omitempty"`
	State          string `json:"state"`
	Generation     uint64 `json:"generation"`
}

func ClaimLease(lease Lease, executionID string) (Lease, error) {
	if lease.State != LeaseApproved {
		return Lease{}, fmt.Errorf("lease cannot be claimed from state %q", lease.State)
	}
	if lease.ID == "" || lease.ManifestDigest == "" || executionID == "" {
		return Lease{}, errors.New("lease ID, manifest digest, and execution ID are required")
	}
	lease.State = LeaseClaimed
	lease.ExecutionID = executionID
	lease.Generation++
	return lease, nil
}

func ConsumeLease(lease Lease, executionID string) (Lease, error) {
	if lease.State != LeaseClaimed {
		return Lease{}, fmt.Errorf("lease cannot be consumed from state %q", lease.State)
	}
	if lease.ExecutionID != executionID || executionID == "" {
		return Lease{}, errors.New("lease execution fence mismatch")
	}
	lease.State = LeaseConsumed
	lease.Generation++
	return lease, nil
}

func ReconcileLease(lease Lease, executionID string) (Lease, error) {
	if lease.State != LeaseClaimed && lease.State != LeaseNeedsReconciliation {
		return Lease{}, fmt.Errorf("lease cannot be reconciled from state %q", lease.State)
	}
	if lease.ExecutionID != executionID || executionID == "" {
		return Lease{}, errors.New("lease execution fence mismatch")
	}
	lease.State = LeaseNeedsReconciliation
	lease.Generation++
	return lease, nil
}

type JournalRecord struct {
	Sequence  uint64    `json:"sequence"`
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`
	ObjectID  string    `json:"object_id"`
	Before    string    `json:"before"`
	After     string    `json:"after"`
	RequestID string    `json:"request_id,omitempty"`
	Previous  string    `json:"previous_hash,omitempty"`
	Hash      string    `json:"hash"`
}

type Journal struct {
	Records []JournalRecord `json:"records"`
}

func NewJournal() *Journal { return &Journal{} }

func (journal *Journal) Append(record JournalRecord) error {
	if journal == nil {
		return errors.New("journal is nil")
	}
	if record.Action == "" || record.ObjectID == "" || record.Before == "" || record.After == "" {
		return errors.New("journal action, object ID, and before/after state are required")
	}
	record.Sequence = uint64(len(journal.Records) + 1)
	record.Timestamp = record.Timestamp.UTC()
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Unix(0, 0).UTC()
	}
	if len(journal.Records) > 0 {
		record.Previous = journal.Records[len(journal.Records)-1].Hash
	}
	record.Hash = journalRecordHash(record)
	journal.Records = append(journal.Records, record)
	return nil
}

func (journal *Journal) Verify() error {
	if journal == nil {
		return errors.New("journal is nil")
	}
	var previous string
	for index, record := range journal.Records {
		if record.Sequence != uint64(index+1) {
			return fmt.Errorf("journal sequence gap at %d", index+1)
		}
		if record.Previous != previous {
			return fmt.Errorf("journal previous hash mismatch at sequence %d", record.Sequence)
		}
		if record.Hash != journalRecordHash(record) {
			return fmt.Errorf("journal hash mismatch at sequence %d", record.Sequence)
		}
		previous = record.Hash
	}
	return nil
}

func journalRecordHash(record JournalRecord) string {
	copyRecord := record
	copyRecord.Hash = ""
	canonical, _ := json.Marshal(copyRecord)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
