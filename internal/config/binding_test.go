package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type bindingReadbackResolver struct {
	role        BindingReadback
	policy      BindingReadback
	categoryRef string
	category    BindingReadback
	err         error
}

func (r bindingReadbackResolver) ReadRoleBinding(string) (BindingReadback, error) {
	return r.role, r.err
}

func (r bindingReadbackResolver) ReadPolicyBinding(ref string) (BindingReadback, error) {
	if r.categoryRef != "" && ref == r.categoryRef {
		return r.category, r.err
	}
	return r.policy, r.err
}

func bindingFile(t *testing.T, name, body string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return "file:" + path, "sha256:" + hex.EncodeToString(sum[:])
}

func testRoleBinding(t *testing.T) RoleBinding {
	roleRef, roleDigest := bindingFile(t, "role.json", `{"role":"home"}`)
	policyRef, policyDigest := bindingFile(t, "policy.json", `{"policy":"home"}`)
	return RoleBinding{
		RoleRef:      roleRef,
		RoleDigest:   roleDigest,
		PolicyRef:    policyRef,
		PolicyDigest: policyDigest,
	}
}

func TestWriteCanonicalV2CreateOnlyRoundTripsExplicitBooleansAndIntegers(t *testing.T) {
	cfg := validV2Config(t)
	cfg.RoleBinding = testRoleBinding(t)
	cfg.Jobs[0].Required = false
	cfg.Jobs[0].CategoryPolicyVersion = 3
	cfg.CategoryPolicies[0].Version = 3
	cfg.Jobs[0].Destinations[0].Required = false
	cfg.Jobs[0].Destinations[0].MinCompleteRestoreSets = 7
	path := filepath.Join(t.TempDir(), "canonical.toml")

	if err := WriteCanonicalV2CreateOnly(path, cfg); err != nil {
		t.Fatalf("WriteCanonicalV2CreateOnly: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load canonical config: %v", err)
	}
	if got.SchemaVersion != CurrentSchemaVersion || got.RoleBinding != cfg.RoleBinding {
		t.Fatalf("identity = %#v, want %#v", got.RoleBinding, cfg.RoleBinding)
	}
	if len(got.CategoryPolicies) != 1 || got.CategoryPolicies[0].BindingRef != cfg.CategoryPolicies[0].BindingRef {
		t.Fatalf("category policy binding ref = %#v, want %#v", got.CategoryPolicies, cfg.CategoryPolicies)
	}
	job := got.Jobs[0]
	if job.Required || job.CategoryPolicyVersion != 3 || job.Destinations[0].Required || job.Destinations[0].MinCompleteRestoreSets != 7 {
		t.Fatalf("round-trip safety fields = %#v, want explicit false/3/false/7", job)
	}
}

func TestWriteCanonicalV2CreateOnlyRefusesExistingTarget(t *testing.T) {
	cfg := validV2Config(t)
	cfg.RoleBinding = testRoleBinding(t)
	path := filepath.Join(t.TempDir(), "existing.toml")
	if err := os.WriteFile(path, []byte("foreign target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteCanonicalV2CreateOnly(path, cfg); err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("write existing target error = %v, want os.ErrExist", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "foreign target" {
		t.Fatalf("existing target changed to %q", body)
	}
}

func TestWriteCanonicalV2CreateOnlyRejectsMissingIdentityAndUntypedCredential(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *Config)
		want   string
	}{
		{name: "role binding", mutate: func(_ *testing.T, cfg *Config) { cfg.RoleBinding = RoleBinding{} }, want: "role_ref"},
		{name: "account", mutate: func(t *testing.T, cfg *Config) {
			cfg.RoleBinding = testRoleBinding(t)
			cfg.Jobs[0].Destinations[0].AccountID = ""
		}, want: "account_id"},
		{name: "root", mutate: func(t *testing.T, cfg *Config) {
			cfg.RoleBinding = testRoleBinding(t)
			cfg.Jobs[0].Destinations[0].RootID = ""
		}, want: "root_id"},
		{name: "credential", mutate: func(t *testing.T, cfg *Config) {
			cfg.RoleBinding = testRoleBinding(t)
			cfg.Jobs[0].Destinations[0].CredentialRef = "gdrive"
		}, want: "credential_ref"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validV2Config(t)
			tc.mutate(t, cfg)
			if err := WriteCanonicalV2CreateOnly(filepath.Join(t.TempDir(), "config.toml"), cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("write error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateForExecutionWithBindingsRejectsFreshRoleOrPolicyDrift(t *testing.T) {
	cfg := validV2Config(t)
	cfg.RoleBinding = testRoleBinding(t)
	resolver := bindingReadbackResolver{
		role:        BindingReadback{Ref: cfg.RoleBinding.RoleRef, Digest: "sha256:" + strings.Repeat("f", 64)},
		policy:      BindingReadback{Ref: cfg.RoleBinding.PolicyRef, Digest: cfg.RoleBinding.PolicyDigest},
		categoryRef: cfg.CategoryPolicies[0].BindingRef,
		category:    BindingReadback{Ref: cfg.CategoryPolicies[0].BindingRef, Digest: cfg.CategoryPolicies[0].Digest},
	}
	if err := cfg.ValidateForExecutionWithBindings(resolver, resolver); err == nil || !strings.Contains(err.Error(), "role binding") {
		t.Fatalf("role drift error = %v, want role binding drift", err)
	}
	resolver.role = BindingReadback{Ref: cfg.RoleBinding.RoleRef, Digest: cfg.RoleBinding.RoleDigest}
	resolver.policy = BindingReadback{Ref: cfg.RoleBinding.PolicyRef, Digest: "sha256:" + strings.Repeat("a", 64)}
	if err := cfg.ValidateForExecutionWithBindings(resolver, resolver); err == nil || !strings.Contains(err.Error(), "policy binding") {
		t.Fatalf("policy drift error = %v, want policy binding drift", err)
	}
}

func TestValidateForExecutionWithBindingsAcceptsFreshCategoryPolicyReadback(t *testing.T) {
	cfg := validV2Config(t)
	cfg.RoleBinding = testRoleBinding(t)
	categoryRef, categoryDigest := bindingFile(t, "category.json", `{"category":"home"}`)
	cfg.CategoryPolicies[0].BindingRef = categoryRef
	cfg.CategoryPolicies[0].Digest = categoryDigest
	cfg.Jobs[0].CategoryPolicyDigest = categoryDigest
	resolver := FileBindingResolver{}
	if err := cfg.ValidateForExecutionWithBindings(resolver, resolver); err != nil {
		t.Fatalf("fresh category policy readback: %v", err)
	}
}

func TestValidateForExecutionWithBindingsRejectsCategoryPolicyFileDrift(t *testing.T) {
	cfg := validV2Config(t)
	cfg.RoleBinding = testRoleBinding(t)
	categoryRef, categoryDigest := bindingFile(t, "category.json", `{"category":"home"}`)
	cfg.CategoryPolicies[0].BindingRef = categoryRef
	cfg.CategoryPolicies[0].Digest = categoryDigest
	cfg.Jobs[0].CategoryPolicyDigest = categoryDigest
	resolver := FileBindingResolver{}
	if err := os.WriteFile(strings.TrimPrefix(categoryRef, "file:"), []byte(`{"category":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateForExecutionWithBindings(resolver, resolver); err == nil || !strings.Contains(err.Error(), "category policy binding") {
		t.Fatalf("category file drift error = %v, want category policy binding drift", err)
	}
}

func TestValidateForExecutionWithBindingsRejectsCategoryPolicyWrongReadbackRef(t *testing.T) {
	cfg := validV2Config(t)
	cfg.RoleBinding = testRoleBinding(t)
	resolver := bindingReadbackResolver{
		role:        BindingReadback{Ref: cfg.RoleBinding.RoleRef, Digest: cfg.RoleBinding.RoleDigest},
		policy:      BindingReadback{Ref: cfg.RoleBinding.PolicyRef, Digest: cfg.RoleBinding.PolicyDigest},
		categoryRef: cfg.CategoryPolicies[0].BindingRef,
		category:    BindingReadback{Ref: "policy:wrong", Digest: cfg.CategoryPolicies[0].Digest},
	}
	if err := cfg.ValidateForExecutionWithBindings(resolver, resolver); err == nil || !strings.Contains(err.Error(), "category policy binding") {
		t.Fatalf("category wrong-ref error = %v, want category policy binding drift", err)
	}
}

func TestValidateForExecutionWithBindingsRejectsSharedCategoryPolicyRefDigestConflict(t *testing.T) {
	cfg := validV2Config(t)
	cfg.RoleBinding = testRoleBinding(t)
	duplicate := cfg.CategoryPolicies[0]
	duplicate.ID = "other-policy"
	duplicate.Digest = "sha256:" + strings.Repeat("f", 64)
	cfg.CategoryPolicies = append(cfg.CategoryPolicies, duplicate)
	resolver := bindingReadbackResolver{
		role:        BindingReadback{Ref: cfg.RoleBinding.RoleRef, Digest: cfg.RoleBinding.RoleDigest},
		policy:      BindingReadback{Ref: cfg.RoleBinding.PolicyRef, Digest: cfg.RoleBinding.PolicyDigest},
		categoryRef: cfg.CategoryPolicies[0].BindingRef,
		category:    BindingReadback{Ref: cfg.CategoryPolicies[0].BindingRef, Digest: cfg.CategoryPolicies[0].Digest},
	}
	if err := cfg.ValidateForExecutionWithBindings(resolver, resolver); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("shared category policy ref error = %v, want inconsistent digest rejection", err)
	}
}

func TestValidateForExecutionWithBindingsRequiresFreshResolvers(t *testing.T) {
	cfg := validV2Config(t)
	cfg.RoleBinding = testRoleBinding(t)
	if err := cfg.ValidateForExecutionWithBindings(nil, nil); err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("missing resolver error = %v, want resolver rejection", err)
	}
	resolver := bindingReadbackResolver{err: errors.New("stale")}
	if err := cfg.ValidateForExecutionWithBindings(resolver, resolver); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("resolver readback error = %v, want stale readback", err)
	}
}
func TestFileBindingResolverReadsBoundedFileDigestAndDetectsDrift(t *testing.T) {
	ref, digest := bindingFile(t, "role.json", `{"role":"home"}`)
	resolver := FileBindingResolver{}
	readback, err := resolver.ReadRoleBinding(ref)
	if err != nil {
		t.Fatalf("ReadRoleBinding: %v", err)
	}
	if readback.Ref != ref || readback.Digest != digest {
		t.Fatalf("readback = %#v, want ref=%q digest=%q", readback, ref, digest)
	}

	cfg := validV2Config(t)
	policyRef, policyDigest := bindingFile(t, "policy.json", `{"policy":"home"}`)
	cfg.RoleBinding = RoleBinding{RoleRef: ref, RoleDigest: digest, PolicyRef: policyRef, PolicyDigest: policyDigest}
	if err := os.WriteFile(strings.TrimPrefix(ref, "file:"), []byte(`{"role":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateForExecutionWithBindings(resolver, resolver); err == nil || !strings.Contains(err.Error(), "role binding") {
		t.Fatalf("ValidateForExecutionWithBindings error = %v, want role drift", err)
	}
}

func TestFileBindingResolverRejectsUnsupportedSymlinkAndOversizedRefs(t *testing.T) {
	resolver := FileBindingResolver{MaxBytes: 4}
	if _, err := resolver.ReadRoleBinding("profile:home"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported ref error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "large")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ReadRoleBinding(path); err == nil || !strings.Contains(err.Error(), "bounded") {
		t.Fatalf("oversized ref error = %v", err)
	}
}

func TestWriteCanonicalV2CreateOnlyBlocksUnreadyConfigBeforeCreating(t *testing.T) {
	cfg := validV2Config(t)
	cfg.RoleBinding = testRoleBinding(t)
	cfg.CategoryPolicies = nil
	path := filepath.Join(t.TempDir(), "blocked.toml")
	if err := WriteCanonicalV2CreateOnly(path, cfg); err == nil || !strings.Contains(err.Error(), "category policy registry") {
		t.Fatalf("write error = %v, want category-policy blocker", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked writer created %q: stat error=%v", path, err)
	}
}

func TestLegacySevenPairMigrationKeepsRequiredRestoreFloorWithExplicitBindings(t *testing.T) {
	body := "rclone_config = \"C:/tools/rclone.conf\"\n"
	for i := 0; i < 7; i++ {
		body += "[[pair]]\nlocal = \"C:/source/" + string(rune('a'+i)) + "\"\nremote = \"gdrive:Backups/" + string(rune('a'+i)) + "\"\ninterval = \"30s\"\n"
	}
	cfg, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("Load legacy pairs: %v", err)
	}
	cfg.RoleBinding = testRoleBinding(t)
	if len(cfg.Jobs) != 7 {
		t.Fatalf("jobs = %d, want 7", len(cfg.Jobs))
	}
	for _, job := range cfg.Jobs {
		if !job.Required || len(job.Destinations) != 1 || !job.Destinations[0].Required || job.Destinations[0].MinCompleteRestoreSets != 2 {
			t.Fatalf("legacy job %q = %#v, want required floor 2", job.ID, job)
		}
	}
}
