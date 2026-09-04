package scheduler

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/state"
)

func missingReadback(platform Platform, now time.Time) Readback {
	return Readback{
		Platform:      platform,
		Installed:     false,
		Enabled:       false,
		OverlapState:  state.OverlapNone,
		OverlapHealth: "ok",
		ObservedAt:    now.UTC(),
		Health:        state.HealthMissing,
	}
}

func parseRFC3339Time(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func nativeCommandError(action string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, message)
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create scheduler directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".better-drive-scheduler-*")
	if err != nil {
		return fmt.Errorf("create scheduler temporary file: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return fmt.Errorf("protect scheduler temporary file: %w", err)
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return fmt.Errorf("write scheduler temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync scheduler temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close scheduler temporary file: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace scheduler file: %w", err)
	}
	return nil
}

func removeFileIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
