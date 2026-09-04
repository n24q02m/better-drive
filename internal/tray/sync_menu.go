package tray

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/n24q02m/better-drive/internal/syncloop"
)

func syncMenuState(aggregate AggregateState) (enabled bool, title, tooltip string) {
	switch {
	case aggregate.State == syncloop.StateSyncing:
		return false, "Sync now (sync in progress)", "Sync is currently in progress"
	case aggregate.NeedsResync:
		return false, "Sync now (run better-drive sync --resync)", "Run better-drive sync --resync to rebuild the bisync baseline"
	case aggregate.State == syncloop.StatePaused:
		return false, "Sync now (cannot sync while paused)", "Cannot sync while paused"
	default:
		return true, "Sync now", "Trigger a sync immediately for all pairs"
	}
}

func pauseMenuState(aggregate AggregateState) (enabled bool, title, tooltip string) {
	if aggregate.NeedsResync {
		return false, "Pause (run better-drive sync --resync)", "Run better-drive sync --resync before pausing"
	}
	if aggregate.State == syncloop.StatePaused {
		return true, "Resume", "Resume scheduled syncs for all pairs"
	}
	return true, "Pause", "Pause scheduled syncs for all pairs"
}

func trayStatusText(aggregate AggregateState) (title, tooltip string) {
	title = "Status: " + aggregate.State.String()
	if aggregate.NeedsResync {
		return title, "Run better-drive sync --resync to rebuild the bisync baseline"
	}
	return title, "Current status: " + aggregate.State.String()
}

func trayIconTooltip(aggregate AggregateState) string {
	if aggregate.NeedsResync {
		return "better-drive - run better-drive sync --resync"
	}
	return "better-drive - " + aggregate.State.String()
}

func validateOpenFolder(path string) (string, error) {
	cleanPath := filepath.Clean(path)
	info, err := os.Stat(cleanPath)
	if err != nil {
		return "", fmt.Errorf("inspect folder: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}
	return cleanPath, nil
}
