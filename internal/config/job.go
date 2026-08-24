package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const CurrentSchemaVersion = 2

type Job struct {
	ID                    string        `toml:"id" json:"id"`
	Source                string        `toml:"source" json:"source"`
	Direction             string        `toml:"direction" json:"direction"`
	Mode                  string        `toml:"mode" json:"mode"`
	ModeGateRef           string        `toml:"mode_gate_ref" json:"mode_gate_ref,omitempty"`
	ModeGateDigest        string        `toml:"mode_gate_digest" json:"mode_gate_digest,omitempty"`
	Required              bool          `toml:"required" json:"required"`
	CategoryPolicyID      string        `toml:"category_policy_id" json:"category_policy_id"`
	CategoryPolicyVersion int           `toml:"category_policy_version" json:"category_policy_version"`
	CategoryPolicyDigest  string        `toml:"category_policy_digest" json:"category_policy_digest"`
	SymlinkPolicy         string        `toml:"symlink_policy" json:"symlink_policy"`
	Interval              time.Duration `toml:"-" json:"-"`
	Schedule              string        `toml:"schedule" json:"schedule"`
	Exclude               []string      `toml:"exclude" json:"exclude,omitempty"`
	Destinations          []Destination `toml:"destination" json:"destination"`
}

type Destination struct {
	Backend                string `toml:"backend" json:"backend"`
	Path                   string `toml:"path" json:"path"`
	AccountID              string `toml:"account_id" json:"account_id"`
	RootID                 string `toml:"root_id" json:"root_id"`
	CredentialRef          string `toml:"credential_ref" json:"credential_ref"`
	Required               bool   `toml:"required" json:"required"`
	Retention              string `toml:"retention" json:"retention"`
	MinCompleteRestoreSets int    `toml:"min_complete_restore_sets" json:"min_complete_restore_sets"`
	DeletePolicy           string `toml:"delete_policy" json:"delete_policy"`
}

// RcloneRuntime is the immutable runtime identity bound to a role. It is not
// inferred from PATH, RCLONE_* variables, or rclone's default discovery.
type RcloneRuntime struct {
	Executable       string            `toml:"executable" json:"executable"`
	ExecutableFileID string            `toml:"executable_file_id" json:"executable_file_id"`
	ExecutableDigest string            `toml:"executable_digest" json:"executable_digest"`
	Version          string            `toml:"version" json:"version"`
	Provenance       string            `toml:"provenance" json:"provenance"`
	Signature        string            `toml:"signature" json:"signature"`
	Owner            string            `toml:"owner" json:"owner"`
	ACL              string            `toml:"acl" json:"acl"`
	Config           string            `toml:"config" json:"config"`
	ConfigFileID     string            `toml:"config_file_id" json:"config_file_id"`
	ConfigDigest     string            `toml:"config_digest" json:"config_digest"`
	AllowedRemotes   []string          `toml:"allowed_remotes" json:"allowed_remotes"`
	AllowedBackends  []string          `toml:"allowed_backends" json:"allowed_backends"`
	Environment      map[string]string `toml:"environment" json:"environment,omitempty"`
	Hooks            []string          `toml:"hooks" json:"hooks,omitempty"`
}

func (r RcloneRuntime) Validate() error {
	if !filepath.IsAbs(r.Executable) {
		return fmt.Errorf("rclone_runtime.executable must be absolute")
	}
	if !filepath.IsAbs(r.Config) {
		return fmt.Errorf("rclone_runtime.config must be absolute")
	}
	for name, value := range map[string]string{
		"executable_file_id": r.ExecutableFileID,
		"executable_digest":  r.ExecutableDigest,
		"version":            r.Version,
		"provenance":         r.Provenance,
		"signature":          r.Signature,
		"owner":              r.Owner,
		"acl":                r.ACL,
		"config_file_id":     r.ConfigFileID,
		"config_digest":      r.ConfigDigest,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("rclone_runtime.%s is required", name)
		}
	}
	for name, digest := range map[string]string{
		"executable_digest": r.ExecutableDigest,
		"config_digest":     r.ConfigDigest,
	} {
		if !isSHA256Digest(digest) {
			return fmt.Errorf("rclone_runtime.%s must use sha256:<64 hex chars>", name)
		}
	}
	if len(r.AllowedRemotes) == 0 {
		return fmt.Errorf("rclone_runtime.allowed_remotes is required")
	}
	if len(r.AllowedBackends) == 0 {
		return fmt.Errorf("rclone_runtime.allowed_backends is required")
	}
	for name := range r.Environment {
		upper := strings.ToUpper(strings.TrimSpace(name))
		if upper == "PATH" || (strings.HasPrefix(upper, "RCLONE_") && upper != "RCLONE_LOCAL_NO_CHECK_UPDATED") {
			return fmt.Errorf("rclone_runtime.environment.%s is forbidden", name)
		}
	}
	if len(r.Hooks) != 0 {
		return fmt.Errorf("rclone_runtime.hooks is forbidden")
	}
	return nil
}

type CategorySizeGuard struct {
	MaxBytes int64 `toml:"max_bytes" json:"max_bytes"`
}

type CategoryPolicy struct {
	ID                 string            `toml:"id" json:"id"`
	Version            int               `toml:"version" json:"version"`
	Digest             string            `toml:"digest" json:"digest"`
	AllowlistedRoot    string            `toml:"allowlisted_root" json:"allowlisted_root"`
	MandatoryDenylist  []string          `toml:"mandatory_denylist" json:"mandatory_denylist"`
	SizeGuard          CategorySizeGuard `toml:"size_guard" json:"size_guard"`
	RestoreExpectation string            `toml:"restore_expectation" json:"restore_expectation"`
}

func (p CategoryPolicy) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if p.Version <= 0 {
		return fmt.Errorf("version must be > 0")
	}
	if !isSHA256Digest(p.Digest) {
		return fmt.Errorf("digest must use sha256:<64 hex chars>")
	}
	if strings.TrimSpace(p.AllowlistedRoot) == "" {
		return fmt.Errorf("allowlisted_root is required")
	}
	if len(p.MandatoryDenylist) == 0 {
		return fmt.Errorf("mandatory_denylist is required")
	}
	for _, entry := range p.MandatoryDenylist {
		if strings.TrimSpace(entry) == "" {
			return fmt.Errorf("mandatory_denylist contains an empty entry")
		}
	}
	if p.SizeGuard.MaxBytes <= 0 {
		return fmt.Errorf("size_guard.max_bytes must be > 0")
	}
	if strings.TrimSpace(p.RestoreExpectation) == "" {
		return fmt.Errorf("restore_expectation is required")
	}
	return nil
}

func (c *Config) validateCategoryPolicies() error {
	if len(c.CategoryPolicies) == 0 {
		return fmt.Errorf("category policy registry is required")
	}
	byBinding := make(map[string]CategoryPolicy, len(c.CategoryPolicies))
	for _, policy := range c.CategoryPolicies {
		if err := policy.Validate(); err != nil {
			return fmt.Errorf("category policy %q: %w", policy.ID, err)
		}
		binding := fmt.Sprintf("%s/%d", policy.ID, policy.Version)
		if _, exists := byBinding[binding]; exists {
			return fmt.Errorf("duplicate category policy %s", binding)
		}
		byBinding[binding] = policy
	}
	for _, job := range c.Jobs {
		binding := fmt.Sprintf("%s/%d", job.CategoryPolicyID, job.CategoryPolicyVersion)
		policy, exists := byBinding[binding]
		if !exists {
			return fmt.Errorf("job %q references missing category policy %s", job.ID, binding)
		}
		if policy.Digest != job.CategoryPolicyDigest {
			return fmt.Errorf("job %q category policy digest mismatch", job.ID)
		}
		if !policyPathWithin(policy.AllowlistedRoot, job.Source) {
			return fmt.Errorf("job %q source is outside category policy allowlisted_root", job.ID)
		}
		for _, denylistEntry := range policy.MandatoryDenylist {
			if !containsString(job.Exclude, denylistEntry) {
				return fmt.Errorf("job %q is missing mandatory denylist entry %q", job.ID, denylistEntry)
			}
		}
	}
	return nil
}

func policyPathWithin(root, source string) bool {
	normalize := func(value string) string {
		value = filepath.ToSlash(filepath.Clean(strings.ReplaceAll(value, "\\", "/")))
		value = strings.TrimRight(value, "/")
		if runtime.GOOS == "windows" {
			value = strings.ToLower(value)
		}
		return value
	}
	root = normalize(root)
	source = normalize(source)
	if root == "" || source == "" {
		return false
	}
	return source == root || strings.HasPrefix(source, root+"/")
}
func (c *Config) validateCategorySourceSizes() error {
	policies := make(map[string]CategoryPolicy, len(c.CategoryPolicies))
	for _, policy := range c.CategoryPolicies {
		policies[fmt.Sprintf("%s/%d", policy.ID, policy.Version)] = policy
	}
	for _, job := range c.Jobs {
		policy := policies[fmt.Sprintf("%s/%d", job.CategoryPolicyID, job.CategoryPolicyVersion)]
		if _, err := sourceSizeWithinGuard(job.Source, job.Exclude, policy.SizeGuard.MaxBytes); err != nil {
			return fmt.Errorf("job %q: %w", job.ID, err)
		}
	}
	return nil
}

func sourceSizeWithinGuard(root string, excludes []string, maxBytes int64) (int64, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var total int64
	add := func(size int64) error {
		if size > maxBytes-total {
			return fmt.Errorf("category source exceeds size guard (%d bytes)", maxBytes)
		}
		total += size
		return nil
	}
	if !info.IsDir() {
		if err := add(info.Size()); err != nil {
			return 0, err
		}
		return total, nil
	}
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative != "." && policyPathExcluded(relative, excludes) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Mode().IsRegular() {
			return add(info.Size())
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func policyPathExcluded(relative string, excludes []string) bool {
	relative = filepath.ToSlash(filepath.Clean(relative))
	for _, entry := range excludes {
		entry = strings.TrimRight(filepath.ToSlash(filepath.Clean(entry)), "/")
		if entry != "" && (relative == entry || strings.HasPrefix(relative, entry+"/")) {
			return true
		}
	}
	return false
}

func (r RcloneRuntime) ValidateDestination(destination Destination) error {
	target, err := destination.RcloneTarget()
	if err != nil {
		return err
	}
	remote, _, _ := strings.Cut(target, ":")
	if !containsString(r.AllowedRemotes, remote) {
		return fmt.Errorf("rclone_runtime.allowed_remotes does not include %q", remote)
	}
	if !containsString(r.AllowedBackends, destination.Backend) {
		return fmt.Errorf("rclone_runtime.allowed_backends does not include %q", destination.Backend)
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func isSHA256Digest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}

// RoleBinding is the explicit role/profile and policy identity enrolled for
// scheduled execution. References and digests are read back from their
// authorities; they are never inferred from local environment or credentials.
type RoleBinding struct {
	RoleRef      string `toml:"role_ref" json:"role_ref"`
	RoleDigest   string `toml:"role_digest" json:"role_digest"`
	PolicyRef    string `toml:"policy_ref" json:"policy_ref"`
	PolicyDigest string `toml:"policy_digest" json:"policy_digest"`
}

func (b RoleBinding) Validate() error {
	if strings.TrimSpace(b.RoleRef) == "" {
		return fmt.Errorf("role_ref is required")
	}
	if !isSHA256Digest(b.RoleDigest) {
		return fmt.Errorf("role_digest must use sha256:<64 hex chars>")
	}
	if strings.TrimSpace(b.PolicyRef) == "" {
		return fmt.Errorf("policy_ref is required")
	}
	if !isSHA256Digest(b.PolicyDigest) {
		return fmt.Errorf("policy_digest must use sha256:<64 hex chars>")
	}
	return nil
}

// BindingReadback is the non-secret identity returned by an authority.
type BindingReadback struct {
	Ref    string
	Digest string
}

// PolicyBindingResolver reads the current policy identity at execution time.
type PolicyBindingResolver interface {
	ReadPolicyBinding(ref string) (BindingReadback, error)
}

// RoleBindingResolver reads the current role/profile identity at execution time.
type RoleBindingResolver interface {
	ReadRoleBinding(ref string) (BindingReadback, error)
}

type tomlRoleBinding struct {
	RoleRef      string `toml:"role_ref"`
	RoleDigest   string `toml:"role_digest"`
	PolicyRef    string `toml:"policy_ref"`
	PolicyDigest string `toml:"policy_digest"`
}

func (b RoleBinding) toml() tomlRoleBinding {
	return tomlRoleBinding{RoleRef: b.RoleRef, RoleDigest: b.RoleDigest, PolicyRef: b.PolicyRef, PolicyDigest: b.PolicyDigest}
}

func roleBindingFromTOML(raw tomlRoleBinding) RoleBinding {
	return RoleBinding{RoleRef: raw.RoleRef, RoleDigest: raw.RoleDigest, PolicyRef: raw.PolicyRef, PolicyDigest: raw.PolicyDigest}
}

type BindingValidationError struct {
	Kind string
	Ref  string
	Want string
	Got  string
}

func (e *BindingValidationError) Error() string {
	return fmt.Sprintf("%s binding drift for %q: want %s, got %s", e.Kind, e.Ref, e.Want, e.Got)
}

type Config struct {
	SchemaVersion    int              `toml:"schema_version" json:"schema_version"`
	RoleBinding      RoleBinding      `toml:"role_binding" json:"role_binding"`
	RcloneRuntime    RcloneRuntime    `toml:"rclone_runtime" json:"rclone_runtime"`
	CategoryPolicies []CategoryPolicy `toml:"category_policy" json:"category_policy"`
	Jobs             []Job            `toml:"job" json:"job"`
}

type LoadOptions struct {
	EnrolledBidirectionalJobIDs map[string]bool
}

type tomlRuntime struct {
	Executable       string            `toml:"executable"`
	ExecutableFileID string            `toml:"executable_file_id"`
	ExecutableDigest string            `toml:"executable_digest"`
	Version          string            `toml:"version"`
	Provenance       string            `toml:"provenance"`
	Signature        string            `toml:"signature"`
	Owner            string            `toml:"owner"`
	ACL              string            `toml:"acl"`
	Config           string            `toml:"config"`
	ConfigFileID     string            `toml:"config_file_id"`
	ConfigDigest     string            `toml:"config_digest"`
	AllowedRemotes   []string          `toml:"allowed_remotes"`
	AllowedBackends  []string          `toml:"allowed_backends"`
	Environment      map[string]string `toml:"environment"`
	Hooks            []string          `toml:"hooks"`
}

type tomlDestination struct {
	Backend                string  `toml:"backend"`
	Path                   string  `toml:"path"`
	AccountID              string  `toml:"account_id"`
	RootID                 string  `toml:"root_id"`
	CredentialRef          string  `toml:"credential_ref"`
	Required               *bool   `toml:"required"`
	Retention              string  `toml:"retention"`
	MinCompleteRestoreSets *int    `toml:"min_complete_restore_sets"`
	DeletePolicy           *string `toml:"delete_policy"`
}

type tomlJob struct {
	ID                    string            `toml:"id"`
	Source                string            `toml:"source"`
	Direction             string            `toml:"direction"`
	Mode                  string            `toml:"mode"`
	ModeGateRef           string            `toml:"mode_gate_ref"`
	ModeGateDigest        string            `toml:"mode_gate_digest"`
	Required              *bool             `toml:"required"`
	CategoryPolicyID      string            `toml:"category_policy_id"`
	CategoryPolicyVersion int               `toml:"category_policy_version"`
	CategoryPolicyDigest  string            `toml:"category_policy_digest"`
	SymlinkPolicy         string            `toml:"symlink_policy"`
	Schedule              string            `toml:"schedule"`
	Exclude               []string          `toml:"exclude"`
	Destinations          []tomlDestination `toml:"destination"`
}

type tomlPair struct {
	Local    string   `toml:"local"`
	Remote   string   `toml:"remote"`
	Interval string   `toml:"interval"`
	Mode     string   `toml:"mode"`
	Exclude  []string `toml:"exclude"`
}

type rawConfig struct {
	SchemaVersion    int              `toml:"schema_version"`
	RoleBinding      tomlRoleBinding  `toml:"role_binding"`
	RcloneConfig     string           `toml:"rclone_config"`
	RcloneRuntime    tomlRuntime      `toml:"rclone_runtime"`
	CategoryPolicies []CategoryPolicy `toml:"category_policy"`
	Jobs             []tomlJob        `toml:"job"`
	Pairs            []tomlPair       `toml:"pair"`
}

func LoadWithOptions(path string, options LoadOptions) (*Config, error) {
	var raw rawConfig
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if raw.SchemaVersion == 0 && len(raw.Jobs) == 0 {
		return migrateLegacy(raw.Pairs, raw.RcloneConfig, options)
	}
	if raw.SchemaVersion != CurrentSchemaVersion {
		return nil, fmt.Errorf("unsupported schema_version %d, want %d", raw.SchemaVersion, CurrentSchemaVersion)
	}
	if len(raw.Pairs) != 0 {
		return nil, fmt.Errorf("schema v2 cannot contain legacy [[pair]] entries")
	}
	return decodeV2(raw)
}

func decodeV2(raw rawConfig) (*Config, error) {
	cfg := &Config{SchemaVersion: CurrentSchemaVersion, RoleBinding: roleBindingFromTOML(raw.RoleBinding), RcloneRuntime: RcloneRuntime{
		Executable: raw.RcloneRuntime.Executable, ExecutableFileID: raw.RcloneRuntime.ExecutableFileID,
		ExecutableDigest: raw.RcloneRuntime.ExecutableDigest, Version: raw.RcloneRuntime.Version,
		Provenance: raw.RcloneRuntime.Provenance, Signature: raw.RcloneRuntime.Signature,
		Owner: raw.RcloneRuntime.Owner, ACL: raw.RcloneRuntime.ACL, Config: raw.RcloneRuntime.Config,
		ConfigFileID: raw.RcloneRuntime.ConfigFileID, ConfigDigest: raw.RcloneRuntime.ConfigDigest,
		AllowedRemotes: raw.RcloneRuntime.AllowedRemotes, AllowedBackends: raw.RcloneRuntime.AllowedBackends,
		Environment: raw.RcloneRuntime.Environment, Hooks: raw.RcloneRuntime.Hooks,
	}, CategoryPolicies: append([]CategoryPolicy(nil), raw.CategoryPolicies...)}
	for i, rawJob := range raw.Jobs {
		if rawJob.Required == nil {
			return nil, fmt.Errorf("job %d: required must be explicit", i)
		}
		interval, err := time.ParseDuration(rawJob.Schedule)
		if err != nil || interval <= 0 {
			return nil, fmt.Errorf("job %q: bad schedule %q", rawJob.ID, rawJob.Schedule)
		}
		job := Job{ID: rawJob.ID, Source: rawJob.Source, Direction: rawJob.Direction, Mode: rawJob.Mode,
			ModeGateRef: rawJob.ModeGateRef, ModeGateDigest: rawJob.ModeGateDigest,
			Required: *rawJob.Required, CategoryPolicyID: rawJob.CategoryPolicyID,
			CategoryPolicyVersion: rawJob.CategoryPolicyVersion, CategoryPolicyDigest: rawJob.CategoryPolicyDigest,
			SymlinkPolicy: rawJob.SymlinkPolicy, Interval: interval, Schedule: rawJob.Schedule, Exclude: rawJob.Exclude}
		for j, rawDestination := range rawJob.Destinations {
			if rawDestination.Required == nil {
				return nil, fmt.Errorf("job %q destination %d: required must be explicit", rawJob.ID, j)
			}
			if rawDestination.MinCompleteRestoreSets == nil {
				return nil, fmt.Errorf("job %q destination %d: min_complete_restore_sets must be explicit", rawJob.ID, j)
			}
			if rawDestination.DeletePolicy == nil {
				return nil, fmt.Errorf("job %q destination %d: delete_policy must be explicit", rawJob.ID, j)
			}
			job.Destinations = append(job.Destinations, Destination{Backend: rawDestination.Backend, Path: rawDestination.Path,
				AccountID: rawDestination.AccountID, RootID: rawDestination.RootID, CredentialRef: rawDestination.CredentialRef,
				Required: *rawDestination.Required, Retention: rawDestination.Retention,
				MinCompleteRestoreSets: *rawDestination.MinCompleteRestoreSets, DeletePolicy: *rawDestination.DeletePolicy})
		}
		cfg.Jobs = append(cfg.Jobs, job)
	}
	return cfg, nil
}

func migrateLegacy(pairs []tomlPair, rcloneConfig string, options LoadOptions) (*Config, error) {
	cfg := &Config{SchemaVersion: CurrentSchemaVersion}
	cfg.RcloneRuntime.Config = rcloneConfig
	for _, legacy := range pairs {
		interval, err := time.ParseDuration(legacy.Interval)
		if err != nil || interval <= 0 {
			return nil, fmt.Errorf("pair %q: bad interval %q", legacy.Local, legacy.Interval)
		}
		id := LegacyJobID(legacy.Local, legacy.Remote)
		mode := legacy.Mode
		if mode == "" || mode == "default" {
			mode = "copy"
		}
		direction := "push"
		if mode == "bisync" {
			if !options.EnrolledBidirectionalJobIDs[id] {
				return nil, fmt.Errorf("pair %q: legacy bisync requires explicit enrollment for job %s", legacy.Local, id)
			}
			direction = "bidirectional"
		}
		if mode != "copy" && mode != "sync" && mode != "bisync" {
			return nil, fmt.Errorf("pair %q: unsupported legacy mode %q", legacy.Local, legacy.Mode)
		}
		backend, path := splitLegacyRemote(legacy.Remote)
		cfg.Jobs = append(cfg.Jobs, Job{ID: id, Source: legacy.Local, Direction: direction, Mode: mode, Required: true,
			SymlinkPolicy: "preserve", Interval: interval, Schedule: legacy.Interval, Exclude: legacy.Exclude,
			Destinations: []Destination{{Backend: backend, Path: path, CredentialRef: "rclone:" + backend,
				Required: true, MinCompleteRestoreSets: 2, DeletePolicy: "none"}}})
	}
	return cfg, nil
}

func LegacyJobID(local, remote string) string {
	sum := sha256.Sum256([]byte(local + "\x00" + remote))
	return "legacy-" + hex.EncodeToString(sum[:8])
}

func splitLegacyRemote(remote string) (string, string) {
	name, path, found := strings.Cut(remote, ":")
	if !found {
		return "", remote
	}
	return name, path
}

func (c *Config) ValidateForExecution() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.validateCategoryPolicies(); err != nil {
		return err
	}
	if err := c.RcloneRuntime.Validate(); err != nil {
		return err
	}
	if err := c.validateCategorySourceSizes(); err != nil {
		return err
	}
	for _, job := range c.Jobs {
		for _, destination := range job.Destinations {
			if err := c.RcloneRuntime.ValidateDestination(destination); err != nil {
				return fmt.Errorf("job %q: %w", job.ID, err)
			}
		}
	}
	return nil
}

// ValidateForExecutionWithBindings performs the normal execution checks and
// then re-reads both enrolled binding identities. A scheduled caller must use
// this fresh readback path rather than trusting a cached profile or policy.
func (c *Config) ValidateForExecutionWithBindings(policyResolver PolicyBindingResolver, roleResolver RoleBindingResolver) error {
	if err := c.ValidateForExecution(); err != nil {
		return err
	}
	if policyResolver == nil || roleResolver == nil {
		return errors.New("config binding resolver is required for execution")
	}
	if err := c.RoleBinding.Validate(); err != nil {
		return fmt.Errorf("role binding: %w", err)
	}
	role, err := roleResolver.ReadRoleBinding(c.RoleBinding.RoleRef)
	if err != nil {
		return fmt.Errorf("role binding readback: %w", err)
	}
	if role.Ref != c.RoleBinding.RoleRef || role.Digest != c.RoleBinding.RoleDigest {
		return &BindingValidationError{Kind: "role", Ref: c.RoleBinding.RoleRef, Want: c.RoleBinding.RoleDigest, Got: role.Digest}
	}
	policy, err := policyResolver.ReadPolicyBinding(c.RoleBinding.PolicyRef)
	if err != nil {
		return fmt.Errorf("policy binding readback: %w", err)
	}
	if policy.Ref != c.RoleBinding.PolicyRef || policy.Digest != c.RoleBinding.PolicyDigest {
		return &BindingValidationError{Kind: "policy", Ref: c.RoleBinding.PolicyRef, Want: c.RoleBinding.PolicyDigest, Got: policy.Digest}
	}
	return nil
}

func (c *Config) Validate() error {
	if c.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("config: schema_version must be %d, got %d", CurrentSchemaVersion, c.SchemaVersion)
	}
	if len(c.Jobs) == 0 {
		return fmt.Errorf("config: at least 1 job required, got 0")
	}
	seen := make(map[string]struct{}, len(c.Jobs))
	for i, job := range c.Jobs {
		if err := job.validate(seen); err != nil {
			return fmt.Errorf("job %d: %w", i, err)
		}
	}
	return nil
}

func (j Job) validate(seen map[string]struct{}) error {
	if strings.TrimSpace(j.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if _, exists := seen[j.ID]; exists {
		return fmt.Errorf("duplicate id %q", j.ID)
	}
	seen[j.ID] = struct{}{}
	if strings.TrimSpace(j.Source) == "" {
		return fmt.Errorf("source is required")
	}
	if j.Direction != "push" && j.Direction != "pull" && j.Direction != "bidirectional" {
		return fmt.Errorf("direction must be one of push|pull|bidirectional, got %q", j.Direction)
	}
	if j.Mode != "copy" && j.Mode != "sync" && j.Mode != "bisync" {
		return fmt.Errorf("mode must be one of copy|sync|bisync, got %q", j.Mode)
	}
	if (j.Mode == "copy" || j.Mode == "sync") && j.Direction == "bidirectional" {
		return fmt.Errorf("mode %s cannot use bidirectional direction", j.Mode)
	}
	if j.Mode == "sync" && j.Direction != "push" {
		return fmt.Errorf("destructive sync jobs require push direction")
	}
	if j.Mode == "sync" && (strings.TrimSpace(j.ModeGateRef) == "" || strings.TrimSpace(j.ModeGateDigest) == "") {
		return fmt.Errorf("sync mode requires mode_gate_ref and mode_gate_digest")
	}
	if j.Mode == "sync" {
		if !strings.HasPrefix(j.ModeGateRef, "drive-e2e:") {
			return fmt.Errorf("sync mode_gate_ref must use drive-e2e:<ref>")
		}
		if !isSHA256Digest(j.ModeGateDigest) {
			return fmt.Errorf("sync mode_gate_digest must use sha256:<64 hex chars>")
		}
	}
	if j.Mode == "bisync" && j.Direction != "bidirectional" {
		return fmt.Errorf("bisync requires bidirectional direction")
	}
	if j.Interval <= 0 {
		return fmt.Errorf("schedule must be > 0")
	}
	if j.CategoryPolicyID == "" || j.CategoryPolicyVersion <= 0 || j.CategoryPolicyDigest == "" {
		return fmt.Errorf("category policy id/version/digest are required")
	}
	if j.SymlinkPolicy != "preserve" && j.SymlinkPolicy != "follow" && j.SymlinkPolicy != "skip" {
		return fmt.Errorf("symlink_policy must be one of preserve|follow|skip, got %q", j.SymlinkPolicy)
	}
	if j.Schedule != "" && j.SymlinkPolicy == "follow" {
		return fmt.Errorf("scheduled jobs cannot use symlink_policy=follow")
	}
	if len(j.Destinations) == 0 {
		return fmt.Errorf("at least one destination is required")
	}
	seenDestinations := map[string]struct{}{}
	for i, destination := range j.Destinations {
		if err := destination.validate(); err != nil {
			return fmt.Errorf("destination %d: %w", i, err)
		}
		key := strings.ToLower(destination.Backend + "\x00" + destination.AccountID + "\x00" + destination.RootID + "\x00" + strings.Trim(destination.Path, "/"))
		if _, exists := seenDestinations[key]; exists {
			return fmt.Errorf("destination %d: canonical identity collides", i)
		}
		seenDestinations[key] = struct{}{}
	}
	return nil
}

func (d Destination) validate() error {
	if strings.TrimSpace(d.Backend) == "" || strings.TrimSpace(d.Path) == "" {
		return fmt.Errorf("backend and path are required")
	}
	if d.MinCompleteRestoreSets < 2 {
		return fmt.Errorf("min_complete_restore_sets must be >= 2")
	}
	if d.DeletePolicy != "none" && d.DeletePolicy != "quarantine" {
		return fmt.Errorf("delete_policy must be one of quarantine|none, got %q", d.DeletePolicy)
	}
	if d.AccountID == "" || d.RootID == "" {
		return fmt.Errorf("account_id and root_id are required")
	}
	return nil
}

func (d Destination) RcloneTarget() (string, error) {
	const prefix = "rclone:"
	if !strings.HasPrefix(d.CredentialRef, prefix) {
		return "", fmt.Errorf("destination credential_ref must use %q", prefix)
	}
	remote := strings.TrimPrefix(d.CredentialRef, prefix)
	if remote == "" {
		return "", fmt.Errorf("destination credential_ref remote is empty")
	}
	return remote + ":" + strings.TrimPrefix(d.Path, ":"), nil
}
