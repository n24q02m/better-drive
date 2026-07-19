//go:build windows

package tray

import _ "embed"

//go:embed icon.ico
var trayIcon []byte
