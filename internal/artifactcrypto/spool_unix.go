//go:build !windows

package artifactcrypto

import (
	"errors"
	"os"
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
	return spool, nil
}
