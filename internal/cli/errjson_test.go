package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/n24q02m/better-drive/internal/cli"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
)

// TestRenderError_JSONEnvelope verifies --format json renders a parseable
// {error, code, remediation} body on stderr instead of a plain-text line, so
// a script/agent never has to fall back to parsing "error: ..." text.
func TestRenderError_JSONEnvelope(t *testing.T) {
	var buf bytes.Buffer
	err := exitcode.SyncFailed(errors.New("one or more pairs failed"))
	cli.RenderError(&buf, err, output.FormatJSON)

	var got struct {
		Error       string `json:"error"`
		Code        int    `json:"code"`
		Remediation string `json:"remediation"`
	}
	if uErr := json.Unmarshal(buf.Bytes(), &got); uErr != nil {
		t.Fatalf("Unmarshal: %v; got:\n%s", uErr, buf.String())
	}
	if got.Error != "one or more pairs failed" {
		t.Errorf("Error = %q, want %q", got.Error, "one or more pairs failed")
	}
	if got.Code != exitcode.SyncFailedCode {
		t.Errorf("Code = %d, want %d", got.Code, exitcode.SyncFailedCode)
	}
	if got.Remediation != "" {
		t.Errorf("Remediation = %q, want empty (no hint attached)", got.Remediation)
	}
}

// TestRenderError_JSONEnvelopeCarriesRemediation verifies a hint attached via
// exitcode.WithRemediation round-trips into the JSON envelope's
// "remediation" field.
func TestRenderError_JSONEnvelopeCarriesRemediation(t *testing.T) {
	var buf bytes.Buffer
	base := exitcode.RemoteNotConfiguredError(errors.New(`remote "gdrive" is not set up`))
	err := exitcode.WithRemediation(base, "run: better-drive setup --remote gdrive")
	cli.RenderError(&buf, err, output.FormatJSON)

	var got struct {
		Code        int    `json:"code"`
		Remediation string `json:"remediation"`
	}
	if uErr := json.Unmarshal(buf.Bytes(), &got); uErr != nil {
		t.Fatalf("Unmarshal: %v; got:\n%s", uErr, buf.String())
	}
	if got.Code != exitcode.RemoteNotConfigured {
		t.Errorf("Code = %d, want %d", got.Code, exitcode.RemoteNotConfigured)
	}
	if got.Remediation != "run: better-drive setup --remote gdrive" {
		t.Errorf("Remediation = %q, want %q", got.Remediation, "run: better-drive setup --remote gdrive")
	}
}

// TestRenderError_TableFormatUnchanged verifies the table (default) format
// stays byte-identical to the pre-envelope main.go behavior
// (fmt.Fprintln(os.Stderr, "error:", err)), so existing scripts parsing
// stderr text see no difference.
func TestRenderError_TableFormatUnchanged(t *testing.T) {
	var buf bytes.Buffer
	cli.RenderError(&buf, errors.New("plain failure"), output.FormatTable)
	if got, want := buf.String(), "error: plain failure\n"; got != want {
		t.Errorf("table format = %q, want %q", got, want)
	}
}

// TestRenderError_NilErrorNoOutput verifies a nil error (the success path)
// writes nothing, in either format.
func TestRenderError_NilErrorNoOutput(t *testing.T) {
	var buf bytes.Buffer
	cli.RenderError(&buf, nil, output.FormatJSON)
	if buf.Len() != 0 {
		t.Errorf("nil error wrote %q, want no output", buf.String())
	}
}
