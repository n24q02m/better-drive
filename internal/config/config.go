package config

import (
	"fmt"
	"time"

	"github.com/BurntSushi/toml"
)

type Pair struct {
	Local    string
	Remote   string
	Interval time.Duration
	Mode     string
	// Exclude holds config-level gitignore-syntax patterns for this pair,
	// combined with any .driveignore file at Local by PairFilters. Lets a
	// pair (e.g. ~/.claude) exclude paths (node_modules, .venv, caches) from
	// the config itself instead of requiring a .driveignore file dropped
	// into a real user directory.
	Exclude []string
}

type Config struct {
	Pairs []Pair `toml:"pair"`
}

// tomlPair mirrors Pair nhưng Interval là string để toml decode "30s".
type tomlPair struct {
	Local    string   `toml:"local"`
	Remote   string   `toml:"remote"`
	Interval string   `toml:"interval"`
	Mode     string   `toml:"mode"`
	Exclude  []string `toml:"exclude"`
}

type tomlConfig struct {
	Pairs []tomlPair `toml:"pair"`
}

func Load(path string) (*Config, error) {
	var raw tomlConfig
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	c := &Config{}
	for _, p := range raw.Pairs {
		d, err := time.ParseDuration(p.Interval)
		if err != nil {
			return nil, fmt.Errorf("pair %q: bad interval %q: %w", p.Local, p.Interval, err)
		}
		mode := p.Mode
		if mode == "" {
			mode = "bisync"
		}
		c.Pairs = append(c.Pairs, Pair{Local: p.Local, Remote: p.Remote, Interval: d, Mode: mode, Exclude: p.Exclude})
	}
	return c, nil
}

// Validate checks every pair independently; N pairs (>= 1) are supported.
func (c *Config) Validate() error {
	if len(c.Pairs) == 0 {
		return fmt.Errorf("config: at least 1 pair required, got 0")
	}
	for i, p := range c.Pairs {
		if p.Local == "" || p.Remote == "" {
			return fmt.Errorf("pair %d: local and remote required", i)
		}
		if p.Interval <= 0 {
			return fmt.Errorf("pair %d: interval must be > 0", i)
		}
		switch p.Mode {
		case "bisync", "copy", "sync":
		default:
			return fmt.Errorf("pair %d: mode must be one of bisync|copy|sync, got %q", i, p.Mode)
		}
	}
	return nil
}
