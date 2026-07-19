//go:build windows || linux

package tray

import (
	"os/exec"
	"runtime"

	"fyne.io/systray"
	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/syncloop"
)

// Run starts the systray icon and blocks until Quit is chosen. loops and
// pairs must be the same length and index-aligned (loops[i] is the Loop
// driving pairs[i]); agg must already be wired to loops via agg.Register so
// it reflects their combined state.
func Run(loops []*syncloop.Loop, pairs []config.Pair, agg *Aggregator) error {
	systray.Run(func() { onReady(loops, pairs, agg) }, func() {})
	return nil
}

func onReady(loops []*syncloop.Loop, pairs []config.Pair, agg *Aggregator) {
	systray.SetIcon(trayIcon)
	systray.SetTitle("better-drive")
	systray.SetTooltip("better-drive: idle")
	mStatus := systray.AddMenuItem("Status: idle", "")
	mStatus.Disable()
	systray.AddSeparator()
	mSync := systray.AddMenuItem("Sync now", "Trigger a sync immediately for all pairs")
	mPause := systray.AddMenuItem("Pause", "Pause scheduled syncs for all pairs")
	systray.AddSeparator()
	mOpen := systray.AddMenuItem("Open folder", "Open the local sync folder(s)")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Exit better-drive")

	agg.OnChange(func(st syncloop.State) {
		systray.SetTooltip("better-drive: " + st.String())
		mStatus.SetTitle("Status: " + st.String())
		if st == syncloop.StatePaused {
			mPause.SetTitle("Resume")
		} else {
			mPause.SetTitle("Pause")
		}
	})

	go func() {
		for {
			select {
			case <-mSync.ClickedCh:
				for _, l := range loops {
					l.SyncNow()
				}
			case <-mPause.ClickedCh:
				if agg.State() == syncloop.StatePaused {
					for _, l := range loops {
						l.Resume()
					}
				} else {
					for _, l := range loops {
						l.Pause()
					}
				}
			case <-mOpen.ClickedCh:
				for _, p := range pairs {
					openFolder(p.Local)
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func openFolder(path string) {
	if runtime.GOOS == "windows" {
		_ = exec.Command("explorer", path).Start()
	}
}
