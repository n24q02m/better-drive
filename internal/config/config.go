package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

func Load(path string) (*Config, error) {
	return LoadWithOptions(path, LoadOptions{})
}

// LoadRcloneConfigOnly reads the explicitly enrolled rclone config path for
// commands such as mount that do not consume sync jobs. A missing config file
// remains allowed for the foreground mount command; sync-capable commands use
// ValidateForExecution and reject an unbound runtime before any spawn.
func LoadRcloneConfigOnly(path string) (string, error) {
	var raw struct {
		RcloneConfig  string `toml:"rclone_config"`
		RcloneRuntime struct {
			Config string `toml:"config"`
		} `toml:"rclone_runtime"`
	}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("decode %s: %w", path, err)
	}
	if raw.RcloneRuntime.Config != "" {
		return raw.RcloneRuntime.Config, nil
	}
	return raw.RcloneConfig, nil
}
