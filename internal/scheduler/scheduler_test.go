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

func TestValidateOwnerRejectsUnknownOwnerUnlessReplace(t *testing.T) {
	if err := ValidateOwner(OwnerRecord{Owner: "other", JobID: "other-job"}, testDefinition(), false); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatal("unknown owner overwrite was accepted")
	}
	if err := ValidateOwner(OwnerRecord{Owner: "other", JobID: "other-job"}, testDefinition(), true); err != nil {
		t.Fatalf("--replace should allow explicit owner replacement: %v", err)
	}
}
