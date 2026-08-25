package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// WriteCanonicalV2CreateOnly encodes a complete schema-v2 configuration and
// creates path only when no target exists. It never invents an identity,
// credential, account, or provider object ID.
func WriteCanonicalV2CreateOnly(path string, cfg *Config) error {
	return writeCanonicalV2CreateOnly(path, cfg, FileBindingResolver{})
}

func writeCanonicalV2CreateOnly(path string, cfg *Config, resolver FileBindingResolver) (err error) {
	if strings.TrimSpace(path) == "" {
		return errors.New("canonical config path is required")
	}
	if cfg == nil {
		return errors.New("canonical config is nil")
	}
	if len(cfg.CategoryPolicies) == 0 {
		return errors.New("canonical config blocked: legacy migration lacks category policy registry")
	}
	if err := cfg.RcloneRuntime.Validate(); err != nil {
		return fmt.Errorf("canonical config blocked: legacy migration lacks execution-ready rclone_runtime: %w", err)
	}
	if err := cfg.ValidateForExecutionWithBindings(resolver, resolver); err != nil {
		return fmt.Errorf("canonical config is not execution-ready: %w", err)
	}
	if err := validateCanonicalDestinations(cfg); err != nil {
		return err
	}

	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(canonicalV2Config(cfg)); err != nil {
		return fmt.Errorf("encode canonical config: %w", err)
	}

	file, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create canonical config: %w", err)
	}
	created := true
	defer func() {
		closeErr := file.Close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("close canonical config: %w", closeErr)
		}
		if err != nil && created {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(encoded.Bytes()); err != nil {
		return fmt.Errorf("write canonical config: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync canonical config: %w", err)
	}
	created = false
	return nil
}

func validateCanonicalDestinations(cfg *Config) error {
	for _, job := range cfg.Jobs {
		for index, destination := range job.Destinations {
			if strings.TrimSpace(destination.AccountID) == "" || strings.TrimSpace(destination.RootID) == "" {
				return fmt.Errorf("job %q destination %d: account_id and root_id are required", job.ID, index)
			}
			if strings.ContainsAny(destination.CredentialRef, "\r\n") {
				return fmt.Errorf("job %q destination %d: credential_ref contains control characters", job.ID, index)
			}
			if _, err := destination.RcloneTarget(); err != nil {
				return fmt.Errorf("job %q destination %d: %w", job.ID, index, err)
			}
		}
	}
	return nil
}

type canonicalV2 struct {
	SchemaVersion    int              `toml:"schema_version"`
	RoleBinding      tomlRoleBinding  `toml:"role_binding"`
	RcloneRuntime    tomlRuntime      `toml:"rclone_runtime"`
	CategoryPolicies []CategoryPolicy `toml:"category_policy"`
	Jobs             []tomlJob        `toml:"job"`
}

func canonicalV2Config(cfg *Config) canonicalV2 {
	encoded := canonicalV2{
		SchemaVersion:    CurrentSchemaVersion,
		RoleBinding:      cfg.RoleBinding.toml(),
		RcloneRuntime:    tomlRuntime{Executable: cfg.RcloneRuntime.Executable, ExecutableFileID: cfg.RcloneRuntime.ExecutableFileID, ExecutableDigest: cfg.RcloneRuntime.ExecutableDigest, Version: cfg.RcloneRuntime.Version, Provenance: cfg.RcloneRuntime.Provenance, Signature: cfg.RcloneRuntime.Signature, Owner: cfg.RcloneRuntime.Owner, ACL: cfg.RcloneRuntime.ACL, Config: cfg.RcloneRuntime.Config, ConfigFileID: cfg.RcloneRuntime.ConfigFileID, ConfigDigest: cfg.RcloneRuntime.ConfigDigest, AllowedRemotes: append([]string(nil), cfg.RcloneRuntime.AllowedRemotes...), AllowedBackends: append([]string(nil), cfg.RcloneRuntime.AllowedBackends...), Environment: cloneStringMap(cfg.RcloneRuntime.Environment), Hooks: append([]string(nil), cfg.RcloneRuntime.Hooks...)},
		CategoryPolicies: append([]CategoryPolicy(nil), cfg.CategoryPolicies...),
	}
	for _, sourceJob := range cfg.Jobs {
		required := sourceJob.Required
		job := tomlJob{ID: sourceJob.ID, Source: sourceJob.Source, Direction: sourceJob.Direction, Mode: sourceJob.Mode, ModeGateRef: sourceJob.ModeGateRef, ModeGateDigest: sourceJob.ModeGateDigest, Required: &required, CategoryPolicyID: sourceJob.CategoryPolicyID, CategoryPolicyVersion: sourceJob.CategoryPolicyVersion, CategoryPolicyDigest: sourceJob.CategoryPolicyDigest, SymlinkPolicy: sourceJob.SymlinkPolicy, Schedule: sourceJob.Schedule, Exclude: append([]string(nil), sourceJob.Exclude...)}
		for _, sourceDestination := range sourceJob.Destinations {
			destinationRequired := sourceDestination.Required
			minRestoreSets := sourceDestination.MinCompleteRestoreSets
			deletePolicy := sourceDestination.DeletePolicy
			job.Destinations = append(job.Destinations, tomlDestination{Backend: sourceDestination.Backend, Path: sourceDestination.Path, AccountID: sourceDestination.AccountID, RootID: sourceDestination.RootID, CredentialRef: sourceDestination.CredentialRef, Required: &destinationRequired, Retention: sourceDestination.Retention, MinCompleteRestoreSets: &minRestoreSets, DeletePolicy: &deletePolicy})
		}
		encoded.Jobs = append(encoded.Jobs, job)
	}
	return encoded
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
