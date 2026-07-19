//go:build linux || darwin

package tray

import _ "embed"

//go:embed icon.png
var trayIcon []byte
