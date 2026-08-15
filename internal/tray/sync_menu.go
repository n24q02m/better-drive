package tray

import "github.com/n24q02m/better-drive/internal/syncloop"

func syncMenuState(aggregate AggregateState) (enabled bool, tooltip string) {
	switch {
	case aggregate.State == syncloop.StateSyncing:
		return false, "Sync is currently in progress"
	case aggregate.NeedsResync:
		return false, "Run better-drive sync --resync to rebuild the bisync baseline"
	case aggregate.State == syncloop.StatePaused:
		return false, "Cannot sync while paused"
	default:
		return true, "Trigger a sync immediately for all pairs"
	}
}
