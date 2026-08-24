package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type DeleteBudget struct {
	MaxObjects int64
	MaxBytes   int64
	Objects    int64
	Bytes      int64
}

func (b DeleteBudget) Validate() error {
	if b.MaxObjects < 0 || b.MaxBytes < 0 || b.Objects < 0 || b.Bytes < 0 {
		return fmt.Errorf("delete budget values must be non-negative")
	}
	if b.Objects > b.MaxObjects {
		return fmt.Errorf("delete budget objects exceeded: %d > %d", b.Objects, b.MaxObjects)
	}
	if b.Bytes > b.MaxBytes {
		return fmt.Errorf("delete budget bytes exceeded: %d > %d", b.Bytes, b.MaxBytes)
	}
	return nil
}

func validateTransferSafety(mode string, sourceWasNonEmpty *bool, sourceObjectCount *int64, budget *DeleteBudget) error {
	if budget != nil {
		if err := budget.Validate(); err != nil {
			return err
		}
	}
	if sourceWasNonEmpty == nil && sourceObjectCount == nil {
		return nil
	}
	if sourceWasNonEmpty == nil || sourceObjectCount == nil {
		return fmt.Errorf("source safety evidence requires history and object count")
	}
	return ValidateSourceForDestructiveMode(mode, *sourceWasNonEmpty, *sourceObjectCount)
}

type OwnershipMarker struct {
	JobID          string
	SourceIdentity string
}

func ValidateOwnershipMarker(expected, actual OwnershipMarker) error {
	if strings.TrimSpace(expected.JobID) == "" || strings.TrimSpace(expected.SourceIdentity) == "" {
		return fmt.Errorf("expected ownership marker is incomplete")
	}
	if expected.JobID != actual.JobID {
		return fmt.Errorf("ownership marker job mismatch: got %q, want %q", actual.JobID, expected.JobID)
	}
	if expected.SourceIdentity != actual.SourceIdentity {
		return fmt.Errorf("ownership marker source mismatch: got %q, want %q", actual.SourceIdentity, expected.SourceIdentity)
	}
	return nil
}

type DestinationIdentity struct {
	Provider  string
	AccountID string
	RootID    string
	Namespace string
}

type canonicalDestination struct {
	Provider  string
	AccountID string
	RootID    string
	Namespace string
}

func (d DestinationIdentity) canonical() (canonicalDestination, error) {
	provider := strings.ToLower(strings.TrimSpace(d.Provider))
	account := strings.ToLower(strings.TrimSpace(d.AccountID))
	root := strings.ToLower(strings.TrimSpace(d.RootID))
	namespace := strings.Trim(strings.ReplaceAll(strings.TrimSpace(d.Namespace), "\\", "/"), "/")
	namespace = strings.ToLower(namespace)
	if provider == "" || account == "" || root == "" || namespace == "" {
		return canonicalDestination{}, fmt.Errorf("destination identity requires provider, account, root, and namespace")
	}
	if filepath.IsAbs(namespace) || namespace == "." || strings.HasPrefix(namespace, "../") || strings.Contains(namespace, "/../") {
		return canonicalDestination{}, fmt.Errorf("destination namespace must stay relative")
	}
	return canonicalDestination{
		Provider:  provider,
		AccountID: account,
		RootID:    root,
		Namespace: namespace,
	}, nil
}

func ValidateDestinationCollisions(destinations []DestinationIdentity) error {
	canonical := make([]canonicalDestination, len(destinations))
	for i, destination := range destinations {
		value, err := destination.canonical()
		if err != nil {
			return fmt.Errorf("destination %d: %w", i, err)
		}
		canonical[i] = value
	}
	for i := range canonical {
		for j := i + 1; j < len(canonical); j++ {
			left := canonical[i]
			right := canonical[j]
			if left.Provider != right.Provider || left.AccountID != right.AccountID || left.RootID != right.RootID {
				continue
			}
			if left.Namespace == right.Namespace {
				return fmt.Errorf("destination identities collide exactly: %q", left.Namespace)
			}
			if strings.HasPrefix(left.Namespace, right.Namespace+"/") || strings.HasPrefix(right.Namespace, left.Namespace+"/") {
				return fmt.Errorf("destination identities collide by ancestor overlap: %q and %q", left.Namespace, right.Namespace)
			}
		}
	}
	return nil
}

func ValidateQuarantineIdentity(transfer, quarantine DestinationIdentity) error {
	left, err := transfer.canonical()
	if err != nil {
		return fmt.Errorf("transfer identity: %w", err)
	}
	right, err := quarantine.canonical()
	if err != nil {
		return fmt.Errorf("quarantine identity: %w", err)
	}
	if left.Provider == right.Provider && left.AccountID == right.AccountID && left.RootID == right.RootID {
		if left.Namespace == right.Namespace || strings.HasPrefix(left.Namespace, right.Namespace+"/") || strings.HasPrefix(right.Namespace, left.Namespace+"/") {
			return fmt.Errorf("quarantine identity overlaps transfer namespace")
		}
	}
	return nil
}

func ValidateSourceForDestructiveMode(mode string, previouslyNonEmpty bool, currentObjects int64) error {
	if mode != "sync" && mode != "bisync" {
		if mode == "copy" {
			return nil
		}
		return fmt.Errorf("unknown destructive mode %q", mode)
	}
	if currentObjects < 0 {
		return fmt.Errorf("source object count must be non-negative")
	}
	if previouslyNonEmpty && currentObjects == 0 {
		return fmt.Errorf("destructive %s blocked: previously-nonempty source is empty", mode)
	}
	return nil
}

func ValidateSymlinkPolicy(policy string, scheduled bool) error {
	if policy != "preserve" && policy != "follow" && policy != "skip" {
		return fmt.Errorf("symlink_policy must be one of preserve|follow|skip, got %q", policy)
	}
	if scheduled && policy == "follow" {
		return fmt.Errorf("symlink_policy=follow is forbidden for scheduled jobs")
	}
	return nil
}

type DestinationLock struct {
	path string
	file *os.File
}

func AcquireDestinationLock(path string) (*DestinationLock, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("destination lock path is required")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("acquire destination lock: %w", err)
	}
	return &DestinationLock{path: path, file: file}, nil
}

func (l *DestinationLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	closeErr := l.file.Close()
	removeErr := os.Remove(l.path)
	l.file = nil
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}
