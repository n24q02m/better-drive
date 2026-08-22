package paths

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestConfigFileHonorsEnvironmentOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("BETTER_DRIVE_CONFIG", want)
	if got := ConfigFile(); got != want {
		t.Fatalf("ConfigFile() = %q, want override %q", got, want)
	}
}

func TestDefaultPathsUseBetterDriveDirectory(t *testing.T) {
	t.Setenv("BETTER_DRIVE_CONFIG", "")
	for name, got := range map[string]string{
		"config":  ConfigFile(),
		"workdir": Workdir(),
		"log":     LogFile(),
	} {
		if !strings.Contains(got, filepath.Join("better-drive")) {
			t.Errorf("%s path %q does not use the better-drive directory", name, got)
		}
	}
	if !strings.HasSuffix(ConfigFile(), filepath.Join("better-drive", "config.toml")) {
		t.Errorf("ConfigFile() = %q, want better-drive\\config.toml suffix", ConfigFile())
	}
	if !strings.HasSuffix(LogFile(), filepath.Join("better-drive", "better-drive.log")) {
		t.Errorf("LogFile() = %q, want better-drive\\better-drive.log suffix", LogFile())
	}
}

func TestJobWorkdirUsesStableJobID(t *testing.T) {
	first := JobWorkdir("home-claude")
	second := JobWorkdir("home-claude")
	if first != second {
		t.Fatalf("JobWorkdir is not stable: %q != %q", first, second)
	}
	if filepath.Dir(first) != Workdir() {
		t.Fatalf("JobWorkdir parent = %q, want %q", filepath.Dir(first), Workdir())
	}
	if matched, _ := regexp.MatchString(`^job-[0-9a-f]{8}$`, filepath.Base(first)); !matched {
		t.Fatalf("JobWorkdir name = %q, want job-<8 hex> identity hash", filepath.Base(first))
	}
}

func TestJobWorkdirSeparatesDistinctStableIDs(t *testing.T) {
	if got, other := JobWorkdir("home-claude"), JobWorkdir("home-codex"); got == other {
		t.Fatalf("distinct job IDs share workdir %q", got)
	}
}
