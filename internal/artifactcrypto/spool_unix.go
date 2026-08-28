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
