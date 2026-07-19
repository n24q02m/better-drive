//go:build !windows && !darwin && !linux

package autostart

import "errors"

// Windows, macOS, and Linux each have a real autostart implementation
// (autostart_windows.go, autostart_darwin.go, autostart_linux.go); this file
// only exists so any other GOOS still compiles, since cli.go imports this
// package unconditionally. These stubs are never reached on a supported
// platform.
var errUnsupported = errors.New("autostart is not supported on this platform")

func Enable(string) error    { return errUnsupported }
func Disable() error         { return errUnsupported }
func Enabled() (bool, error) { return false, nil }
