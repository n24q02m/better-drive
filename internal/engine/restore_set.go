package engine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type RestoreComponent struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type RestoreReplicaAck struct {
	ReplicaID        string `json:"replica_id"`
	Required         bool   `json:"required"`
	Complete         bool   `json:"complete"`
	CiphertextDigest string `json:"ciphertext_digest,omitempty"`
}

type RestoreSetRecord struct {
	RestoreSetID string              `json:"restore_set_id"`
	Components   []RestoreComponent  `json:"components"`
	ReplicaAcks  []RestoreReplicaAck `json:"replica_acks"`
}

func (r RestoreSetRecord) Validate() error {
	if strings.TrimSpace(r.RestoreSetID) == "" {
		return fmt.Errorf("restore_set_id is required")
	}
	if len(r.Components) == 0 {
		return fmt.Errorf("restore set %q requires components", r.RestoreSetID)
	}
	componentIDs := make(map[string]struct{}, len(r.Components))
	for _, component := range r.Components {
		if strings.TrimSpace(component.Name) == "" || strings.TrimSpace(component.Digest) == "" {
			return fmt.Errorf("restore set %q has incomplete component", r.RestoreSetID)
		}
		if _, exists := componentIDs[component.Name]; exists {
			return fmt.Errorf("restore set %q has duplicate component %q", r.RestoreSetID, component.Name)
		}
		componentIDs[component.Name] = struct{}{}
	}
	if len(r.ReplicaAcks) == 0 {
		return fmt.Errorf("restore set %q requires replica acknowledgements", r.RestoreSetID)
	}
	replicaIDs := make(map[string]struct{}, len(r.ReplicaAcks))
	for _, ack := range r.ReplicaAcks {
		if strings.TrimSpace(ack.ReplicaID) == "" {
			return fmt.Errorf("restore set %q has acknowledgement without replica_id", r.RestoreSetID)
		}
		if _, exists := replicaIDs[ack.ReplicaID]; exists {
			return fmt.Errorf("restore set %q has duplicate replica acknowledgement %q", r.RestoreSetID, ack.ReplicaID)
		}
		replicaIDs[ack.ReplicaID] = struct{}{}
		if ack.Required && !ack.Complete {
			return fmt.Errorf("restore set %q required replica %q is incomplete", r.RestoreSetID, ack.ReplicaID)
		}
		if ack.Complete && strings.TrimSpace(ack.CiphertextDigest) == "" {
			return fmt.Errorf("restore set %q complete replica %q lacks ciphertext_digest", r.RestoreSetID, ack.ReplicaID)
		}
	}
	return nil
}

func AppendRestoreSetRecord(path string, record RestoreSetRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open restore journal: %w", err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(record); err != nil {
		return fmt.Errorf("append restore journal: %w", err)
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
		return nil, fmt.Errorf("read restore journal: %w", err)
	}
	return records, nil
}
