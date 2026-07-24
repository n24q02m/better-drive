// Package exitcode maps errors to process exit codes so a caller (a script or
// an agent) can branch on the failure category without parsing stderr text.
package exitcode

import "errors"

const (
	Success             = 0
	GenericError        = 1
	ConfigErrorCode     = 2
	RemoteNotConfigured = 3
	SyncFailedCode      = 4
)

type coded struct {
	code int
	err  error
}

func (c *coded) Error() string { return c.err.Error() }
func (c *coded) Unwrap() error { return c.err }

func withCode(code int, err error) error {
	if err == nil {
		return nil
	}
	return &coded{code: code, err: err}
}

// ConfigError marks a failure to read, parse, or validate configuration.
func ConfigError(err error) error { return withCode(ConfigErrorCode, err) }

// RemoteNotConfiguredError marks an rclone remote that is missing or token-less.
func RemoteNotConfiguredError(err error) error { return withCode(RemoteNotConfigured, err) }

// SyncFailed marks one or more sync pairs failing.
func SyncFailed(err error) error { return withCode(SyncFailedCode, err) }

// Code returns the exit code for err, unwrapping wrapped errors.
func Code(err error) int {
	if err == nil {
		return Success
	}
	var c *coded
	if errors.As(err, &c) {
		return c.code
	}
	return GenericError
}
