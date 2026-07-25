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

// remediated wraps an error with a one-line, copy-pasteable fix hint (e.g.
// "run: better-drive setup --remote gdrive"). Kept as a separate wrapper
// (rather than a field on coded) so a hint can be attached independently of
// -- and in either order around -- the exit-code classification: Code and
// RemediationOf each unwrap past the other via errors.As.
type remediated struct {
	err  error
	hint string
}

func (r *remediated) Error() string { return r.err.Error() }
func (r *remediated) Unwrap() error { return r.err }

// WithRemediation attaches hint to err, to be surfaced by a --format json
// caller (see cli.RenderError) as the "remediation" field. Returns err
// unchanged if err is nil or hint is empty, so a bare error never renders a
// useless empty hint.
func WithRemediation(err error, hint string) error {
	if err == nil || hint == "" {
		return err
	}
	return &remediated{err: err, hint: hint}
}

// RemediationOf returns the hint attached to err via WithRemediation, or ""
// if none was attached.
func RemediationOf(err error) string {
	var r *remediated
	if errors.As(err, &r) {
		return r.hint
	}
	return ""
}
