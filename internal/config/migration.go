package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

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
