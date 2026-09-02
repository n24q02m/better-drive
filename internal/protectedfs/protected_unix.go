//go:build !windows

package protectedfs

import (
	"errors"
	"os"
	"syscall"
)

func EnsurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if err := verifyPrivateUnixInfo(info, true); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	return VerifyPrivateDir(path)
}

func VerifyPrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if err := verifyPrivateUnixInfo(info, true); err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("protected directory permissions must be owner-only")
	}
	return nil
}

func CreatePrivateFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
}

func OpenPrivateFile(path string) (*os.File, error) {
	descriptor, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = syscall.Close(descriptor)
		return nil, errors.New("open protected file handle failed")
	}
	if err := VerifyPrivateFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func VerifyPrivateFile(file *os.File) error {
	if file == nil {
		return errors.New("protected file is required")
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := verifyPrivateUnixInfo(info, false); err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("protected file permissions must be owner-only")
	}
	return nil
}

func verifyPrivateUnixInfo(info os.FileInfo, directory bool) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory ||
		(!directory && !info.Mode().IsRegular()) {
		return errors.New("protected path type is invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("protected path owner is not the current process owner")
	}
	return nil
}
