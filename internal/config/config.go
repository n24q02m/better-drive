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
}

type Config struct {
	Pairs []Pair `toml:"pair"`
}

// tomlPair mirrors Pair nhưng Interval là string để toml decode "30s".
type tomlPair struct {
	Local    string `toml:"local"`
	Remote   string `toml:"remote"`
	Interval string `toml:"interval"`
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
		c.Pairs = append(c.Pairs, Pair{Local: p.Local, Remote: p.Remote, Interval: d})
	}
	return c, nil
}

func (c *Config) Validate() error {
	if len(c.Pairs) != 1 {
		return fmt.Errorf("v1 supports exactly 1 pair, got %d", len(c.Pairs))
	}
	p := c.Pairs[0]
	if p.Local == "" || p.Remote == "" {
		return fmt.Errorf("pair: local and remote required")
	}
	if p.Interval <= 0 {
		return fmt.Errorf("pair: interval must be > 0")
	}
	return nil
}
