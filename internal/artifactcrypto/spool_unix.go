//go:build linux || darwin

package artifactcrypto

import (
	"errors"
	"os"
	"syscall"
)

func createSecureSpool() (*os.File, error) {
	spool, err := os.CreateTemp("", spoolPattern)
	if err != nil {
		return nil, err
	}
	info, err := spool.Stat()
	if err != nil {
		_ = cleanupSpool(spool)
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = cleanupSpool(spool)
		return nil, errors.New("artifact spool is not a private regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Uid) != uint64(os.Geteuid()) {
		_ = cleanupSpool(spool)
		return nil, errors.New("artifact spool owner is invalid")
	}
	return spool, nil
}

func cleanupSecureSpool(spool *os.File) error {
	if spool == nil {
		return nil
	}
	var cleanupErrs []error
	if err := spool.Close(); err != nil {
		cleanupErrs = append(cleanupErrs, wrapError("close artifact spool", err))
	}
	if err := os.Remove(spool.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanupErrs = append(cleanupErrs, wrapError("remove artifact spool", err))
	}
	return errors.Join(cleanupErrs...)
}
