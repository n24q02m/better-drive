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

// JobWorkdir returns the stable baseline directory for a normalized job ID.
// Job IDs, rather than config-list positions or display paths, own bisync
// state so a config reorder cannot silently hand one job another baseline.
func JobWorkdir(jobID string) string {
	sum := sha256.Sum256([]byte(jobID))
	return filepath.Join(Workdir(), "job-"+hex.EncodeToString(sum[:4]))
}

// JobReplicaWorkdir isolates a destination's bisync baseline under its owning
// job so two replicas cannot share listing state.
func JobReplicaWorkdir(jobID, replicaID string) string {
	sum := sha256.Sum256([]byte(jobID + "\x00" + replicaID))
	return filepath.Join(JobWorkdir(jobID), "replica-"+hex.EncodeToString(sum[:4]))
}

// LogFile returns the path of the daemon's persistent sync log
// (base()/better-drive.log), where `better-drive run` appends one line per
// completed sync cycle per pair.
func LogFile() string { return filepath.Join(base(), "better-drive.log") }
