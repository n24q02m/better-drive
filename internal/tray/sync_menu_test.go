package tray

import (
	"testing"

	"github.com/n24q02m/better-drive/internal/syncloop"
)

func TestSyncMenuStatePausedDisablesWithReason(t *testing.T) {
	enabled, tooltip := syncMenuState(syncloop.StatePaused)
	if enabled {
		t.Fatal("paused sync action is enabled")
	}
	if tooltip != "Cannot sync while paused" {
		t.Fatalf("paused sync tooltip = %q, want %q", tooltip, "Cannot sync while paused")
	}
}
