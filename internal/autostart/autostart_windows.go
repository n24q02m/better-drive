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

type runRegistry interface {
	set(value string) error
	delete() error
	enabled() (bool, error)
}

type windowsRunRegistry struct{}

func (windowsRunRegistry) set(value string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(valueName, value)
}

func (windowsRunRegistry) delete() error {
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

func (windowsRunRegistry) enabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err == registry.ErrNotExist {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer k.Close()
	if _, _, err := k.GetStringValue(valueName); err == registry.ErrNotExist {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

type autostartRegistration struct {
	registry runRegistry
}

func newAutostart(registry runRegistry) *autostartRegistration {
	return &autostartRegistration{registry: registry}
}

func (a *autostartRegistration) enable(exePath string) error {
	return a.registry.set(`"` + exePath + `" run`)
}

func (a *autostartRegistration) disable() error {
	return a.registry.delete()
}

func (a *autostartRegistration) enabled() (bool, error) {
	return a.registry.enabled()
}

var systemAutostart = newAutostart(windowsRunRegistry{})

func Enable(exePath string) error { return systemAutostart.enable(exePath) }
func Disable() error              { return systemAutostart.disable() }
func Enabled() (bool, error)      { return systemAutostart.enabled() }
