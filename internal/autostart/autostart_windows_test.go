//go:build windows

package autostart

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestEnableDisableRoundTrip writes the real Run value, asserts Enabled, then
// removes it. Uses the real HKCU Run key but a distinct temp exe path and
// cleans up; valueName "better-drive" is ours, so this is self-contained.
func TestEnableDisableRoundTrip(t *testing.T) {
	t.Cleanup(func() { _ = Disable() })
	if err := Enable(`C:\tmp\better-drive.exe`); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	on, err := Enabled()
	if err != nil || !on {
		t.Fatalf("Enabled after Enable = %v, %v; want true", on, err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := k.GetStringValue(valueName)
	k.Close()
	if want := `"C:\tmp\better-drive.exe" run`; got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
	if err := Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	on, _ = Enabled()
	if on {
		t.Fatal("Enabled after Disable = true; want false")
	}
}
