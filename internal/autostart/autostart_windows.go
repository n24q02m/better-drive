//go:build windows

// Package autostart registers better-drive to launch at user login via the
// per-user HKCU Run key (no admin needed). The value runs the GUI-subsystem
// binary as `run`, so it starts the hidden tray daemon at login.
package autostart

import "golang.org/x/sys/windows/registry"

const (
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName = "better-drive"
)

func Enable(exePath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(valueName, `"`+exePath+`" run`)
}

func Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(valueName); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

func Enabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false, nil
	}
	defer k.Close()
	if _, _, err := k.GetStringValue(valueName); err == registry.ErrNotExist {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}
