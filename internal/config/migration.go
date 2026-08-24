package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type MigrationBindings struct {
	AccountID    string
	RootID       string
	RoleRef      string
	RoleDigest   string
	PolicyRef    string
	PolicyDigest string
}

func (b MigrationBindings) Validate() error {
	for name, value := range map[string]string{
		"account_id": b.AccountID, "root_id": b.RootID, "role_ref": b.RoleRef, "policy_ref": b.PolicyRef,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("complete migration bindings require %s", name)
		}
	}
	if !isSHA256Digest(b.RoleDigest) {
		return errors.New("complete migration bindings require role_digest sha256:<64 hex chars>")
	}
	if !isSHA256Digest(b.PolicyDigest) {
		return errors.New("complete migration bindings require policy_digest sha256:<64 hex chars>")
	}
	return nil
}

// ApplyMigrationBindings returns a new config with only caller-supplied
// identities applied. Existing identities must agree; no ID or secret is
// generated for a legacy target.
func ApplyMigrationBindings(source *Config, bindings MigrationBindings) (*Config, error) {
	if source == nil {
		return nil, errors.New("migration config is nil")
	}
	if err := bindings.Validate(); err != nil {
		return nil, err
	}
	if source.RoleBinding != (RoleBinding{}) && source.RoleBinding != (RoleBinding{RoleRef: bindings.RoleRef, RoleDigest: bindings.RoleDigest, PolicyRef: bindings.PolicyRef, PolicyDigest: bindings.PolicyDigest}) {
		return nil, errors.New("migration role/policy binding drift")
	}
	clone := *source
	clone.RoleBinding = RoleBinding{RoleRef: bindings.RoleRef, RoleDigest: bindings.RoleDigest, PolicyRef: bindings.PolicyRef, PolicyDigest: bindings.PolicyDigest}
	clone.Jobs = append([]Job(nil), source.Jobs...)
	for jobIndex := range clone.Jobs {
		clone.Jobs[jobIndex].Destinations = append([]Destination(nil), source.Jobs[jobIndex].Destinations...)
		if clone.Jobs[jobIndex].CategoryPolicyID != "" && clone.Jobs[jobIndex].CategoryPolicyID != bindings.PolicyRef {
			return nil, fmt.Errorf("job %q: foreign category policy ref %q", clone.Jobs[jobIndex].ID, clone.Jobs[jobIndex].CategoryPolicyID)
		}
		if clone.Jobs[jobIndex].CategoryPolicyDigest != "" && clone.Jobs[jobIndex].CategoryPolicyDigest != bindings.PolicyDigest {
			return nil, fmt.Errorf("job %q: category policy digest drift", clone.Jobs[jobIndex].ID)
		}
		if clone.Jobs[jobIndex].CategoryPolicyID == "" {
			clone.Jobs[jobIndex].CategoryPolicyID = bindings.PolicyRef
		}
		if clone.Jobs[jobIndex].CategoryPolicyVersion == 0 {
			clone.Jobs[jobIndex].CategoryPolicyVersion = 1
		}
		if clone.Jobs[jobIndex].CategoryPolicyDigest == "" {
			clone.Jobs[jobIndex].CategoryPolicyDigest = bindings.PolicyDigest
		}
		for destinationIndex := range clone.Jobs[jobIndex].Destinations {
			destination := &clone.Jobs[jobIndex].Destinations[destinationIndex]
			if destination.AccountID != "" && destination.AccountID != bindings.AccountID {
				return nil, fmt.Errorf("job %q destination %d: foreign account_id %q", clone.Jobs[jobIndex].ID, destinationIndex, destination.AccountID)
			}
			if destination.RootID != "" && destination.RootID != bindings.RootID {
				return nil, fmt.Errorf("job %q destination %d: foreign root_id %q", clone.Jobs[jobIndex].ID, destinationIndex, destination.RootID)
			}
			destination.AccountID = bindings.AccountID
			destination.RootID = bindings.RootID
			if _, err := destination.RcloneTarget(); err != nil {
				return nil, fmt.Errorf("job %q destination %d: %w", clone.Jobs[jobIndex].ID, destinationIndex, err)
			}
		}
	}
	return &clone, nil
}

type MigrationPreview struct {
	SchemaVersion int                     `json:"schema_version"`
	RcloneRuntime MigrationRuntimePreview `json:"rclone_runtime"`
	Blockers      []string                `json:"blockers,omitempty"`
	Jobs          []MigrationJobPreview   `json:"jobs"`
}

type MigrationRuntimePreview struct {
	Executable       string   `json:"executable,omitempty"`
	ExecutableFileID string   `json:"executable_file_id,omitempty"`
	ExecutableDigest string   `json:"executable_digest,omitempty"`
	Version          string   `json:"version,omitempty"`
	Provenance       string   `json:"provenance,omitempty"`
	Signature        string   `json:"signature,omitempty"`
	Owner            string   `json:"owner,omitempty"`
	ACL              string   `json:"acl,omitempty"`
	Config           string   `json:"config,omitempty"`
	ConfigFileID     string   `json:"config_file_id,omitempty"`
	ConfigDigest     string   `json:"config_digest,omitempty"`
	AllowedRemotes   []string `json:"allowed_remotes,omitempty"`
	AllowedBackends  []string `json:"allowed_backends,omitempty"`
}

type MigrationJobPreview struct {
	ID                    string                        `json:"id"`
	Source                string                        `json:"source"`
	Direction             string                        `json:"direction"`
	Mode                  string                        `json:"mode"`
	ModeGateRef           string                        `json:"mode_gate_ref,omitempty"`
	ModeGateDigest        string                        `json:"mode_gate_digest,omitempty"`
	Required              bool                          `json:"required"`
	CategoryPolicyID      string                        `json:"category_policy_id,omitempty"`
	CategoryPolicyVersion int                           `json:"category_policy_version,omitempty"`
	CategoryPolicyDigest  string                        `json:"category_policy_digest,omitempty"`
	SymlinkPolicy         string                        `json:"symlink_policy,omitempty"`
	Schedule              string                        `json:"schedule,omitempty"`
	Destinations          []MigrationDestinationPreview `json:"destinations"`
}

type MigrationDestinationPreview struct {
	Backend                string `json:"backend"`
	Path                   string `json:"path"`
	AccountID              string `json:"account_id,omitempty"`
	RootID                 string `json:"root_id,omitempty"`
	CredentialRef          string `json:"credential_ref,omitempty"`
	Required               bool   `json:"required"`
	Retention              string `json:"retention,omitempty"`
	MinCompleteRestoreSets int    `json:"min_complete_restore_sets"`
	DeletePolicy           string `json:"delete_policy"`
}

func Preview(c *Config) MigrationPreview {
	preview := MigrationPreview{
		SchemaVersion: c.SchemaVersion,
		Blockers:      migrationBlockers(c),
		RcloneRuntime: MigrationRuntimePreview{
			Executable:       redactUserPath(c.RcloneRuntime.Executable),
			ExecutableFileID: c.RcloneRuntime.ExecutableFileID,
			ExecutableDigest: c.RcloneRuntime.ExecutableDigest,
			Version:          c.RcloneRuntime.Version,
			Provenance:       c.RcloneRuntime.Provenance,
			Signature:        c.RcloneRuntime.Signature,
			Owner:            c.RcloneRuntime.Owner,
			ACL:              c.RcloneRuntime.ACL,
			Config:           redactUserPath(c.RcloneRuntime.Config),
			ConfigFileID:     c.RcloneRuntime.ConfigFileID,
			ConfigDigest:     c.RcloneRuntime.ConfigDigest,
			AllowedRemotes:   append([]string(nil), c.RcloneRuntime.AllowedRemotes...),
			AllowedBackends:  append([]string(nil), c.RcloneRuntime.AllowedBackends...),
		},
	}
	for _, job := range c.Jobs {
		item := MigrationJobPreview{ID: job.ID, Source: redactUserPath(job.Source), Direction: job.Direction, Mode: job.Mode,
			ModeGateRef: job.ModeGateRef, ModeGateDigest: job.ModeGateDigest, Required: job.Required,
			CategoryPolicyID: job.CategoryPolicyID, CategoryPolicyVersion: job.CategoryPolicyVersion,
			CategoryPolicyDigest: job.CategoryPolicyDigest, SymlinkPolicy: job.SymlinkPolicy, Schedule: job.Schedule}
		for _, destination := range job.Destinations {
			item.Destinations = append(item.Destinations, MigrationDestinationPreview{Backend: destination.Backend, Path: destination.Path,
				AccountID: destination.AccountID, RootID: destination.RootID, CredentialRef: destination.CredentialRef,
				Required: destination.Required, Retention: destination.Retention, MinCompleteRestoreSets: destination.MinCompleteRestoreSets,
				DeletePolicy: destination.DeletePolicy})
		}
		preview.Jobs = append(preview.Jobs, item)
	}
	return preview
}

func migrationBlockers(c *Config) []string {
	var blockers []string
	if err := c.RcloneRuntime.Validate(); err != nil {
		blockers = append(blockers, "rclone_runtime: "+err.Error())
	}
	if len(c.CategoryPolicies) == 0 {
		blockers = append(blockers, "category policy registry is required")
	} else if err := c.validateCategoryPolicies(); err != nil {
		blockers = append(blockers, "category policy binding: "+err.Error())
	}
	for _, job := range c.Jobs {
		if job.CategoryPolicyID == "" || job.CategoryPolicyVersion <= 0 || job.CategoryPolicyDigest == "" {
			blockers = append(blockers, fmt.Sprintf("job %q: category policy id/version/digest are required", job.ID))
		}
		for index, destination := range job.Destinations {
			if destination.AccountID == "" || destination.RootID == "" {
				blockers = append(blockers, fmt.Sprintf("job %q destination %d: account_id and root_id are required", job.ID, index))
			}
		}
	}
	return blockers
}

func redactUserPath(path string) string {
	if path == "" {
		return ""
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	parts := strings.Split(clean, "/")
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], "users") {
			parts[i+1] = "<user>"
		}
	}
	return strings.Join(parts, "/")
}
