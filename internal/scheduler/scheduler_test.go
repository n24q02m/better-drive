package scheduler

import (
	"strings"
	"testing"
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
