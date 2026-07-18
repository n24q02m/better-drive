//go:build !windows

package autostart

import "errors"

// better-drive is Windows-only (goreleaser goos: windows), but cli.go imports
// this package unconditionally, so cross-platform CI (which runs `go build` on
// Linux) needs a non-Windows implementation for the package to have any Go
// files. These stubs are never reached in a real run.
var errUnsupported = errors.New("autostart is only supported on Windows")

func Enable(string) error    { return errUnsupported }
func Disable() error         { return errUnsupported }
func Enabled() (bool, error) { return false, nil }
