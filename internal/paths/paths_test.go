package paths

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPairWorkdirIsStableForTheSamePair verifies the same (local, remote) pair
// always maps to the same directory, and that the directory sits directly
// under the single top-level Workdir(). Stability is the entire point of
// keying on the pair's identity rather than on its position in the config's
// [[pair]] list: reordering, inserting or deleting a block must not silently
// repoint a surviving pair at a different pair's bisync baseline.
func TestPairWorkdirIsStableForTheSamePair(t *testing.T) {
	first := PairWorkdir("C:/Users/me/Documents", "gdrive:Documents")
	second := PairWorkdir("C:/Users/me/Documents", "gdrive:Documents")
	if first != second {
		t.Errorf("PairWorkdir is not stable for one pair: %q vs %q", first, second)
	}
	if got := filepath.Dir(first); got != Workdir() {
		t.Errorf("parent of %q = %q, want the top-level workdir %q", first, got, Workdir())
	}
}

// TestPairWorkdirDiffersPerPair verifies two distinct pairs never share a
// workdir - the invariant per-pair workdirs exist for at all: bisync keeps its
// baseline listing files (*.lst) and a filters.txt in the workdir, and two
// pairs sharing one directory would corrupt each other's baseline.
func TestPairWorkdirDiffersPerPair(t *testing.T) {
	reference := PairWorkdir("C:/a", "gdrive:a")
	for _, other := range []struct{ local, remote string }{
		{"C:/b", "gdrive:a"}, // same remote, different local
		{"C:/a", "gdrive:b"}, // same local, different remote
		{"C:/b", "gdrive:b"}, // both differ
	} {
		if got := PairWorkdir(other.local, other.remote); got == reference {
			t.Errorf("pair (%q, %q) collides with (C:/a, gdrive:a) on workdir %q", other.local, other.remote, got)
		}
	}
}

// TestPairWorkdirSeparatesLocalFromRemote verifies the two halves of a pair's
// identity cannot bleed into one another. Deriving the name from a bare
// concatenation would map ("C:/a", "b") and ("C:/ab", "") to the same string
// and therefore to the same baseline, which is precisely the kind of silent
// aliasing this naming scheme exists to prevent.
func TestPairWorkdirSeparatesLocalFromRemote(t *testing.T) {
	if PairWorkdir("C:/a", "b") == PairWorkdir("C:/ab", "") {
		t.Error("local and remote are concatenated without a separator; distinct pairs alias onto one workdir")
	}
}

// TestPairWorkdirNameShape pins the directory's leaf name to the short
// "pair-<hex>" form. The name has to stay filesystem-safe on every supported
// OS (it is derived from paths that contain slashes, colons and spaces), and
// short enough that the workdir plus rclone's own long listing file names stay
// well inside Windows' path length limit.
func TestPairWorkdirNameShape(t *testing.T) {
	name := filepath.Base(PairWorkdir("C:/Users/me/My Documents", "gdrive:My Documents"))
	if matched, _ := regexp.MatchString(`^pair-[0-9a-f]{8}$`, name); !matched {
		t.Errorf("workdir name = %q, want the form pair-<8 hex chars>", name)
	}
}

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
