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
