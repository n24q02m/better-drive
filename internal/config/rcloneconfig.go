package config

import (
	"os"
	"path/filepath"
)

// ResolveRcloneConfig returns the rclone config path to hand librclone: the
// explicit value if non-empty, else the first existing default location. The
// scoop portable rclone.conf (adjacent to the scoop rclone binary) is only
// found by the rclone CLI, not by librclone's default (%APPDATA%), so probe it
// first, then %APPDATA%. Returns "" if none exist (librclone falls back to its
// own default).
func ResolveRcloneConfig(explicit string) string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, "scoop", "apps", "rclone", "current", "rclone.conf"),
	}
	if ad := os.Getenv("APPDATA"); ad != "" {
		candidates = append(candidates, filepath.Join(ad, "rclone", "rclone.conf"))
	}
	return resolveFrom(explicit, candidates)
}

func resolveFrom(explicit string, candidates []string) string {
	if explicit != "" {
		return explicit
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}
