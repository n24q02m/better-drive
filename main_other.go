//go:build !windows

package main

// attachParentConsole is a no-op stub on non-Windows so cross-OS builds/vet
// (e.g. CI running on Linux) still pass; better-drive itself is Windows-only.
func attachParentConsole() {}
