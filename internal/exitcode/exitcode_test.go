package exitcode_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/n24q02m/better-drive/internal/exitcode"
)

func TestCode(t *testing.T) {
	if got := exitcode.Code(nil); got != 0 {
		t.Errorf("Code(nil) = %d, want 0", got)
	}
	if got := exitcode.Code(errors.New("unclassified")); got != 1 {
		t.Errorf("Code(unclassified) = %d, want 1", got)
	}
	if got := exitcode.Code(exitcode.ConfigError(errors.New("bad toml"))); got != 2 {
		t.Errorf("Code(ConfigError) = %d, want 2", got)
	}
	if got := exitcode.Code(exitcode.RemoteNotConfiguredError(errors.New("no token"))); got != 3 {
		t.Errorf("Code(RemoteNotConfiguredError) = %d, want 3", got)
	}
	if got := exitcode.Code(exitcode.SyncFailed(errors.New("one or more pairs failed"))); got != 4 {
		t.Errorf("Code(SyncFailed) = %d, want 4", got)
	}
}

// TestCodeUnwrapsWrappedErrors verifies Code sees through fmt.Errorf("...: %w",
// ...) wrapping, so a caller further up the stack can add context without
// losing the exit-code classification.
func TestCodeUnwrapsWrappedErrors(t *testing.T) {
	wrapped := fmt.Errorf("context: %w", exitcode.ConfigError(errors.New("bad toml")))
	if got := exitcode.Code(wrapped); got != 2 {
		t.Errorf("Code(wrapped ConfigError) = %d, want 2", got)
	}
}

// TestRemediationOf_NoneAttached verifies a plain (or exitcode-classified)
// error with no WithRemediation call carries no hint, so RenderError can omit
// the field instead of emitting an empty string.
func TestRemediationOf_NoneAttached(t *testing.T) {
	if got := exitcode.RemediationOf(errors.New("plain")); got != "" {
		t.Errorf("RemediationOf(plain) = %q, want empty", got)
	}
	if got := exitcode.RemediationOf(exitcode.ConfigError(errors.New("bad toml"))); got != "" {
		t.Errorf("RemediationOf(ConfigError, no hint attached) = %q, want empty", got)
	}
}

// TestWithRemediation_AttachesHint verifies RemediationOf reads back exactly
// the hint WithRemediation attached.
func TestWithRemediation_AttachesHint(t *testing.T) {
	err := exitcode.WithRemediation(errors.New("not set up"), "run: better-drive setup")
	if got := exitcode.RemediationOf(err); got != "run: better-drive setup" {
		t.Errorf("RemediationOf = %q, want %q", got, "run: better-drive setup")
	}
}

// TestWithRemediation_NilErrOrEmptyHintIsNoop verifies WithRemediation never
// manufactures an error from nil, and never attaches an empty hint (which
// would otherwise render as a useless "" in the JSON envelope).
func TestWithRemediation_NilErrOrEmptyHintIsNoop(t *testing.T) {
	if got := exitcode.WithRemediation(nil, "run: x"); got != nil {
		t.Errorf("WithRemediation(nil, hint) = %v, want nil", got)
	}
	base := errors.New("boom")
	if got := exitcode.WithRemediation(base, ""); got != base {
		t.Errorf("WithRemediation(err, \"\") = %v, want the original err unchanged", got)
	}
}

// TestWithRemediation_ComposesWithCode verifies a remediation hint attached
// on top of an exitcode.ConfigError (etc.) does not break Code's
// classification -- both errors.As chains (through *coded and through
// *remediated) must resolve on the same wrapped error, regardless of wrap
// order, since call sites in cli.go do
// exitcode.WithRemediation(exitcode.ConfigError(err), hint).
func TestWithRemediation_ComposesWithCode(t *testing.T) {
	err := exitcode.WithRemediation(exitcode.ConfigError(errors.New("bad toml")), "edit config.toml")
	if got := exitcode.Code(err); got != 2 {
		t.Errorf("Code(remediated ConfigError) = %d, want 2", got)
	}
	if got := exitcode.RemediationOf(err); got != "edit config.toml" {
		t.Errorf("RemediationOf(remediated ConfigError) = %q, want %q", got, "edit config.toml")
	}
}
