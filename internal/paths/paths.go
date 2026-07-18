package paths

import (
	"fmt"
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

// PairWorkdir returns a workdir unique to the pair at the given index in the
// config's [[pair]] list. Each pair needs its own workdir: bisync mode keeps
// baseline listing files (*.lst) and a filters.txt in the workdir, and those
// would collide across pairs (and corrupt each other's baseline) if the N
// pairs of a multi-pair config all shared the single top-level Workdir().
func PairWorkdir(index int) string {
	return filepath.Join(Workdir(), fmt.Sprintf("pair-%d", index))
}
