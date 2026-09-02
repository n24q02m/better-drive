package restore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CheckpointKind distinguishes immutable checkpoint authorizations.
type CheckpointKind string

const (
	CheckpointKindCreate  CheckpointKind = "create"
	CheckpointKindReplace CheckpointKind = "replace"
)

// MachineCheckpoint is an externally issued, signed authorization for one explicit restore batch.
// Restore cannot mint it; it can only verify.
type MachineCheckpoint struct {
	ID           string         `json:"id"`
	Kind         CheckpointKind `json:"kind"`
	Root         string         `json:"root"`
	RootIdentity RootIdentity   `json:"root_identity"`
	Entries      []string       `json:"entries"` // canonical relative paths, sorted
	CapacityBytes int64         `json:"capacity_bytes"`
	ExpiresAt    time.Time      `json:"expires_at"`
	Signature    string         `json:"signature"`
	// IssuedAt is the checkpoint issuance time for TTL/expiry reasoning.
	IssuedAt time.Time `json:"issued_at"`
}

// ApplyIntent is the exact restore intent that a checkpoint must authorize.
type ApplyIntent struct {
	Plan          Plan   `json:"plan"`
	CapacityBytes int64  `json:"capacity_bytes"`
	TotalObjects  int    `json:"total_objects"`
	// For replace, CurrentIdentities binds existing destination state; for create it is empty.
	CurrentIdentities map[string]DestinationIdentity `json:"current_identities,omitempty"`
}

// DestinationIdentity binds the observable destination state that a replace checkpoint must authorize.
type DestinationIdentity struct {
	Exists          bool   `json:"exists"`
	PlaintextDigest string `json:"plaintext_digest,omitempty"`
	FileIdentity    string `json:"file_identity,omitempty"`
	ModTimeUnix     int64  `json:"mod_time_unix,omitempty"`
	ProviderVersion string `json:"provider_version,omitempty"`
}

// CheckpointVerifier verifies an externally issued checkpoint against the exact intent.
// Implementations must verify detached signature, expiry, root identity, entry set, budgets, and current destination binding.
type CheckpointVerifier interface {
	Verify(ctx context.Context, checkpoint MachineCheckpoint, intent ApplyIntent) error
}

func (c MachineCheckpoint) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("checkpoint id is required")
	}
	if c.Kind != CheckpointKindCreate && c.Kind != CheckpointKindReplace {
		return fmt.Errorf("checkpoint kind must be create or replace")
	}
	if strings.TrimSpace(c.Root) == "" {
		return fmt.Errorf("checkpoint root is required")
	}
	if err := validateRootIdentityFields(c.RootIdentity); err != nil {
		return fmt.Errorf("checkpoint root identity: %w", err)
	}
	if len(c.Entries) == 0 {
		return fmt.Errorf("checkpoint entries is required")
	}
	if c.CapacityBytes < 0 {
		return fmt.Errorf("checkpoint capacity must be non-negative")
	}
	if c.ExpiresAt.IsZero() {
		return fmt.Errorf("checkpoint expiry is required")
	}
	if time.Now().After(c.ExpiresAt) {
		return fmt.Errorf("checkpoint expired")
	}
	if strings.TrimSpace(c.Signature) == "" {
		return fmt.Errorf("checkpoint signature is required")
	}
	// Ensure entries are canonical and sorted/deduped.
	seen := make(map[string]struct{}, len(c.Entries))
	prev := ""
	for i, entry := range c.Entries {
		clean, err := cleanRelativePath(entry)
		if err != nil {
			return fmt.Errorf("checkpoint entry %d: %w", i, err)
		}
		if clean != entry {
			return fmt.Errorf("checkpoint entry %d is not canonical", i)
		}
		if _, exists := seen[clean]; exists {
			return fmt.Errorf("checkpoint duplicate entry %q", clean)
		}
		seen[clean] = struct{}{}
		if prev != "" && clean < prev {
			return fmt.Errorf("checkpoint entries must be sorted")
		}
		prev = clean
	}
	return nil
}

// VerifyCheckpoint performs local structural checks before delegating to an injected verifier.
// It never mints trust; it only validates shape and expiry.
func VerifyCheckpoint(ctx context.Context, checkpoint MachineCheckpoint, intent ApplyIntent, verifier CheckpointVerifier) error {
	if verifier == nil {
		return fmt.Errorf("checkpoint verifier is required for restore apply")
	}
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	if err := intent.Plan.RootIdentity.Validate(intent.Plan.Root); err != nil {
		return fmt.Errorf("apply intent root identity: %w", err)
	}
	if checkpoint.Root != intent.Plan.Root {
		return fmt.Errorf("checkpoint root mismatch")
	}
	if checkpoint.RootIdentity != intent.Plan.RootIdentity {
		return fmt.Errorf("checkpoint root identity mismatch")
	}
	if checkpoint.CapacityBytes < intent.CapacityBytes {
		return fmt.Errorf("checkpoint capacity %d is insufficient for required %d", checkpoint.CapacityBytes, intent.CapacityBytes)
	}
	// Verify entry set binding exactly.
	if len(checkpoint.Entries) != len(intent.Plan.Entries) {
		return fmt.Errorf("checkpoint entry count mismatch")
	}
	planEntries := make(map[string]struct{}, len(intent.Plan.Entries))
	for _, e := range intent.Plan.Entries {
		planEntries[e.RelativePath] = struct{}{}
	}
	for _, entry := range checkpoint.Entries {
		if _, ok := planEntries[entry]; !ok {
			return fmt.Errorf("checkpoint entry %q not in plan", entry)
		}
	}
	if err := verifier.Verify(ctx, checkpoint, intent); err != nil {
		return fmt.Errorf("checkpoint verification failed: %w", err)
	}
	return nil
}
