package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/state"
)

func testDefinition() Definition {
	return Definition{
		JobID: "job-1", Executable: `C:\Program Files\better-drive\better-drive.exe`,
		Config: `C:\Users\me\AppData\Roaming\better-drive\config.toml`, Arguments: []string{"sync", "--format", "json"},
		IntervalSeconds: 21600, CatchUp: true, ExecutionLimitSeconds: 3600, Owner: "better-drive",
	}
}

func TestRenderWindowsDefinitionHasHeadlessSafetyAndCatchup(t *testing.T) {
	got, err := Render(PlatformWindows, testDefinition())
	if err != nil {
		t.Fatalf("Render windows: %v", err)
	}
	text := string(got)
	for _, want := range []string{"Task", "WakeToRun", "StopExisting", "ExecutionTimeLimit", "better-drive.exe", "--format"} {
		if !strings.Contains(text, want) {
			t.Errorf("Windows definition missing %q: %s", want, text)
		}
	}
}

func TestDefinitionRejectsRelativeExecutableAndConfig(t *testing.T) {
	definition := testDefinition()
	definition.Executable = "better-drive.exe"
	if err := definition.Validate(); err == nil || !strings.Contains(err.Error(), "absolute executable") {
		t.Fatalf("Validate executable error = %v, want absolute-path rejection", err)
	}
	definition = testDefinition()
	definition.Config = "config.toml"
	if err := definition.Validate(); err == nil || !strings.Contains(err.Error(), "absolute config") {
		t.Fatalf("Validate config error = %v, want absolute-path rejection", err)
	}
}

func TestRenderLinuxDefinitionPersistsTimer(t *testing.T) {
	got, err := Render(PlatformLinux, testDefinition())
	if err != nil {
		t.Fatalf("Render linux: %v", err)
	}
	text := string(got)
	for _, want := range []string{"[Unit]", "[Service]", "[Timer]", "Persistent=true", "OnUnitActiveSec=6h"} {
		if !strings.Contains(text, want) {
			t.Errorf("Linux definition missing %q: %s", want, text)
		}
	}
}

func TestRenderDarwinDefinitionIsLaunchAgent(t *testing.T) {
	got, err := Render(PlatformDarwin, testDefinition())
	if err != nil {
		t.Fatalf("Render darwin: %v", err)
	}
	text := string(got)
	for _, want := range []string{"LaunchAgent", "ProgramArguments", "StartInterval", "21600"} {
		if !strings.Contains(text, want) {
			t.Errorf("Darwin definition missing %q: %s", want, text)
		}
	}
}

func TestRenderersKeepRequiredSyncArgumentsWhenOverridesArePartial(t *testing.T) {
	definition := testDefinition()
	linux, err := Render(PlatformLinux, definition)
	if err != nil {
		t.Fatalf("Render linux: %v", err)
	}
	linuxText := string(linux)
	for _, want := range []string{"sync", "--format", "json", "--config", definition.Config} {
		if !strings.Contains(linuxText, want) {
			t.Errorf("linux renderer missing required argument %q: %s", want, linuxText)
		}
	}

	definition.Arguments = nil
	darwin, err := Render(PlatformDarwin, definition)
	if err != nil {
		t.Fatalf("Render darwin: %v", err)
	}
	darwinText := string(darwin)
	for _, want := range []string{"sync", "--format", "json", "--config", definition.Config} {
		if !strings.Contains(darwinText, want) {
			t.Errorf("darwin renderer missing required argument %q: %s", want, darwinText)
		}
	}
}

func TestRendererRepairsIncompleteFlagArguments(t *testing.T) {
	definition := testDefinition()
	definition.Arguments = []string{"sync", "--format", "--config"}
	got, err := Render(PlatformLinux, definition)
	if err != nil {
		t.Fatalf("Render linux: %v", err)
	}
	text := string(got)
	if !strings.Contains(text, `"--format" "json"`) || !strings.Contains(text, `"--config" "`+definition.Config+`"`) {
		t.Fatalf("linux renderer did not repair incomplete flags: %s", text)
	}
}

func TestValidateOwnerRejectsUnknownOwnerUnlessReplace(t *testing.T) {
	if err := ValidateOwner(OwnerRecord{Owner: "other", JobID: "other-job"}, testDefinition(), false); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatal("unknown owner overwrite was accepted")
	}
	if err := ValidateOwner(OwnerRecord{Owner: "other", JobID: "other-job"}, testDefinition(), true); err != nil {
		t.Fatalf("--replace should allow explicit owner replacement: %v", err)
	}
}

func TestMemoryAdapterRoundTripAndOwnerMismatch(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	adapter := NewMemoryAdapter(PlatformLinux, func() time.Time { return now })
	ctx := context.Background()

	// Initial install
	def := testDefinition()
	if err := adapter.Install(ctx, def, false); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	readback, err := adapter.Readback(ctx, "job-1")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if !readback.Installed || readback.Owner.Owner != "better-drive" || readback.Owner.Generation != 1 || readback.Health != state.HealthHealthy {
		t.Fatalf("readback = %#v, want installed healthy generation 1", readback)
	}

	// Reject overwrite from different owner without replace
	other := testDefinition()
	other.Owner = "foreign-agent"
	if err := adapter.Install(ctx, other, false); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("install with foreign owner accepted: %v", err)
	}

	// Allow replace
	if err := adapter.Install(ctx, other, true); err != nil {
		t.Fatalf("replace install: %v", err)
	}
	readback, err = adapter.Readback(ctx, "job-1")
	if err != nil || readback.Owner.Owner != "foreign-agent" || readback.Owner.Generation != 2 {
		t.Fatalf("after replace readback = %#v", readback)
	}
}

func TestMemoryAdapterEnforcesSingleActiveAndDetectsOverlap(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	adapter := NewMemoryAdapter(PlatformWindows, func() time.Time { return now })
	ctx := context.Background()

	def := testDefinition()
	if err := adapter.Install(ctx, def, false); err != nil {
		t.Fatal(err)
	}

	adapter.SetActiveInstance("job-1", "inst-1", state.OverlapSingleActive)
	readback, err := adapter.Readback(ctx, "job-1")
	if err != nil || readback.ActiveInstance != "inst-1" || readback.Health != state.HealthHealthy {
		t.Fatalf("single active readback = %#v", readback)
	}

	// Overlap state detected
	adapter.SetActiveInstance("job-1", "inst-2", state.OverlapMultipleActive)
	readback, err = adapter.Readback(ctx, "job-1")
	if err != nil || readback.Health != state.HealthOverlap {
		t.Fatalf("overlap readback = %#v, want overlap health", readback)
	}

	// Removal refuses active instance unless forced
	if err := adapter.Remove(ctx, "job-1", false); err == nil {
		t.Fatal("remove without force accepted active job")
	}
	if err := adapter.Remove(ctx, "job-1", true); err != nil {
		t.Fatalf("forced remove failed: %v", err)
	}
	readback, err = adapter.Readback(ctx, "job-1")
	if err != nil || readback.Installed || readback.Health != state.HealthMissing {
		t.Fatalf("after remove readback = %#v, want missing", readback)
	}
}
