package paths

import (
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

func ConfigFile() string { return filepath.Join(base(), "config.toml") }
func Workdir() string    { return filepath.Join(base(), "bisync") }
