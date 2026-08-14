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

func TestSyncMenuStateNeedsResyncDisablesWithAction(t *testing.T) {
	enabled, tooltip := syncMenuState(syncloop.StateNeedsResync)
	if enabled {
		t.Fatal("needs-resync sync action is enabled")
	}
	if tooltip != "Sync broken: run 'better-drive sync --resync' to fix" {
		t.Fatalf("needs-resync sync tooltip = %q, want %q", tooltip, "Sync broken: run 'better-drive sync --resync' to fix")
	}
}
