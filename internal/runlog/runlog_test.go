package runlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func decodeEvents(t *testing.T, data []byte) []Event {
	t.Helper()
	var events []Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func TestSinkMergesInterleavedStreamsInWriteOrder(t *testing.T) {
	var buf bytes.Buffer
	sink := NewSink("run-1", &buf)
	stdout, stderr := sink.Stdout(), sink.Stderr()
	if _, err := stdout.Write([]byte("out-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := stderr.Write([]byte("err-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := stdout.Write([]byte("out-2")); err != nil {
		t.Fatal(err)
	}
	events := decodeEvents(t, buf.Bytes())
	if got := []Stream{events[0].Stream, events[1].Stream, events[2].Stream}; !reflect.DeepEqual(got, []Stream{StreamStdout, StreamStderr, StreamStdout}) {
		t.Fatalf("stream order=%v", got)
	}
	for i, event := range events {
		if event.Sequence != uint64(i+1) || event.Timestamp.Location() != time.UTC {
			t.Fatalf("event[%d]=%#v, want increasing sequence and UTC", i, event)
		}
	}
}

func TestSinkRedactsCredentialShapedValuesAndBoundsMessages(t *testing.T) {
	var buf bytes.Buffer
	sink := NewSink("run-1", &buf)
	sink.SetMaxMessageBytes(48)
	if _, err := sink.Emit(StreamSystem, `Authorization: Bearer abc123 token=secret123 password="pw"`+strings.Repeat("x", 100)); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	for _, secret := range []string{"abc123", "secret123", "pw"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q leaked in %q", secret, text)
		}
	}
	if len(decodeEvents(t, buf.Bytes())[0].Message) > 48 {
		t.Fatal("event message exceeded configured bound")
	}
}

func TestRunEmitsExactlyOneTerminalForEachOutcome(t *testing.T) {
	cases := []struct {
		name    string
		ctx     context.Context
		fn      func() error
		outcome string
	}{
		{name: "success", ctx: context.Background(), fn: func() error { return nil }, outcome: "success"},
		{name: "error", ctx: context.Background(), fn: func() error { return errors.New("boom") }, outcome: "error"},
		{name: "panic", ctx: context.Background(), fn: func() error { panic("boom") }, outcome: "panic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			sink := NewSink("run-1", &buf)
			_ = Run(tc.ctx, sink, tc.fn)
			events := decodeEvents(t, buf.Bytes())
			terminals := 0
			for _, event := range events {
				if event.Terminal {
					terminals++
					if event.Outcome != tc.outcome {
						t.Fatalf("terminal outcome=%q, want %q", event.Outcome, tc.outcome)
					}
				}
			}
			if terminals != 1 || !sink.TerminalWritten() {
				t.Fatalf("terminal records=%d events=%#v", terminals, events)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf bytes.Buffer
	sink := NewSink("run-cancel", &buf)
	if err := Run(ctx, sink, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	if events := decodeEvents(t, buf.Bytes()); len(events) != 1 || events[0].Outcome != "cancelled" {
		t.Fatalf("cancel events=%#v", events)
	}
}

func TestRotatingFileBoundsSizeAgeAndRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	file, err := OpenRotatingFile(path, RotationOptions{MaxBytes: 5, MaxAge: time.Hour, Retain: 2, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("6")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := file.Write([]byte("7")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".1", ".2"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Fatalf("missing retained log %q: %v", path+suffix, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("retention exceeded: stat .3=%v", err)
	}
}
