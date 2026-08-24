package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadV2PreservesJobAndDestinationContract(t *testing.T) {
	rclone := filepath.ToSlash(filepath.Join(t.TempDir(), "rclone.exe"))
	rcloneConfig := filepath.ToSlash(filepath.Join(t.TempDir(), "rclone.conf"))
	path := writeTemp(t, `schema_version = 2

[rclone_runtime]
executable = "`+rclone+`"
executable_file_id = "file-1"
executable_digest = "sha256:exe"
version = "1.67.0"
provenance = "managed-release"
signature = "sig-1"
owner = "role-home"
acl = "owner-only"
config = "`+rcloneConfig+`"
config_file_id = "file-2"
config_digest = "sha256:cfg"
allowed_remotes = ["gdrive"]
allowed_backends = ["drive"]

[[job]]
id = "home-claude"
source = "C:/Users/me/.claude"
direction = "push"
mode = "copy"
required = true
category_policy_id = "claude-state"
category_policy_version = 1
category_policy_digest = "sha256:policy"
symlink_policy = "preserve"
schedule = "6h"
exclude = ["node_modules/"]

[[job.destination]]
backend = "drive"
path = "Backups/home/Claude"
account_id = "account-1"
root_id = "root-1"
credential_ref = "rclone:gdrive"
required = true
retention = "30d"
min_complete_restore_sets = 2
delete_policy = "none"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SchemaVersion != 2 {
		t.Fatalf("schema version = %d, want 2", cfg.SchemaVersion)
	}
	if len(cfg.Jobs) != 1 || cfg.Jobs[0].ID != "home-claude" {
		t.Fatalf("jobs = %#v, want one home-claude job", cfg.Jobs)
	}
	job := cfg.Jobs[0]
	if job.Source != "C:/Users/me/.claude" || job.Direction != "push" || job.Mode != "copy" || !job.Required {
		t.Fatalf("job = %#v, missing explicit v2 fields", job)
	}
	if job.Interval != 6*time.Hour || job.SymlinkPolicy != "preserve" || len(job.Exclude) != 1 {
		t.Fatalf("job schedule/policy = %#v", job)
	}
	if len(job.Destinations) != 1 {
		t.Fatalf("destinations = %#v, want one", job.Destinations)
	}
	dst := job.Destinations[0]
	if dst.Backend != "drive" || dst.Path != "Backups/home/Claude" || dst.AccountID != "account-1" || dst.RootID != "root-1" || dst.CredentialRef != "rclone:gdrive" {
		t.Fatalf("destination identity = %#v", dst)
	}
	if !dst.Required || dst.MinCompleteRestoreSets != 2 || dst.DeletePolicy != "none" {
		t.Fatalf("destination safety fields = %#v", dst)
	}
	if cfg.RcloneRuntime.Executable != rclone || cfg.RcloneRuntime.Config != rcloneConfig {
		t.Fatalf("runtime paths = %#v", cfg.RcloneRuntime)
	}
}

func TestLoadV2PreservesWorkstationCategoryPolicyBindings(t *testing.T) {
	body := "schema_version = 2\n" +
		workstationJobTOML("vscode-user", "C:/Users/me/AppData/Roaming/Code - Insiders/User", "Backups/home/VSCode-Insiders/User", "vscode-insiders-user") +
		workstationJobTOML("vscode-extensions", "C:/Users/me/.vscode-insiders/extensions", "Backups/home/VSCode-Insiders/Extensions", "vscode-insiders-extensions") +
		workstationJobTOML("curseforge", "C:/Users/me/AppData/Roaming/CurseForge", "Backups/home/CurseForge", "curseforge-instances")
	cfg, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Jobs) != 3 {
		t.Fatalf("jobs = %d, want 3", len(cfg.Jobs))
	}
	for _, job := range cfg.Jobs {
		if job.CategoryPolicyID == "" || job.CategoryPolicyVersion != 1 || len(job.CategoryPolicyDigest) != len("sha256:")+64 {
			t.Fatalf("job %q missing policy binding: %#v", job.ID, job)
		}
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestLoadV2PreservesCategoryPolicyRegistry(t *testing.T) {
	body := `schema_version = 2

[[category_policy]]
id = "vscode-insiders-user"
version = 1
digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
allowlisted_root = "C:/Users/me/AppData/Roaming/Code - Insiders/User"
mandatory_denylist = ["Cache/", "logs/"]
restore_expectation = "empty-or-exact-hash"

[category_policy.size_guard]
max_bytes = 1073741824

` + workstationJobTOML("vscode-user", "C:/Users/me/AppData/Roaming/Code - Insiders/User", "Backups/home/VSCode-Insiders/User", "vscode-insiders-user")
	cfg, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.CategoryPolicies) != 1 {
		t.Fatalf("category policies = %d, want 1", len(cfg.CategoryPolicies))
	}
	if err := cfg.CategoryPolicies[0].Validate(); err != nil {
		t.Fatalf("CategoryPolicy.Validate: %v", err)
	}
}

func TestCategoryPolicyRejectsMissingRequiredFields(t *testing.T) {
	base := CategoryPolicy{
		ID: "policy", Version: 1, Digest: "sha256:" + strings.Repeat("a", 64),
		AllowlistedRoot: "C:/Users/me/source", MandatoryDenylist: []string{"Cache/"},
		SizeGuard: CategorySizeGuard{MaxBytes: 1024}, RestoreExpectation: "empty-or-exact-hash",
	}
	tests := []struct {
		name   string
		mutate func(*CategoryPolicy)
		want   string
	}{
		{name: "digest", mutate: func(policy *CategoryPolicy) { policy.Digest = "sha256:bad" }, want: "digest"},
		{name: "allowlisted root", mutate: func(policy *CategoryPolicy) { policy.AllowlistedRoot = "" }, want: "allowlisted_root"},
		{name: "denylist", mutate: func(policy *CategoryPolicy) { policy.MandatoryDenylist = nil }, want: "mandatory_denylist"},
		{name: "size guard", mutate: func(policy *CategoryPolicy) { policy.SizeGuard.MaxBytes = 0 }, want: "size_guard.max_bytes"},
		{name: "restore expectation", mutate: func(policy *CategoryPolicy) { policy.RestoreExpectation = "" }, want: "restore_expectation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := base
			test.mutate(&policy)
			if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CategoryPolicy.Validate error = %v, want %q", err, test.want)
			}
		})
	}
}

func workstationJobTOML(id, source, destination, policyID string) string {
	return fmt.Sprintf(`[[job]]
id = %q
source = %q
direction = "push"
mode = "copy"
required = true
category_policy_id = %q
category_policy_version = 1
category_policy_digest = "sha256:%s"
symlink_policy = "preserve"
schedule = "6h"

[[job.destination]]
backend = "drive"
path = %q
account_id = "home"
root_id = "drive-root"
credential_ref = "rclone:gdrive"
required = true
min_complete_restore_sets = 2
delete_policy = "none"

`, id, source, policyID, strings.Repeat("a", 64), destination)
}

func TestLoadLegacyMissingModeMigratesToSafeCopyPush(t *testing.T) {
	path := writeTemp(t, `[[pair]]
local = "C:/Users/me/.claude"
remote = "gdrive:Backups/claude"
interval = "30s"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SchemaVersion != 2 || len(cfg.Jobs) != 1 {
		t.Fatalf("migrated config = %#v", cfg)
	}
	job := cfg.Jobs[0]
	if job.ID == "" || !strings.HasPrefix(job.ID, "legacy-") {
		t.Fatalf("legacy job id = %q, want deterministic legacy- id", job.ID)
	}
	if job.Mode != "copy" || job.Direction != "push" || !job.Required {
		t.Fatalf("legacy migration = %#v, want copy/push/required", job)
	}
	if len(job.Destinations) != 1 || !job.Destinations[0].Required || job.Destinations[0].MinCompleteRestoreSets != 2 {
		t.Fatalf("legacy destination = %#v", job.Destinations)
	}
}

func TestLoadLegacyConfigFailsExecutionValidationWithoutCategoryPolicy(t *testing.T) {
	path := writeTemp(t, `[[pair]]
local = "C:/Users/me/.vscode-insiders/User"
remote = "gdrive:Backups/home/VSCode-Insiders/User"
interval = "30s"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "category policy") {
		t.Fatalf("Validate error = %v, want category-policy fail-closed result", err)
	}
}

func TestLoadLegacyModeTable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      string
		wantMode  string
		wantDir   string
		wantError string
	}{
		{name: "default", mode: "default", wantMode: "copy", wantDir: "push"},
		{name: "copy", mode: "copy", wantMode: "copy", wantDir: "push"},
		{name: "sync", mode: "sync", wantMode: "sync", wantDir: "push"},
		{name: "bisync requires enrollment", mode: "bisync", wantError: "enrollment"},
		{name: "unknown", mode: "mirror", wantError: "unsupported legacy mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, `[[pair]]
local = "C:/Users/me/source"
remote = "gdrive:Backups/source"
interval = "30s"
mode = "`+tc.mode+`"
`)
			cfg, err := Load(path)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("Load error = %v, want substring %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			job := cfg.Jobs[0]
			if job.Mode != tc.wantMode || job.Direction != tc.wantDir {
				t.Fatalf("migration = mode %q direction %q, want %q/%q", job.Mode, job.Direction, tc.wantMode, tc.wantDir)
			}
		})
	}
}

func TestLoadLegacyBisyncAcceptsOnlyExplicitStableEnrollment(t *testing.T) {
	path := writeTemp(t, `[[pair]]
local = "C:/Users/me/source"
remote = "gdrive:Backups/source"
interval = "30s"
mode = "bisync"
`)
	jobID := LegacyJobID("C:/Users/me/source", "gdrive:Backups/source")
	cfg, err := LoadWithOptions(path, LoadOptions{EnrolledBidirectionalJobIDs: map[string]bool{jobID: true}})
	if err != nil {
		t.Fatalf("LoadWithOptions: %v", err)
	}
	if got := cfg.Jobs[0]; got.Mode != "bisync" || got.Direction != "bidirectional" || got.ID != jobID {
		t.Fatalf("enrolled migration = %#v", got)
	}
}

func TestValidateRejectsInvalidDestinationRestoreFloorAndDeletePolicy(t *testing.T) {
	cfg := &Config{
		SchemaVersion: 2,
		Jobs: []Job{{
			ID:                    "job-1",
			Source:                "C:/source",
			Direction:             "push",
			Mode:                  "copy",
			Required:              true,
			CategoryPolicyID:      "policy",
			CategoryPolicyVersion: 1,
			CategoryPolicyDigest:  "sha256:policy",
			SymlinkPolicy:         "preserve",
			Interval:              time.Hour,
			Destinations:          []Destination{{Backend: "drive", Path: "Backups/job-1", Required: true, MinCompleteRestoreSets: 1, DeletePolicy: "permanent-delete"}},
		}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "min_complete_restore_sets") {
		t.Fatalf("Validate error = %v, want restore-floor rejection before delete-policy validation", err)
	}
}

func TestRcloneRuntimeRejectsAmbientDiscoveryAndUnpinnedIdentity(t *testing.T) {
	runtime := RcloneRuntime{
		Executable:  "rclone",
		Config:      "",
		Environment: map[string]string{"PATH": "C:/tools", "RCLONE_CONFIG": "C:/ambient.conf"},
		Hooks:       []string{"post-sync"},
	}
	if err := runtime.Validate(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("Validate error = %v, want pinned absolute runtime rejection", err)
	}
}

func TestValidateForExecutionRejectsDestinationOutsideRuntimeAllowlist(t *testing.T) {
	cfg := validV2Config(t)
	cfg.RcloneRuntime.AllowedRemotes = []string{"other"}
	if err := cfg.ValidateForExecution(); err == nil || !strings.Contains(err.Error(), "allowed_remotes") {
		t.Fatalf("ValidateForExecution error = %v, want remote allowlist rejection", err)
	}
}

func TestValidateRequiresRealDriveGateForSyncMode(t *testing.T) {
	cfg := validV2Config(t)
	cfg.Jobs[0].Mode = "sync"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mode_gate") {
		t.Fatalf("Validate error = %v, want sync gate rejection", err)
	}
	cfg.Jobs[0].ModeGateRef = "drive-e2e:sync-gate"
	cfg.Jobs[0].ModeGateDigest = "sha256:" + strings.Repeat("0", 64)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate with gate: %v", err)
	}
}

func TestValidateRejectsUnboundSyncGateReference(t *testing.T) {
	cfg := validV2Config(t)
	cfg.Jobs[0].Mode = "sync"
	cfg.Jobs[0].ModeGateRef = "file:arbitrary"
	cfg.Jobs[0].ModeGateDigest = "sha256:" + strings.Repeat("0", 64)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "drive-e2e") {
		t.Fatalf("Validate error = %v, want drive-e2e gate reference rejection", err)
	}
}

func TestValidateRejectsScheduledFollowSymlinkPolicy(t *testing.T) {
	cfg := validV2Config(t)
	cfg.Jobs[0].SymlinkPolicy = "follow"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "scheduled") {
		t.Fatalf("Validate error = %v, want scheduled follow rejection", err)
	}
}
