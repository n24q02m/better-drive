package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/config"
)

func TestConfigMigrateDryRunJSONRedactsUserPathAndDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	legacy := `rclone_config = "C:/Users/alice/AppData/rclone/rclone.conf"

[[pair]]
local = "C:/Users/alice/.claude"
remote = "gdrive:Backups/claude"
interval = "30s"
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", path)

	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"config", "migrate", "--dry-run", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config migrate --dry-run: %v; stderr=%s", err, errOut.String())
	}
	if strings.Contains(out.String(), "alice") {
		t.Fatalf("migration preview leaked a username: %s", out.String())
	}
	var got struct {
		SchemaVersion int      `json:"schema_version"`
		Blockers      []string `json:"blockers"`
		Jobs          []struct {
			ID        string `json:"id"`
			Direction string `json:"direction"`
			Mode      string `json:"mode"`
			Required  bool   `json:"required"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("preview JSON: %v; output=%s", err, out.String())
	}
	if got.SchemaVersion != 2 || len(got.Jobs) != 1 || got.Jobs[0].Direction != "push" || got.Jobs[0].Mode != "copy" || !got.Jobs[0].Required {
		t.Fatalf("preview = %#v, want normalized v2 copy/push/required job", got)
	}
	if len(got.Blockers) == 0 || !strings.Contains(strings.Join(got.Blockers, "\n"), "category policy") {
		t.Fatalf("blockers = %#v, want category-policy blocker", got.Blockers)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("dry-run migration modified the legacy config")
	}
}

func TestConfigMigrateCreateOnlyRequiresCompleteExplicitBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.toml")
	if err := os.WriteFile(path, []byte(`[[pair]]
local = "C:/source"
remote = "gdrive:Backups/source"
interval = "30s"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", path)
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"config", "migrate", "--create-only", "--output", filepath.Join(t.TempDir(), "migrated.toml")})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "complete") {
		t.Fatalf("create-only without bindings error = %v, want complete-binding rejection", err)
	}
}

func TestConfigMigrateCreateOnlyMissingCategoryPolicyBindingRefLeavesNoOutput(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, "legacy.toml")
	if err := os.WriteFile(legacyPath, []byte(fmt.Sprintf(`[[pair]]
local = %q
remote = "gdrive:Backups/source"
interval = "30s"
exclude = ["node_modules/"]
`, sourceDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "migrated.toml")
	t.Setenv("BETTER_DRIVE_CONFIG", legacyPath)
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"config", "migrate", "--create-only", "--output", outputPath,
		"--account-id", "account-1", "--root-id", "root-1",
		"--role-ref", "profile:home", "--role-digest", "sha256:" + strings.Repeat("d", 64),
		"--policy-ref", "policy:home", "--policy-digest", "sha256:" + strings.Repeat("e", 64),
		"--runtime-executable", filepath.Join(root, "rclone"), "--runtime-executable-file-id", "exe-id",
		"--runtime-executable-digest", "sha256:" + strings.Repeat("b", 64), "--runtime-version", "1.67.0",
		"--runtime-provenance", "release", "--runtime-signature", "sig", "--runtime-owner", "release-owner",
		"--runtime-acl", "owner-only", "--runtime-config", filepath.Join(root, "rclone.conf"), "--runtime-config-file-id", "cfg-id",
		"--runtime-config-digest", "sha256:" + strings.Repeat("c", 64),
		"--runtime-allowed-remote", "gdrive", "--runtime-allowed-backend", "drive",
		"--category-policy-id", "claude-state", "--category-policy-version", "7",
		"--category-policy-digest", "sha256:" + strings.Repeat("a", 64),
		"--category-policy-root", sourceDir, "--category-policy-deny", "node_modules/",
		"--category-policy-max-bytes", "1048576", "--category-policy-restore-expectation", "empty-or-exact-hash",
	})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "category_policy") {
		t.Fatalf("missing category-policy binding-ref error = %v, want category-policy rejection", err)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("missing category-policy binding-ref created output: stat error=%v", err)
	}
}

func TestConfigMigrateCreateOnlyRequiresRuntimeBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.toml")
	outputPath := filepath.Join(t.TempDir(), "migrated.toml")
	if err := os.WriteFile(path, []byte(`[[pair]]
local = "C:/source"
remote = "gdrive:Backups/source"
interval = "30s"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", path)
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"config", "migrate", "--create-only", "--output", outputPath,
		"--account-id", "account-1", "--root-id", "root-1",
		"--role-ref", "profile:home", "--role-digest", "sha256:" + strings.Repeat("d", 64),
		"--policy-ref", "policy:home", "--policy-digest", "sha256:" + strings.Repeat("e", 64),
	})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "rclone_runtime") {
		t.Fatalf("create-only without runtime bindings error = %v, want complete-runtime rejection", err)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("incomplete migration created output: stat error=%v", err)
	}
}

func TestConfigMigrateCreateOnlyWritesReloadableV2FromLegacyFixture(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "settings.json"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(root, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	executablePath := filepath.Join(runtimeDir, "rclone")
	configPath := filepath.Join(runtimeDir, "rclone.conf")
	if err := os.WriteFile(executablePath, []byte("rclone fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("[gdrive]\ntype = drive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rolePath := filepath.Join(root, "role.json")
	roleBody := `{"role":"home"}`
	if err := os.WriteFile(rolePath, []byte(roleBody), 0o600); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(root, "policy.json")
	policyBody := `{"policy":"home"}`
	if err := os.WriteFile(policyPath, []byte(policyBody), 0o600); err != nil {
		t.Fatal(err)
	}
	categoryPolicyPath := filepath.Join(root, "category-policy.json")
	categoryPolicyBody := `{"category":"home"}`
	if err := os.WriteFile(categoryPolicyPath, []byte(categoryPolicyBody), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, "legacy.toml")
	legacy := fmt.Sprintf(`rclone_config = %q

[[pair]]
local = %q
remote = "gdrive:Backups/source"
interval = "30s"
exclude = ["node_modules/"]
`, configPath, sourceDir)
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "migrated.toml")
	t.Setenv("BETTER_DRIVE_CONFIG", legacyPath)
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"config", "migrate", "--create-only", "--output", outputPath,
		"--account-id", "account-1", "--root-id", "root-1",
		"--role-ref", rolePath, "--role-digest", migrationDigest(roleBody),
		"--policy-ref", policyPath, "--policy-digest", migrationDigest(policyBody),
		"--runtime-executable", executablePath, "--runtime-executable-file-id", "exe-id",
		"--runtime-executable-digest", migrationDigest("rclone fixture"), "--runtime-version", "1.67.0",
		"--runtime-provenance", "release", "--runtime-signature", "sig", "--runtime-owner", "release-owner",
		"--runtime-acl", "owner-only", "--runtime-config", configPath, "--runtime-config-file-id", "cfg-id",
		"--runtime-config-digest", migrationDigest("[gdrive]\ntype = drive\n"),
		"--runtime-allowed-remote", "gdrive", "--runtime-allowed-backend", "gdrive",
		"--category-policy-id", "claude-state", "--category-policy-version", "7",
		"--category-policy-digest", migrationDigest(categoryPolicyBody),
		"--category-policy-binding-ref", categoryPolicyPath,
		"--category-policy-root", sourceDir, "--category-policy-deny", "node_modules/",
		"--category-policy-max-bytes", "1048576", "--category-policy-restore-expectation", "empty-or-exact-hash",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("create-only migration: %v", err)
	}
	got, err := config.Load(outputPath)
	if err != nil {
		t.Fatalf("reload migrated config: %v", err)
	}
	if err := got.ValidateForExecution(); err != nil {
		t.Fatalf("reloaded config is not execution-ready: %v", err)
	}
	if err := got.ValidateForExecutionWithBindings(config.FileBindingResolver{}, config.FileBindingResolver{}); err != nil {
		t.Fatalf("reloaded config binding readback: %v", err)
	}
	if got.SchemaVersion != config.CurrentSchemaVersion || got.RcloneRuntime.Version != "1.67.0" ||
		len(got.CategoryPolicies) != 1 || got.CategoryPolicies[0].Version != 7 ||
		got.Jobs[0].CategoryPolicyVersion != 7 || got.Jobs[0].Destinations[0].AccountID != "account-1" ||
		got.Jobs[0].Destinations[0].RootID != "root-1" {
		t.Fatalf("reloaded migrated config = %#v", got)
	}
}

func TestConfigMigrateCreateOnlyRejectsDriftWithoutOutput(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceConfig := filepath.Join(root, "source.conf")
	if err := os.WriteFile(sourceConfig, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	boundConfig := filepath.Join(root, "bound.conf")
	if err := os.WriteFile(boundConfig, []byte("bound"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, "legacy.toml")
	legacy := fmt.Sprintf(`rclone_config = %q

[[pair]]
local = %q
remote = "gdrive:Backups/source"
interval = "30s"
exclude = ["node_modules/"]
`, sourceConfig, sourceDir)
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "migrated.toml")
	t.Setenv("BETTER_DRIVE_CONFIG", legacyPath)
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"config", "migrate", "--create-only", "--output", outputPath,
		"--account-id", "account-1", "--root-id", "root-1",
		"--role-ref", filepath.Join(root, "role.json"), "--role-digest", "sha256:" + strings.Repeat("d", 64),
		"--policy-ref", filepath.Join(root, "policy.json"), "--policy-digest", "sha256:" + strings.Repeat("e", 64),
		"--runtime-executable", filepath.Join(root, "rclone"), "--runtime-executable-file-id", "exe-id",
		"--runtime-executable-digest", "sha256:" + strings.Repeat("b", 64), "--runtime-version", "1.67.0",
		"--runtime-provenance", "release", "--runtime-signature", "sig", "--runtime-owner", "release-owner",
		"--runtime-acl", "owner-only", "--runtime-config", boundConfig, "--runtime-config-file-id", "cfg-id",
		"--runtime-config-digest", "sha256:" + strings.Repeat("c", 64),
		"--runtime-allowed-remote", "gdrive", "--runtime-allowed-backend", "gdrive",
		"--category-policy-id", "claude-state", "--category-policy-version", "7",
		"--category-policy-digest", "sha256:" + strings.Repeat("a", 64),
		"--category-policy-binding-ref", filepath.Join(root, "category.json"),
		"--category-policy-root", sourceDir, "--category-policy-deny", "node_modules/",
		"--category-policy-max-bytes", "1048576", "--category-policy-restore-expectation", "empty-or-exact-hash",
	})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "rclone_runtime.config drift") {
		t.Fatalf("drift migration error = %v, want runtime-config drift", err)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("drift migration created output: stat error=%v", err)
	}
}

func migrationDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestConfigMigrateRequiresDryRunBeforeAnyWritePath(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"config", "migrate"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("config migrate without --dry-run succeeded; want explicit dry-run gate")
	}
}
