package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func migrationBindingsFixture(t *testing.T) MigrationBindings {
	t.Helper()
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	runtimeRoot := filepath.Join(root, "runtime")
	return MigrationBindings{
		AccountID:    "account-1",
		RootID:       "root-1",
		RoleRef:      "role:home",
		RoleDigest:   "sha256:" + strings.Repeat("d", 64),
		PolicyRef:    "policy:home",
		PolicyDigest: "sha256:" + strings.Repeat("e", 64),
		RcloneRuntime: RcloneRuntime{
			Executable:       filepath.Join(runtimeRoot, "rclone"),
			ExecutableFileID: "exe-id",
			ExecutableDigest: "sha256:" + strings.Repeat("b", 64),
			Version:          "1.67.0",
			Provenance:       "release",
			Signature:        "signature",
			Owner:            "release-owner",
			ACL:              "owner-only",
			Config:           filepath.Join(runtimeRoot, "rclone.conf"),
			ConfigFileID:     "config-id",
			ConfigDigest:     "sha256:" + strings.Repeat("c", 64),
			AllowedRemotes:   []string{"gdrive"},
			AllowedBackends:  []string{"drive"},
		},
		CategoryPolicy: CategoryPolicy{
			ID:                 "claude-state",
			Version:            7,
			Digest:             "sha256:" + strings.Repeat("a", 64),
			BindingRef:         "policy:claude-state",
			AllowlistedRoot:    sourceRoot,
			MandatoryDenylist:  []string{"node_modules/"},
			SizeGuard:          CategorySizeGuard{MaxBytes: 1 << 20},
			RestoreExpectation: "empty-or-exact-hash",
		},
	}
}

func migrationLegacyConfig(bindings MigrationBindings) *Config {
	return &Config{
		SchemaVersion: CurrentSchemaVersion,
		RcloneRuntime: RcloneRuntime{Config: bindings.RcloneRuntime.Config},
		Jobs: []Job{{
			ID: "legacy-job", Source: bindings.CategoryPolicy.AllowlistedRoot,
			Direction: "push", Mode: "copy", Required: true,
			SymlinkPolicy: "preserve", Schedule: "30s",
			Destinations: []Destination{{
				Backend: "drive", Path: "Backups/legacy", CredentialRef: "rclone:gdrive",
				Required: true, MinCompleteRestoreSets: 2, DeletePolicy: "none",
			}},
		}},
	}
}

func TestApplyMigrationBindingsFillsLegacyRuntimeAndPolicyWithSuppliedVersion(t *testing.T) {
	bindings := migrationBindingsFixture(t)
	source := migrationLegacyConfig(bindings)
	got, err := ApplyMigrationBindings(source, bindings)
	if err != nil {
		t.Fatalf("ApplyMigrationBindings: %v", err)
	}
	if !reflect.DeepEqual(got.RcloneRuntime, bindings.RcloneRuntime) {
		t.Fatalf("runtime = %#v, want %#v", got.RcloneRuntime, bindings.RcloneRuntime)
	}
	if len(got.CategoryPolicies) != 1 || got.CategoryPolicies[0].Version != bindings.CategoryPolicy.Version ||
		got.CategoryPolicies[0].BindingRef != bindings.CategoryPolicy.BindingRef {
		t.Fatalf("category policies = %#v, want supplied version/ref %d/%q", got.CategoryPolicies, bindings.CategoryPolicy.Version, bindings.CategoryPolicy.BindingRef)
	}
	job := got.Jobs[0]
	if job.CategoryPolicyID != bindings.CategoryPolicy.ID || job.CategoryPolicyVersion != bindings.CategoryPolicy.Version || job.CategoryPolicyDigest != bindings.CategoryPolicy.Digest {
		t.Fatalf("job policy binding = %#v, want supplied policy", job)
	}
	if destination := job.Destinations[0]; destination.AccountID != bindings.AccountID || destination.RootID != bindings.RootID {
		t.Fatalf("destination identity = %#v, want account/root bindings", destination)
	}
	if source.RcloneRuntime.Executable != "" || len(source.CategoryPolicies) != 0 || source.Jobs[0].CategoryPolicyID != "" {
		t.Fatal("ApplyMigrationBindings mutated legacy source")
	}
}

func TestPreviewIncludesRedactedCategoryPolicyBindingInputs(t *testing.T) {
	bindings := migrationBindingsFixture(t)
	policy := bindings.CategoryPolicy
	policy.AllowlistedRoot = `C:/Users/alice/.config`
	policy.BindingRef = `file:C:/Users/alice/policy.json`
	cfg := migrationLegacyConfig(bindings)
	cfg.CategoryPolicies = []CategoryPolicy{policy}

	preview := Preview(cfg)
	if len(preview.CategoryPolicies) != 1 {
		t.Fatalf("category policy preview = %#v, want one policy", preview.CategoryPolicies)
	}
	got := preview.CategoryPolicies[0]
	if got.ID != policy.ID || got.Version != policy.Version || got.Digest != policy.Digest ||
		got.MandatoryDenylist[0] != policy.MandatoryDenylist[0] ||
		got.MaxBytes != policy.SizeGuard.MaxBytes || got.RestoreExpectation != policy.RestoreExpectation {
		t.Fatalf("category policy preview = %#v, want canonical policy inputs", got)
	}
	if strings.Contains(got.AllowlistedRoot, "alice") || strings.Contains(got.BindingRef, "alice") {
		t.Fatalf("category policy preview leaked user path: %#v", got)
	}
	if !strings.Contains(got.AllowlistedRoot, "<user>") || !strings.Contains(got.BindingRef, "<user>") {
		t.Fatalf("category policy preview did not redact user path: %#v", got)
	}
}

func TestApplyMigrationBindingsRejectsRuntimePolicyJobAndDestinationDrift(t *testing.T) {
	bindings := migrationBindingsFixture(t)
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name:   "runtime",
			mutate: func(cfg *Config) { cfg.RcloneRuntime.Version = "1.68.0" },
			want:   "rclone_runtime.version drift",
		},
		{
			name: "category policy binding ref",
			mutate: func(cfg *Config) {
				cfg.CategoryPolicies = []CategoryPolicy{{BindingRef: "policy:foreign"}}
			},
			want: "category_policy.binding_ref drift",
		},
		{
			name:   "policy",
			mutate: func(cfg *Config) { cfg.CategoryPolicies = []CategoryPolicy{{ID: "foreign"}} },
			want:   "category_policy.id drift",
		},
		{
			name:   "job",
			mutate: func(cfg *Config) { cfg.Jobs[0].CategoryPolicyVersion = 9 },
			want:   "category policy version drift",
		},
		{
			name:   "destination",
			mutate: func(cfg *Config) { cfg.Jobs[0].Destinations[0].RootID = "foreign-root" },
			want:   "foreign root_id",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := migrationLegacyConfig(bindings)
			test.mutate(source)
			if _, err := ApplyMigrationBindings(source, bindings); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("drift error = %v, want %q", err, test.want)
			}
		})
	}
}
