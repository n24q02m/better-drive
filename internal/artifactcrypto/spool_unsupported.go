//go:build !windows && !linux && !darwin

package artifactcrypto

import (
	"errors"
	"os"
)

func createSecureSpool() (*os.File, error) {
	return nil, errors.New("secure artifact spools are unsupported on this platform")
}
