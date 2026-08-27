package cleanup

import (
	"bytes"
	"crypto/ed25519"
	"io"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type ApprovalStore struct {
	Root string
}

func NewApprovalStore(root string) *ApprovalStore { return &ApprovalStore{Root: root} }

type draftRecord struct {
	SchemaVersion int      `json:"schema_version"`
	DraftDigest   string   `json:"draft_digest"`
	Approval      Approval `json:"approval"`
}

func (store *ApprovalStore) Prepare(approval Approval) (draftRecord, error) {
	if store == nil || store.Root == "" {
		return draftRecord{}, errors.New("approval store root is required")
	}
	canonical, err := CanonicalApproval(approval)
	if err != nil {
		return draftRecord{}, err
	}
	record := draftRecord{SchemaVersion: CurrentApprovalSchemaVersion, DraftDigest: Digest(canonical), Approval: approval}
	path := filepath.Join(store.Root, "cleanup-drafts", approval.ApprovalID+".json")
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return draftRecord{}, err
	}
	if existing, readErr := os.ReadFile(path); readErr == nil {
		var current draftRecord
		if err := json.Unmarshal(existing, &current); err != nil {
			return draftRecord{}, fmt.Errorf("decode existing draft: %w", err)
		}
		currentData, err := json.MarshalIndent(current, "", "  ")
		if err != nil {
			return draftRecord{}, err
		}
		if !bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(currentData)) || current.DraftDigest != record.DraftDigest {
			return draftRecord{}, errors.New("foreign draft exists for approval ID")
		}
		return current, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return draftRecord{}, readErr
	}
	if err := writeCreateOnly(path, append(data, '\n')); err != nil {
		return draftRecord{}, err
	}
	return record, nil
}

func (store *ApprovalStore) Activate(intent Intent) error {
	if store == nil || store.Root == "" {
		return errors.New("approval store root is required")
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
	draftPath := filepath.Join(store.Root, "cleanup-drafts", intent.Approval.ApprovalID+".json")
	if _, err := os.Stat(draftPath); err != nil {
		return fmt.Errorf("draft must exist before activation: %w", err)
	}
	intentPath := filepath.Join(store.Root, "cleanup-intents", intent.Approval.ApprovalID+".json")
	statePath := filepath.Join(store.Root, "cleanup-states", intent.Approval.ApprovalID+".json")
	intentData, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return err
	}
	stateData, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return err
	}
	if existingIntent, existingState := readFile(intentPath), readFile(statePath); existingIntent != nil || existingState != nil {
		if existingIntent == nil || existingState == nil || !bytes.Equal(bytes.TrimSpace(existingIntent), bytes.TrimSpace(intentData)) || !bytes.Equal(bytes.TrimSpace(existingState), bytes.TrimSpace(stateData)) {
			return errors.New("foreign sealed intent or state exists")
		}
		return nil
	}
	if err := writeCreateOnly(intentPath, append(intentData, '\n')); err != nil {
		return err
	}
	if err := writeCreateOnly(statePath, append(stateData, '\n')); err != nil {
		return fmt.Errorf("sealed intent created but state activation failed: %w", err)
	}
	return nil
}

func (store *ApprovalStore) ReadState(approvalID string) (Intent, error) {
	if store == nil || store.Root == "" || approvalID == "" {
		return Intent{}, errors.New("approval store root and approval ID are required")
	}
	root, err := os.OpenRoot(filepath.Join(store.Root, "cleanup-states"))
	if err != nil {
		return Intent{}, err
	}
	defer root.Close()

	f, err := root.Open(approvalID+".json")
	if err != nil {
		return Intent{}, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return Intent{}, err
	}
	var intent Intent
	if err := json.Unmarshal(data, &intent); err != nil {
		return Intent{}, fmt.Errorf("decode approval state: %w", err)
	}
	return intent, nil
}

func writeCreateOnly(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func readFile(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}
