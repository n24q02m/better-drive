//go:build !windows && !linux && !darwin

package tray

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/syncloop"
)

// Run has no tray UI on the remaining platforms (anything other than
// windows, linux, and darwin, which all get the real systray UI from
// tray_systray.go): fyne.io/systray has no cgo-free support there, and cross
// builds for those platforms must stay CGO_ENABLED=0. loops and pairs must
// be the same length and index-aligned (loops[i] is the Loop driving
// pairs[i]) and agg must already be wired to loops via agg.Register,
// matching the systray Run signature exactly (cli.go calls tray.Run with no
// build-tag switch of its own) - but this headless Run never touches them,
// since the sync loops are already started by the caller before Run is
// called. It simply blocks until SIGINT or SIGTERM, keeping the process
// alive, then returns nil so the caller can shut the loops down the same way
// it would after the tray's Quit.
func Run(loops []*syncloop.Loop, pairs []config.Pair, agg *Aggregator) error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	return nil
}
