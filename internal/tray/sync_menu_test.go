package tray

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/syncloop"
)

func TestSyncMenuStatePausedDisablesWithReason(t *testing.T) {
	enabled, _, _ := syncMenuState(AggregateState{State: syncloop.StatePaused})
	if enabled {
		t.Fatal("paused sync action is enabled")
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
	if !strings.Contains(title, "better-drive sync --resync") || !strings.Contains(tooltip, "better-drive sync --resync") {
		t.Fatalf("resync-required state omits the recovery command: title=%q tooltip=%q", title, tooltip)
	}
}

func TestSyncMenuRegularErrorRemainsEnabledForRetry(t *testing.T) {
	enabled, _, _ := syncMenuState(AggregateState{State: syncloop.StateError})
	if !enabled {
		t.Fatal("ordinary error disabled Sync now and changed retry semantics")
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

func TestPauseMenuIdleAndErrorRemainEnabled(t *testing.T) {
	for _, state := range []syncloop.State{syncloop.StateIdle, syncloop.StateError} {
		enabled, _, _ := pauseMenuState(AggregateState{State: state})
		if !enabled {
			t.Fatalf("pause disabled in retryable state %s", state)
		}
	}
}

func TestTrayIconTooltipNeedsResyncIsActionable(t *testing.T) {
	if got := trayIconTooltip(AggregateState{State: syncloop.StateError, NeedsResync: true}); !strings.Contains(got, "better-drive sync --resync") {
		t.Fatalf("needs-resync icon tooltip = %q", got)
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
