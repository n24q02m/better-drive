package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

func base() string {
	dir, err := os.UserConfigDir() // Windows: %AppData%
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "better-drive")
}

// ConfigFile returns the config.toml path. BETTER_DRIVE_CONFIG overrides it
// (used by tests to point at a temp config, and by users who want a non-default
// location).
func ConfigFile() string {
	if p := os.Getenv("BETTER_DRIVE_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(base(), "config.toml")
}
func Workdir() string { return filepath.Join(base(), "bisync") }

// LogFile returns the path of the daemon's persistent sync log
// (base()/better-drive.log), where `better-drive run` appends one line per
// completed sync cycle per pair.
func LogFile() string { return filepath.Join(base(), "better-drive.log") }

// PairWorkdir returns a workdir unique to the pair syncing local <-> remote.
// Each pair needs its own workdir: bisync mode keeps baseline listing files
// (*.lst) and a filters.txt in the workdir, and those would collide across
// pairs (and corrupt each other's baseline) if the N pairs of a multi-pair
// config all shared the single top-level Workdir().
//
// The name is derived from the pair's IDENTITY (its two paths), not from its
// position in the config's [[pair]] list, because position is not stable:
// reordering the blocks, or inserting/deleting one, used to hand a pair the
// directory holding another pair's listings. rclone then aborted with "must
// run --resync" (it has no listing for this path1/path2 session) while
// syncloop.baselineExists, which only asks whether any *.lst file is present,
// kept reporting a baseline and never requested one - leaving the pair stuck
// on every subsequent run. Keying on identity also means a pair whose paths
// really did change starts from a fresh, correctly empty workdir.
//
// The two paths are hashed rather than used directly because they contain
// separators, colons and spaces that are not portable directory names, and
// truncated to 8 hex characters so the workdir plus rclone's own long listing
// file names stay well inside Windows' path length limit. A NUL byte joins
// them so that no ("C:/a", "b") / ("C:/ab", "") style pair of distinct
// identities can collapse onto the same digest.
func PairWorkdir(local, remote string) string {
	sum := sha256.Sum256([]byte(local + "\x00" + remote))
	return filepath.Join(Workdir(), "pair-"+hex.EncodeToString(sum[:4]))
}
