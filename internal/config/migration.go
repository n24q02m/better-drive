package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

type MigrationBindings struct {
	AccountID      string
	RootID         string
	RoleRef        string
	RoleDigest     string
	PolicyRef      string
	PolicyDigest   string
	RcloneRuntime  RcloneRuntime
	CategoryPolicy CategoryPolicy
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
	if err := b.RcloneRuntime.Validate(); err != nil {
		return fmt.Errorf("complete migration bindings require rclone_runtime: %w", err)
	}
	for name, values := range map[string][]string{
		"allowed_remotes": b.RcloneRuntime.AllowedRemotes, "allowed_backends": b.RcloneRuntime.AllowedBackends,
	} {
		if len(values) == 0 {
			return fmt.Errorf("complete migration bindings require rclone_runtime.%s", name)
		}
		for index, value := range values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("complete migration bindings require rclone_runtime.%s[%d]", name, index)
			}
		}
	}
	if err := b.CategoryPolicy.Validate(); err != nil {
		return fmt.Errorf("complete migration bindings require category_policy: %w", err)
	}
	return nil
}

// ApplyMigrationBindings returns a new config with caller-supplied identities,
// runtime, and category policy applied. Existing non-empty values must agree;
// no ID, policy version, or secret is generated for a legacy target.
func ApplyMigrationBindings(source *Config, bindings MigrationBindings) (*Config, error) {
	if source == nil {
		return nil, errors.New("migration config is nil")
	}
	if err := bindings.Validate(); err != nil {
		return nil, err
	}

	clone := *source
	var err error
	clone.RoleBinding, err = mergeRoleBinding(source.RoleBinding, RoleBinding{
		RoleRef: bindings.RoleRef, RoleDigest: bindings.RoleDigest,
		PolicyRef: bindings.PolicyRef, PolicyDigest: bindings.PolicyDigest,
	})
	if err != nil {
		return nil, err
	}
	clone.RcloneRuntime, err = mergeRcloneRuntime(source.RcloneRuntime, bindings.RcloneRuntime)
	if err != nil {
		return nil, err
	}
	clone.CategoryPolicies, err = mergeCategoryPolicies(source.CategoryPolicies, bindings.CategoryPolicy)
	if err != nil {
		return nil, err
	}
	clone.Jobs = append([]Job(nil), source.Jobs...)
	for jobIndex := range clone.Jobs {
		clone.Jobs[jobIndex].Exclude = append([]string(nil), source.Jobs[jobIndex].Exclude...)
		job := &clone.Jobs[jobIndex]
		job.Destinations = append([]Destination(nil), source.Jobs[jobIndex].Destinations...)
		if job.CategoryPolicyID != "" && job.CategoryPolicyID != bindings.CategoryPolicy.ID {
			return nil, fmt.Errorf("job %q: foreign category policy ref %q", job.ID, job.CategoryPolicyID)
		}
		if job.CategoryPolicyVersion != 0 && job.CategoryPolicyVersion != bindings.CategoryPolicy.Version {
			return nil, fmt.Errorf("job %q: category policy version drift", job.ID)
		}
		if job.CategoryPolicyDigest != "" && job.CategoryPolicyDigest != bindings.CategoryPolicy.Digest {
			return nil, fmt.Errorf("job %q: category policy digest drift", job.ID)
		}
		if job.CategoryPolicyID == "" {
			job.CategoryPolicyID = bindings.CategoryPolicy.ID
		}
		if job.CategoryPolicyVersion == 0 {
			job.CategoryPolicyVersion = bindings.CategoryPolicy.Version
		}
		if job.CategoryPolicyDigest == "" {
			job.CategoryPolicyDigest = bindings.CategoryPolicy.Digest
		}
		for destinationIndex := range job.Destinations {
			destination := &job.Destinations[destinationIndex]
			if destination.AccountID != "" && destination.AccountID != bindings.AccountID {
				return nil, fmt.Errorf("job %q destination %d: foreign account_id %q", job.ID, destinationIndex, destination.AccountID)
			}
			if destination.RootID != "" && destination.RootID != bindings.RootID {
				return nil, fmt.Errorf("job %q destination %d: foreign root_id %q", job.ID, destinationIndex, destination.RootID)
			}
			destination.AccountID = bindings.AccountID
			destination.RootID = bindings.RootID
			if _, err := destination.RcloneTarget(); err != nil {
				return nil, fmt.Errorf("job %q destination %d: %w", job.ID, destinationIndex, err)
			}
		}
	}
	return &clone, nil
}

func mergeRoleBinding(source, binding RoleBinding) (RoleBinding, error) {
	var err error
	merged := binding
	if merged.RoleRef, err = mergeMigrationString("role_binding.role_ref", source.RoleRef, binding.RoleRef); err != nil {
		return RoleBinding{}, err
	}
	if merged.RoleDigest, err = mergeMigrationString("role_binding.role_digest", source.RoleDigest, binding.RoleDigest); err != nil {
		return RoleBinding{}, err
	}
	if merged.PolicyRef, err = mergeMigrationString("role_binding.policy_ref", source.PolicyRef, binding.PolicyRef); err != nil {
		return RoleBinding{}, err
	}
	if merged.PolicyDigest, err = mergeMigrationString("role_binding.policy_digest", source.PolicyDigest, binding.PolicyDigest); err != nil {
		return RoleBinding{}, err
	}
	return merged, nil
}

func mergeRcloneRuntime(source, binding RcloneRuntime) (RcloneRuntime, error) {
	merged := binding
	var err error
	if merged.Executable, err = mergeMigrationString("rclone_runtime.executable", source.Executable, binding.Executable); err != nil {
		return RcloneRuntime{}, err
	}
	if merged.ExecutableFileID, err = mergeMigrationString("rclone_runtime.executable_file_id", source.ExecutableFileID, binding.ExecutableFileID); err != nil {
		return RcloneRuntime{}, err
	}
	if merged.ExecutableDigest, err = mergeMigrationString("rclone_runtime.executable_digest", source.ExecutableDigest, binding.ExecutableDigest); err != nil {
		return RcloneRuntime{}, err
	}
	if merged.Version, err = mergeMigrationString("rclone_runtime.version", source.Version, binding.Version); err != nil {
		return RcloneRuntime{}, err
	}
	if merged.Provenance, err = mergeMigrationString("rclone_runtime.provenance", source.Provenance, binding.Provenance); err != nil {
		return RcloneRuntime{}, err
	}
	if merged.Signature, err = mergeMigrationString("rclone_runtime.signature", source.Signature, binding.Signature); err != nil {
		return RcloneRuntime{}, err
	}
	if merged.Owner, err = mergeMigrationString("rclone_runtime.owner", source.Owner, binding.Owner); err != nil {
		return RcloneRuntime{}, err
	}
	if merged.ACL, err = mergeMigrationString("rclone_runtime.acl", source.ACL, binding.ACL); err != nil {
		return RcloneRuntime{}, err
	}
	if merged.Config, err = mergeMigrationString("rclone_runtime.config", source.Config, binding.Config); err != nil {
		return RcloneRuntime{}, err
	}
	if merged.ConfigFileID, err = mergeMigrationString("rclone_runtime.config_file_id", source.ConfigFileID, binding.ConfigFileID); err != nil {
		return RcloneRuntime{}, err
	}
	if merged.ConfigDigest, err = mergeMigrationString("rclone_runtime.config_digest", source.ConfigDigest, binding.ConfigDigest); err != nil {
		return RcloneRuntime{}, err
	}
	if len(source.AllowedRemotes) != 0 {
		if !slices.Equal(source.AllowedRemotes, binding.AllowedRemotes) {
			return RcloneRuntime{}, errors.New("migration rclone_runtime.allowed_remotes drift")
		}
		merged.AllowedRemotes = append([]string(nil), source.AllowedRemotes...)
	} else {
		merged.AllowedRemotes = append([]string(nil), binding.AllowedRemotes...)
	}
	if len(source.AllowedBackends) != 0 {
		if !slices.Equal(source.AllowedBackends, binding.AllowedBackends) {
			return RcloneRuntime{}, errors.New("migration rclone_runtime.allowed_backends drift")
		}
		merged.AllowedBackends = append([]string(nil), source.AllowedBackends...)
	} else {
		merged.AllowedBackends = append([]string(nil), binding.AllowedBackends...)
	}
	if len(source.Environment) != 0 {
		if len(binding.Environment) == 0 || !sameStringMap(source.Environment, binding.Environment) {
			return RcloneRuntime{}, errors.New("migration rclone_runtime.environment drift")
		}
		merged.Environment = cloneStringMap(source.Environment)
	} else {
		merged.Environment = cloneStringMap(binding.Environment)
	}
	if len(source.Hooks) != 0 {
		if len(binding.Hooks) == 0 || !slices.Equal(source.Hooks, binding.Hooks) {
			return RcloneRuntime{}, errors.New("migration rclone_runtime.hooks drift")
		}
		merged.Hooks = append([]string(nil), source.Hooks...)
	} else {
		merged.Hooks = append([]string(nil), binding.Hooks...)
	}
	return merged, nil
}

func mergeCategoryPolicies(source []CategoryPolicy, binding CategoryPolicy) ([]CategoryPolicy, error) {
	if len(source) == 0 {
		return []CategoryPolicy{cloneCategoryPolicy(binding)}, nil
	}
	merged := make([]CategoryPolicy, len(source))
	for index, policy := range source {
		var err error
		merged[index], err = mergeCategoryPolicy(policy, binding)
		if err != nil {
			return nil, fmt.Errorf("category policy %d: %w", index, err)
		}
	}
	return merged, nil
}

func mergeCategoryPolicy(source, binding CategoryPolicy) (CategoryPolicy, error) {
	merged := binding
	var err error
	if merged.ID, err = mergeMigrationString("category_policy.id", source.ID, binding.ID); err != nil {
		return CategoryPolicy{}, err
	}
	if source.Version != 0 && source.Version != binding.Version {
		return CategoryPolicy{}, errors.New("migration category_policy.version drift")
	}
	if source.Version != 0 {
		merged.Version = source.Version
	}
	if merged.Digest, err = mergeMigrationString("category_policy.digest", source.Digest, binding.Digest); err != nil {
		return CategoryPolicy{}, err
	}
	if merged.AllowlistedRoot, err = mergeMigrationString("category_policy.allowlisted_root", source.AllowlistedRoot, binding.AllowlistedRoot); err != nil {
		return CategoryPolicy{}, err
	}
	if len(source.MandatoryDenylist) != 0 {
		if !slices.Equal(source.MandatoryDenylist, binding.MandatoryDenylist) {
			return CategoryPolicy{}, errors.New("migration category_policy.mandatory_denylist drift")
		}
		merged.MandatoryDenylist = append([]string(nil), source.MandatoryDenylist...)
	} else {
		merged.MandatoryDenylist = append([]string(nil), binding.MandatoryDenylist...)
	}
	if source.SizeGuard.MaxBytes != 0 && source.SizeGuard.MaxBytes != binding.SizeGuard.MaxBytes {
		return CategoryPolicy{}, errors.New("migration category_policy.size_guard.max_bytes drift")
	}
	if source.SizeGuard.MaxBytes != 0 {
		merged.SizeGuard.MaxBytes = source.SizeGuard.MaxBytes
	}
	if merged.RestoreExpectation, err = mergeMigrationString("category_policy.restore_expectation", source.RestoreExpectation, binding.RestoreExpectation); err != nil {
		return CategoryPolicy{}, err
	}
	return merged, nil
}

func mergeMigrationString(field, source, binding string) (string, error) {
	if source != "" && source != binding {
		return "", fmt.Errorf("migration %s drift", field)
	}
	if source != "" {
		return source, nil
	}
	return binding, nil
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		got, ok := right[key]
		if !ok || got != value {
			return false
		}
	}
	return true
}

func cloneCategoryPolicy(policy CategoryPolicy) CategoryPolicy {
	policy.MandatoryDenylist = append([]string(nil), policy.MandatoryDenylist...)
	return policy
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
