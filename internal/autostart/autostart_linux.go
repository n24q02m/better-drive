//go:build linux

// Package autostart registers better-drive to launch at user login via a
// systemd user service (~/.config/systemd/user), the standard per-user
// autostart mechanism on Linux - no root needed, and no dependency on any
// particular desktop environment's autostart convention.
package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const serviceName = "better-drive.service"

const unitTemplate = `[Unit]
Description=better-drive sync daemon

[Service]
ExecStart=%s run
Restart=on-failure

[Install]
WantedBy=default.target
`

func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", serviceName), nil
}

func Enable(exePath string) error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	// Fix: Restrict systemd user directory permissions to 0700 and unit file to 0600
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	unit := fmt.Sprintf(unitTemplate, exePath)
	if err := os.WriteFile(path, []byte(unit), 0o600); err != nil {
		return err
	}
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return err
	}
	return exec.Command("systemctl", "--user", "enable", "--now", serviceName).Run()
}

func Disable() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", serviceName).Run() // ignore errors
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func Enabled() (bool, error) {
	path, err := unitPath()
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
