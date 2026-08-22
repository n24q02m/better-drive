package engine

import (
	"fmt"
	"sort"

	"github.com/n24q02m/better-drive/internal/config"
)

// NewVerified constructs the transfer engine only from an enrolled runtime.
// It never performs PATH lookup and never inherits the caller's environment.
func NewVerified(runtime config.RcloneRuntime) (*Engine, error) {
	if err := runtime.Validate(); err != nil {
		return nil, fmt.Errorf("rclone runtime: %w", err)
	}
	env := explicitEnvironment(runtime.Environment)
	return &Engine{
		bin:    runtime.Executable,
		cfg:    runtime.Config,
		run:    execRunnerWithEnvironment(runtime.Executable, env),
		stream: execStreamRunnerWithEnvironment(runtime.Executable, env),
	}, nil
}

func explicitEnvironment(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}
