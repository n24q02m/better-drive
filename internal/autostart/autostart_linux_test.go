//go:build linux

package autostart

import (
	"fmt"
	"strings"
	"testing"
)

func TestEscapeSystemd(t *testing.T) {
	path := `/opt/better drive/100%/$$better-drive`
	unit := fmt.Sprintf(unitTemplate, escapeSystemd(path))

	want := `ExecStart="/opt/better drive/100%%/$$$$better-drive" run`
	if !strings.Contains(unit, want) {
		t.Fatalf("generated unit = %q, want substring %q", unit, want)
	}
}
