package output_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/output"
	"github.com/spf13/cobra"
)

func TestRenderJSON_PairResults(t *testing.T) {
	var buf bytes.Buffer
	results := []output.PairResult{
		{Local: "/a", Remote: "gdrive:a", Mode: "bisync", Status: "ok"},
		{Local: "/b", Remote: "gdrive:b", Mode: "bisync", Status: "failed", Error: "boom"},
	}
	if err := output.RenderJSON(&buf, results); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	var got []output.PairResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, results) {
		t.Errorf("got %#v, want %#v", got, results)
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("json output must end with a newline; got %q", buf.String())
	}
}

func TestValidate(t *testing.T) {
	if err := output.Validate(output.FormatTable); err != nil {
		t.Errorf("Validate(table) = %v, want nil", err)
	}
	if err := output.Validate(output.FormatJSON); err != nil {
		t.Errorf("Validate(json) = %v, want nil", err)
	}
	if err := output.Validate("xml"); err == nil {
		t.Error("Validate(xml) = nil, want an error for an unknown format")
	}
}

func TestAddFormatFlagRegistersTableDefault(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	var format string
	output.AddFormatFlag(cmd, &format)
	if got := cmd.Flag("format"); got == nil {
		t.Fatal("AddFormatFlag did not register --format")
	}
	if got := cmd.Flag("format").DefValue; got != output.FormatTable {
		t.Fatalf("--format default = %q, want %q", got, output.FormatTable)
	}
	if err := cmd.ParseFlags([]string{"--format", output.FormatJSON}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if format != output.FormatJSON {
		t.Fatalf("format = %q, want %q after parsing", format, output.FormatJSON)
	}
}
