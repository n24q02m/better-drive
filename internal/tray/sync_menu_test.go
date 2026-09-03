package tray

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/syncloop"
)

func TestSyncMenuStatePausedDisablesWithReason(t *testing.T) {
	enabled, title, tooltip := syncMenuState(AggregateState{State: syncloop.StatePaused})
	if enabled {
		t.Fatal("paused sync action is enabled")
	}
	if !strings.Contains(title, "Cannot sync while paused") {
		t.Fatalf("paused sync title = %q", title)
	}
	if tooltip != "Cannot sync while paused" {
		t.Fatalf("paused sync tooltip = %q, want %q", tooltip, "Cannot sync while paused")
	}
}

func TestSyncMenuMixedErrorAndNeedsResyncDisables(t *testing.T) {
	enabled, title, tooltip := syncMenuState(AggregateState{
		State:       syncloop.StateError,
		NeedsResync: true,
	})
	if enabled {
		t.Fatal("sync action is enabled while one pair needs resync")
	}
	if !strings.Contains(title, "--resync") {
		t.Fatalf("needs-resync title = %q", title)
	}
	if tooltip != "Run better-drive sync --resync to rebuild the bisync baseline" {
		t.Fatalf("needs-resync tooltip = %q", tooltip)
	}
}

func TestSyncMenuRegularErrorRemainsEnabledForRetry(t *testing.T) {
	enabled, title, tooltip := syncMenuState(AggregateState{State: syncloop.StateError})
	if !enabled {
		t.Fatal("ordinary error disabled Sync now and changed retry semantics")
	}
	if title != "Sync now" {
		t.Fatalf("ordinary error title = %q", title)
	}
	if tooltip != "Trigger a sync immediately for all pairs" {
		t.Fatalf("ordinary error tooltip = %q", tooltip)
	}
}

func TestPauseMenuNeedsResyncCannotMaskRecoveryState(t *testing.T) {
	enabled, title, tooltip := pauseMenuState(AggregateState{
		State:       syncloop.StateError,
		NeedsResync: true,
	})
	if enabled {
		t.Fatal("Pause is enabled while a pair needs resync")
	}
	if !strings.Contains(title, "better-drive sync --resync") || !strings.Contains(tooltip, "better-drive sync --resync") {
		t.Fatalf("pause menu = enabled:%v title:%q tooltip:%q", enabled, title, tooltip)
	}
}

func TestPauseMenuPausedBecomesResume(t *testing.T) {
	enabled, title, tooltip := pauseMenuState(AggregateState{State: syncloop.StatePaused})
	if !enabled || title != "Resume" || tooltip != "Resume scheduled syncs for all pairs" {
		t.Fatalf("pause menu = enabled:%v title:%q tooltip:%q", enabled, title, tooltip)
	}
}

func TestPauseMenuIdleAndErrorRemainEnabled(t *testing.T) {
	for _, state := range []syncloop.State{syncloop.StateIdle, syncloop.StateError} {
		enabled, title, _ := pauseMenuState(AggregateState{State: state})
		if !enabled || title != "Pause" {
			t.Fatalf("state %s pause menu = enabled:%v title:%q", state, enabled, title)
		}
	}
}

func TestTrayStatusNeedsResyncNamesRecoveryCommand(t *testing.T) {
	title, tooltip := trayStatusText(AggregateState{State: syncloop.StateError, NeedsResync: true})
	if !strings.Contains(title, "better-drive sync --resync") || !strings.Contains(tooltip, "better-drive sync --resync") {
		t.Fatalf("status text = title:%q tooltip:%q", title, tooltip)
	}
	regularTitle, regularTooltip := trayStatusText(AggregateState{State: syncloop.StateError})
	if regularTitle != "Status: error" || regularTooltip != "Current status: error" {
		t.Fatalf("ordinary error text changed: title:%q tooltip:%q", regularTitle, regularTooltip)
	}
}

func TestTrayIconTooltipNeedsResyncIsActionable(t *testing.T) {
	if got := trayIconTooltip(AggregateState{State: syncloop.StateError, NeedsResync: true}); !strings.Contains(got, "better-drive sync --resync") {
		t.Fatalf("needs-resync icon tooltip = %q", got)
	}
	if got := trayIconTooltip(AggregateState{State: syncloop.StateError}); got != "better-drive - error" {
		t.Fatalf("ordinary error icon tooltip changed: %q", got)
	}
}
func TestValidateOpenFolderRequiresExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	got, err := validateOpenFolder(dir)
	if err != nil || got != filepath.Clean(dir) {
		t.Fatalf("validateOpenFolder(directory) = %q, %v", got, err)
	}

	file := filepath.Join(dir, "payload.exe")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(dir, "missing")} {
		if _, err := validateOpenFolder(path); err == nil {
			t.Fatalf("validateOpenFolder(%q) accepted a non-directory", path)
		}
	}
}
