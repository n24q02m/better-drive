package tray

import (
	_ "embed"
	"os/exec"
	"runtime"

	"fyne.io/systray"
	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/syncloop"
)

//go:embed icon.ico
var iconData []byte

func Run(loop *syncloop.Loop, pair config.Pair) error {
	systray.Run(func() { onReady(loop, pair) }, func() {})
	return nil
}

func onReady(loop *syncloop.Loop, pair config.Pair) {
	systray.SetIcon(iconData)
	systray.SetTitle("better-drive")
	systray.SetTooltip("better-drive")
	mStatus := systray.AddMenuItem("Status: idle", "")
	mStatus.Disable()
	mSync := systray.AddMenuItem("Sync now", "Trigger a sync immediately")
	mPause := systray.AddMenuItem("Pause", "Pause scheduled syncs")
	mOpen := systray.AddMenuItem("Open folder", "Open the local sync folder")
	mQuit := systray.AddMenuItem("Quit", "Exit better-drive")

	loop.OnChange(func(st syncloop.State) {
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
				loop.SyncNow()
			case <-mPause.ClickedCh:
				if loop.State() == syncloop.StatePaused {
					loop.Resume()
				} else {
					loop.Pause()
				}
			case <-mOpen.ClickedCh:
				openFolder(pair.Local)
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
