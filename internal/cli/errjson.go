package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
)

// errorEnvelope is the --format json shape for a failing command.
type errorEnvelope struct {
	Error       string `json:"error"`
	Code        int    `json:"code"`
	Remediation string `json:"remediation,omitempty"`
}

// RenderError writes err to w: "error: <msg>" for the table format (byte-
// identical to the plain fmt.Fprintln(os.Stderr, "error:", err) main.go used
// before this existed), or a parseable JSON envelope when format is
// output.FormatJSON -- so a --format json caller gets a body it can
// unmarshal (matching exitcode.Code) instead of falling back to parsing
// stderr text.
func RenderError(w io.Writer, err error, format string) {
	if err == nil {
		return
	}
	if format != output.FormatJSON {
		fmt.Fprintln(w, "error:", err)
		return
	}
	env := errorEnvelope{
		Error:       err.Error(),
		Code:        exitcode.Code(err),
		Remediation: exitcode.RemediationOf(err),
	}
	data, mErr := json.MarshalIndent(env, "", "  ")
	if mErr != nil {
		// Never swallow the original error just because rendering failed.
		fmt.Fprintln(w, "error:", err)
		return
	}
	fmt.Fprintln(w, string(data))
}
