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

type canonicalDestinationIdentity struct {
	provider  string
	accountID string
	rootID    string
	namespace string
}

func (d DestinationIdentity) canonical() (canonicalDestinationIdentity, error) {
	for name, value := range map[string]string{
		"provider": d.Provider, "account": d.AccountID, "root": d.RootID, "namespace": d.Namespace,
	} {
		if strings.ContainsRune(value, '\x00') {
			return canonicalDestinationIdentity{}, fmt.Errorf("destination %s must not contain NUL", name)
		}
	}
	identity := canonicalDestinationIdentity{
		provider:  strings.ToLower(strings.TrimSpace(d.Provider)),
		accountID: strings.ToLower(strings.TrimSpace(d.AccountID)),
		rootID:    strings.ToLower(strings.TrimSpace(d.RootID)),
		namespace: strings.ToLower(strings.Trim(strings.ReplaceAll(strings.TrimSpace(d.Namespace), "\\", "/"), "/")),
	}
	if identity.provider == "" || identity.accountID == "" || identity.rootID == "" || identity.namespace == "" {
		return canonicalDestinationIdentity{}, fmt.Errorf("destination identity requires provider, account, root, and namespace")
	}
	if filepath.IsAbs(identity.namespace) || identity.namespace == "." ||
		strings.HasPrefix(identity.namespace, "../") || strings.Contains(identity.namespace, "/../") {
		return canonicalDestinationIdentity{}, fmt.Errorf("destination namespace must stay relative")
	}
	return identity, nil
}

func (d canonicalDestinationIdentity) sameRoot(other canonicalDestinationIdentity) bool {
	return d.provider == other.provider && d.accountID == other.accountID && d.rootID == other.rootID
}

func namespacesOverlap(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func ValidateDestinationCollisions(destinations []DestinationIdentity) error {
	canonical := make([]canonicalDestinationIdentity, len(destinations))
	for i, destination := range destinations {
		value, err := destination.canonical()
		if err != nil {
			return fmt.Errorf("destination %d: %w", i, err)
		}
		canonical[i] = value
	}
	for i := range canonical {
		for j := i + 1; j < len(canonical); j++ {
			left, right := canonical[i], canonical[j]
			if !left.sameRoot(right) {
				continue
			}
			if left.namespace == right.namespace {
				return fmt.Errorf("destination identities collide exactly: %q", left.namespace)
			}
			if namespacesOverlap(left.namespace, right.namespace) {
				return fmt.Errorf("destination identities collide by ancestor overlap: %q and %q", left.namespace, right.namespace)
			}
		}
	}
	return nil
}

func ValidateQuarantineIdentity(transfer, quarantine DestinationIdentity) error {
	transferKey, err := transfer.canonical()
	if err != nil {
		return fmt.Errorf("transfer identity: %w", err)
	}
	quarantineKey, err := quarantine.canonical()
	if err != nil {
		return fmt.Errorf("quarantine identity: %w", err)
	}
	if transferKey.sameRoot(quarantineKey) && namespacesOverlap(transferKey.namespace, quarantineKey.namespace) {
		return fmt.Errorf("quarantine identity overlaps transfer namespace")
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
