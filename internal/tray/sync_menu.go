package tray

import "github.com/n24q02m/better-drive/internal/syncloop"

func syncMenuState(st syncloop.State) (enabled bool, tooltip string) {
	switch st {
	case syncloop.StateSyncing:
		return false, "Sync is currently in progress"
	case syncloop.StatePaused:
		return false, "Cannot sync while paused"
	case syncloop.StateNeedsResync:
		return false, "Manual resync required: run 'better-drive sync --resync'"
	default:
		return true, "Trigger a sync immediately for all pairs"
	}
}
