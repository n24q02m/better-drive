package engine

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
)

type RestoreComponent struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type RestoreReplica struct {
	ReplicaID string `json:"replica_id"`
	Required  bool   `json:"required"`
}

type RestoreReplicaAck struct {
	ReplicaID        string             `json:"replica_id"`
	Required         bool               `json:"required"`
	Complete         bool               `json:"complete"`
	ProviderVersion  string             `json:"provider_version,omitempty"`
	ETag             string             `json:"etag,omitempty"`
	CiphertextDigest string             `json:"ciphertext_digest,omitempty"`
	VerifiedReadback bool               `json:"verified_readback"`
	Components       []RestoreComponent `json:"components"`
}

type RestoreSetRecord struct {
	RestoreSetID     string              `json:"restore_set_id"`
	Components       []RestoreComponent  `json:"components"`
	ExpectedReplicas []RestoreReplica    `json:"expected_replicas"`
	ReplicaAcks      []RestoreReplicaAck `json:"replica_acks"`
}

// ValidateRestoreSetRecord validates the durable restore-set contract before
// it is persisted or counted toward a retention floor.
func ValidateRestoreSetRecord(record RestoreSetRecord) error {
	return record.Validate()
}

func (r RestoreSetRecord) Validate() error {
	if strings.TrimSpace(r.RestoreSetID) == "" {
		return fmt.Errorf("restore_set_id is required")
	}
	if len(r.Components) == 0 {
		return fmt.Errorf("restore set %q requires components", r.RestoreSetID)
	}
	if err := validateComponents(r.RestoreSetID, "expected", r.Components); err != nil {
		return err
	}
	if len(r.ExpectedReplicas) == 0 {
		return fmt.Errorf("restore set %q requires expected replicas", r.RestoreSetID)
	}
	expected := make(map[string]bool, len(r.ExpectedReplicas))
	for _, replica := range r.ExpectedReplicas {
		if strings.TrimSpace(replica.ReplicaID) == "" {
			return fmt.Errorf("restore set %q has expected replica without replica_id", r.RestoreSetID)
		}
		if _, exists := expected[replica.ReplicaID]; exists {
			return fmt.Errorf("restore set %q has duplicate expected replica %q", r.RestoreSetID, replica.ReplicaID)
		}
		expected[replica.ReplicaID] = replica.Required
	}
	if len(r.ReplicaAcks) == 0 {
		return fmt.Errorf("restore set %q requires replica acknowledgements", r.RestoreSetID)
	}
	seenAcks := make(map[string]struct{}, len(r.ReplicaAcks))
	for _, ack := range r.ReplicaAcks {
		if strings.TrimSpace(ack.ReplicaID) == "" {
			return fmt.Errorf("restore set %q has acknowledgement without replica_id", r.RestoreSetID)
		}
		if _, exists := seenAcks[ack.ReplicaID]; exists {
			return fmt.Errorf("restore set %q has duplicate replica acknowledgement %q", r.RestoreSetID, ack.ReplicaID)
		}
		seenAcks[ack.ReplicaID] = struct{}{}
		required, known := expected[ack.ReplicaID]
		if !known {
			return fmt.Errorf("restore set %q has unknown replica %q", r.RestoreSetID, ack.ReplicaID)
		}
		if required != ack.Required {
			return fmt.Errorf("restore set %q replica %q required bit drift", r.RestoreSetID, ack.ReplicaID)
		}
		if err := validateComponents(r.RestoreSetID, "replica "+ack.ReplicaID, ack.Components); err != nil {
			return err
		}
		if err := compareComponents(r.RestoreSetID, ack.ReplicaID, r.Components, ack.Components); err != nil {
			return err
		}
		if required && !ack.Complete {
			return fmt.Errorf("restore set %q required replica %q is incomplete", r.RestoreSetID, ack.ReplicaID)
		}
		if ack.Complete {
			if strings.TrimSpace(ack.ProviderVersion) == "" {
				return fmt.Errorf("restore set %q complete replica %q lacks provider version", r.RestoreSetID, ack.ReplicaID)
			}
			if strings.TrimSpace(ack.ETag) == "" {
				return fmt.Errorf("restore set %q complete replica %q lacks ETag", r.RestoreSetID, ack.ReplicaID)
			}
			if strings.TrimSpace(ack.CiphertextDigest) == "" {
				return fmt.Errorf("restore set %q complete replica %q lacks ciphertext_digest", r.RestoreSetID, ack.ReplicaID)
			}
			if !ack.VerifiedReadback {
				return fmt.Errorf("restore set %q complete replica %q lacks verified readback", r.RestoreSetID, ack.ReplicaID)
			}
		}
	}
	if len(seenAcks) != len(expected) {
		for replicaID := range expected {
			if _, exists := seenAcks[replicaID]; !exists {
				return fmt.Errorf("restore set %q is missing acknowledgement for replica %q", r.RestoreSetID, replicaID)
			}
		}
	}
	return nil
}

func validateComponents(restoreSetID, owner string, components []RestoreComponent) error {
	if len(components) == 0 {
		return fmt.Errorf("restore set %q %s requires components", restoreSetID, owner)
	}
	seen := make(map[string]struct{}, len(components))
	for _, component := range components {
		if strings.TrimSpace(component.Name) == "" || strings.TrimSpace(component.Digest) == "" {
			return fmt.Errorf("restore set %q %s has incomplete component", restoreSetID, owner)
		}
		if _, exists := seen[component.Name]; exists {
			return fmt.Errorf("restore set %q %s has duplicate component %q", restoreSetID, owner, component.Name)
		}
		seen[component.Name] = struct{}{}
	}
	return nil
}

func compareComponents(restoreSetID, replicaID string, expected, actual []RestoreComponent) error {
	if len(expected) != len(actual) {
		return fmt.Errorf("restore set %q replica %q component set is incomplete", restoreSetID, replicaID)
	}
	expectedByName := make(map[string]string, len(expected))
	for _, component := range expected {
		expectedByName[component.Name] = component.Digest
	}
	for _, component := range actual {
		digest, known := expectedByName[component.Name]
		if !known {
			return fmt.Errorf("restore set %q replica %q has unknown component %q", restoreSetID, replicaID, component.Name)
		}
		if digest != component.Digest {
			return fmt.Errorf("restore set %q replica %q component %q digest drift", restoreSetID, replicaID, component.Name)
		}
	}
	return nil
}

func AppendRestoreSetRecord(path string, record RestoreSetRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	existing, err := ReadRestoreSetRecords(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, prior := range existing {
		if prior.RestoreSetID == record.RestoreSetID {
			return fmt.Errorf("restore set %q replay rejected", record.RestoreSetID)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open restore journal: %w", err)
	}
	payload, err := json.Marshal(record)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("encode restore journal: %w", err)
	}
	payload = append(payload, '\n')
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("append restore journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync restore journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close restore journal: %w", err)
	}
	readback, err := ReadRestoreSetRecords(path)
	if err != nil {
		return fmt.Errorf("read back restore journal: %w", err)
	}
	if len(readback) != len(existing)+1 || !reflect.DeepEqual(readback[len(readback)-1], record) {
		return errors.New("restore journal append readback mismatch")
	}
	return nil
}

func ReadRestoreSetRecords(path string) ([]RestoreSetRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open restore journal: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var records []RestoreSetRecord
	line := 0
	for scanner.Scan() {
		line++
		var record RestoreSetRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("restore journal line %d: %w", line, err)
		}
		if err := record.Validate(); err != nil {
			return nil, fmt.Errorf("restore journal line %d: %w", line, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		if !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read restore journal: %w", err)
		}
	}
	return records, nil
}
