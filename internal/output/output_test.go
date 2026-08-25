package output_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestRenderJSONRedactsCredentialValues(t *testing.T) {
	var buf bytes.Buffer
	payload := map[string]string{
		"token":         "supersecret123",
		"authorization": "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNdfw",
	}
	if err := output.RenderJSON(&buf, payload); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	text := buf.String()
	if strings.Contains(text, "supersecret123") {
		t.Fatalf("token leaked in output: %s", text)
	}
	if strings.Contains(text, "dozjgNdfw") {
		t.Fatalf("jwt signature leaked in output: %s", text)
	}
}

func TestRenderJSONStatusEnvelopeWithNestedScheduler(t *testing.T) {
	var buf bytes.Buffer
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	envelope := output.StatusEnvelope{
		Scheduler: output.SchedulerStatus{
			Owner: "better-drive", OwnerJobID: "job-1", Enabled: true,
			ActiveInstance: "instance-1", OverlapState: "none", OverlapHealth: "ok",
			ObservedAt: now, FreshnessWindow: 15 * time.Minute, CatchUpGrace: time.Hour, Health: "healthy",
		},
		Pairs: []output.PairStatus{{JobID: "job-1", Local: "/local", Remote: "gdrive:remote", Mode: "copy", Interval: "6h", Health: "healthy"}},
	}
	if err := output.RenderJSON(&buf, envelope); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var got output.StatusEnvelope
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode StatusEnvelope: %v", err)
	}
	if got.Scheduler.Health != "healthy" || len(got.Pairs) != 1 || got.Pairs[0].JobID != "job-1" {
		t.Fatalf("decoded envelope = %#v", got)
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
