//go:build windows || ((linux || darwin) && cgo)

package tray

import (
	"os/exec"
	"runtime"

	"fyne.io/systray"
	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/syncloop"
)

// Run starts the systray icon and blocks until Quit is chosen. loops and
// jobs must be the same length and index-aligned (loops[i] is the Loop
// driving jobs[i]); agg must already be wired to loops via agg.Register so
// it reflects their combined state.
func Run(loops []*syncloop.Loop, jobs []config.Job, agg *Aggregator) error {
	systray.Run(func() { onReady(loops, jobs, agg) }, func() {})
	return nil
}

func onReady(loops []*syncloop.Loop, jobs []config.Job, agg *Aggregator) {
	systray.SetIcon(trayIcon)
	systray.SetTitle("better-drive")
	systray.SetTooltip("better-drive")
	mStatus := systray.AddMenuItem("Status: idle", "Current status: idle")
	mStatus.Disable()
	systray.AddSeparator()
	mSync := systray.AddMenuItem("Sync now", "Trigger a sync immediately for all pairs")
	mPause := systray.AddMenuItem("Pause", "Pause scheduled syncs for all pairs")
	systray.AddSeparator()
	mOpen := systray.AddMenuItem("Open folder", "Open the local sync folder(s)")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Exit better-drive")

	agg.OnChange(func(aggregate AggregateState) {
		systray.SetTooltip(trayIconTooltip(aggregate))
		statusTitle, statusTooltip := trayStatusText(aggregate)
		mStatus.SetTitle(statusTitle)
		mStatus.SetTooltip(statusTooltip)
		pauseEnabled, pauseTitle, pauseTooltip := pauseMenuState(aggregate)
		mPause.SetTitle(pauseTitle)
		mPause.SetTooltip(pauseTooltip)
		if pauseEnabled {
			mPause.Enable()
		} else {
			mPause.Disable()
		}
		syncEnabled, syncTooltip := syncMenuState(aggregate)
		if syncEnabled {
			mSync.Enable()
		} else {
			mSync.Disable()
		}
		mSync.SetTooltip(syncTooltip)
	})

	go func() {
		for {
			select {
			case <-mSync.ClickedCh:
				for _, l := range loops {
					l.SyncNow()
				}
			case <-mPause.ClickedCh:
				if agg.Aggregate().NeedsResync {
					continue
				}
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
				for _, job := range jobs {
					openFolder(job.Source)
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func openFolder(path string) {
	cleanPath, err := validateOpenFolder(path)
	if err != nil {
		return
	}
	var cmd string
	switch runtime.GOOS {
	case "windows":
		cmd = "explorer"
	case "darwin":
		cmd = "open"
	default:
		cmd = "xdg-open"
	}
	/* #nosec G204 */
	_ = exec.Command(cmd, cleanPath).Start()
}
