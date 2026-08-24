package tray

import (
	"testing"

	"github.com/n24q02m/better-drive/internal/syncloop"
)

func TestPauseMenuStateNeedsResyncDisables(t *testing.T) {
	enabled, title, tooltip := pauseMenuState(AggregateState{NeedsResync: true})
	if enabled {
		t.Fatal("pause action is enabled while manual resync is required")
	}
	if title != "Pause" {
		t.Fatalf("needs-resync title = %q, want Pause", title)
	}
	if tooltip != "Run better-drive sync --resync to rebuild the bisync baseline" {
		t.Fatalf("needs-resync tooltip = %q", tooltip)
	}
}

func TestPauseMenuStatePaused(t *testing.T) {
	enabled, title, tooltip := pauseMenuState(AggregateState{State: syncloop.StatePaused})
	if !enabled {
		t.Fatal("resume action is disabled while paused")
	}
	if title != "Resume" {
		t.Fatalf("paused title = %q, want Resume", title)
	}
	if tooltip != "Resume scheduled syncs for all pairs" {
		t.Fatalf("paused tooltip = %q", tooltip)
	}
}

func TestPauseMenuStateIdle(t *testing.T) {
	enabled, title, tooltip := pauseMenuState(AggregateState{State: syncloop.StateIdle})
	if !enabled {
		t.Fatal("pause action is disabled while idle")
	}
	if title != "Pause" {
		t.Fatalf("idle title = %q, want Pause", title)
	}
	if tooltip != "Pause scheduled syncs for all pairs" {
		t.Fatalf("idle tooltip = %q", tooltip)
	}
}

func TestSyncMenuStatePausedDisablesWithReason(t *testing.T) {
	enabled, tooltip := syncMenuState(AggregateState{State: syncloop.StatePaused})
	if enabled {
		t.Fatal("paused sync action is enabled")
	}
	if tooltip != "Cannot sync while paused" {
		t.Fatalf("paused sync tooltip = %q, want %q", tooltip, "Cannot sync while paused")
	}
}

func TestSyncMenuMixedErrorAndNeedsResyncDisables(t *testing.T) {
	enabled, tooltip := syncMenuState(AggregateState{
		State:       syncloop.StateError,
		NeedsResync: true,
	})
	if enabled {
		t.Fatal("sync action is enabled while one pair needs resync")
	}
	if tooltip != "Run better-drive sync --resync to rebuild the bisync baseline" {
		t.Fatalf("needs-resync tooltip = %q", tooltip)
	}
}

func TestSyncMenuRegularErrorRemainsEnabledForRetry(t *testing.T) {
	enabled, tooltip := syncMenuState(AggregateState{State: syncloop.StateError})
	if !enabled {
		t.Fatal("ordinary error disabled Sync now and changed retry semantics")
	}
	if tooltip != "Trigger a sync immediately for all pairs" {
		t.Fatalf("ordinary error tooltip = %q", tooltip)
	}
}
