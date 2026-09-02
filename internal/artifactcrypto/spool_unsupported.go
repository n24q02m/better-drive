//go:build !windows && !linux && !darwin

package artifactcrypto

import (
	"errors"
	"os"
)

func createSecureSpool() (*os.File, error) {
	return nil, errors.New("secure artifact spools are unsupported on this platform")
}

func cleanupSecureSpool(spool *os.File) error {
	if spool == nil {
		return nil
	}
	return spool.Close()
}
