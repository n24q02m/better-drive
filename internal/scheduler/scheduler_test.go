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
		Config: `C:\Users\me\AppData\Roaming\better-drive\config.toml`, Arguments: []string{"sync", "--format", "table", "--job=wrong", "--config", `C:\wrong.toml`, "--dry-run"},
		IntervalSeconds: 21600, CatchUp: true, ExecutionLimitSeconds: 21600, Owner: "better-drive",
	}
}

func TestRenderWindowsDefinitionIsDisabledAndNeverStopsExistingRun(t *testing.T) {
	got, err := Render(PlatformWindows, testDefinition())
	if err != nil {
		t.Fatalf("Render windows: %v", err)
	}
	text := string(got)
	for _, want := range []string{
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<ExecutionTimeLimit>PT21600S</ExecutionTimeLimit>",
		"<Enabled>false</Enabled>",
		"<WakeToRun>true</WakeToRun>",
		`&quot;--job&quot; &quot;job-1&quot;`,
		`&quot;--format&quot; &quot;json&quot;`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Windows definition missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "StopExisting") || strings.Contains(text, "wrong") {
		t.Fatalf("Windows definition retained conflicting scheduler arguments: %s", text)
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
func TestDefinitionRejectsNativeControlCharacters(t *testing.T) {
	for name, mutate := range map[string]func(*Definition){
		"job ID":   func(definition *Definition) { definition.JobID = "job\ninjected" },
		"owner":    func(definition *Definition) { definition.Owner = "better-drive\rforeign" },
		"argument": func(definition *Definition) { definition.Arguments = []string{"--dry-run\t--config"} },
	} {
		t.Run(name, func(t *testing.T) {
			definition := testDefinition()
			mutate(&definition)
			if err := definition.Validate(); err == nil || !strings.Contains(err.Error(), "control characters") {
				t.Fatalf("Validate error = %v, want native-definition injection rejection", err)
			}
		})
	}
}

func TestNativeArgumentQuotingPreservesBoundaries(t *testing.T) {
	definition := testDefinition()
	definition.JobID = `job%with"quote`
	definition.Config = `C:\config path\`
	systemd := commandLine(definition)
	if !strings.Contains(systemd, `job%%with\"quote`) || !strings.Contains(systemd, `"C:\\config path\\"`) {
		t.Fatalf("systemd command line did not escape native metacharacters: %s", systemd)
	}
	if got, want := quoteWindows(`C:\config path\`), `"C:\config path\\"`; got != want {
		t.Fatalf("Windows quoted path = %q, want %q", got, want)
	}
}

func TestRenderLinuxDefinitionPersistsTimerForExactJob(t *testing.T) {
	got, err := Render(PlatformLinux, testDefinition())
	if err != nil {
		t.Fatalf("Render linux: %v", err)
	}
	text := string(got)
	for _, want := range []string{"[Unit]", "[Service]", "[Timer]", "Persistent=true", "OnUnitActiveSec=6h", `"--job" "job-1"`} {
		if !strings.Contains(text, want) {
			t.Errorf("Linux definition missing %q: %s", want, text)
		}
	}
}

func TestRenderDarwinDefinitionIsDisabledLaunchAgentForExactJob(t *testing.T) {
	got, err := Render(PlatformDarwin, testDefinition())
	if err != nil {
		t.Fatalf("Render darwin: %v", err)
	}
	text := string(got)
	for _, want := range []string{"LaunchAgent", "<key>Label</key><string>" + darwinLabel(testDefinition().JobID) + "</string>", "ProgramArguments", "StartInterval", "21600", "<key>RunAtLoad</key><false/>", "<string>--job</string>", "<string>job-1</string>"} {
		if !strings.Contains(text, want) {
			t.Errorf("Darwin definition missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "com.better-drive.com.better-drive") {
		t.Fatalf("Darwin definition duplicated the launchd label namespace: %s", text)
	}
}

func TestSchedulerArgumentsCanonicalizeManagedFlags(t *testing.T) {
	definition := testDefinition()
	got := schedulerArguments(definition)
	want := []string{"sync", "--job", definition.JobID, "--format", "json", "--config", definition.Config, "--dry-run"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("scheduler arguments = %#v, want %#v", got, want)
	}
}

func TestDefinitionMetadataAndManagedNameRoundTrip(t *testing.T) {
	definition := testDefinition()
	definition.Generation = 7
	got, err := definitionFromMetadata(definitionMetadata(definition))
	if err != nil {
		t.Fatalf("definitionFromMetadata: %v", err)
	}
	if !MatchesDefinition(Readback{Installed: true, Owner: OwnerRecord{Owner: got.Owner, JobID: got.JobID, Generation: got.Generation}, Definition: &got}, definition) {
		t.Fatalf("metadata round trip = %#v, want %#v", got, definition)
	}
	unsafeName := ManagedName("job with / unsafe : selectors")
	if unsafeName == ManagedName("different-job") || !strings.HasPrefix(unsafeName, "better-drive-") ||
		len(unsafeName) != len("better-drive-")+24 || strings.ContainsAny(unsafeName, " /:") {
		t.Fatalf("managed name is not stable and selector-safe: %q", unsafeName)
	}
}

func TestValidateOwnerRejectsUnknownOwnerUnlessReplace(t *testing.T) {
	if err := ValidateOwner(OwnerRecord{Owner: "other", JobID: "other-job"}, testDefinition(), false); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatal("unknown owner overwrite was accepted")
	}
	if err := ValidateOwner(OwnerRecord{Owner: "other", JobID: "other-job"}, testDefinition(), true); err != nil {
		t.Fatalf("--replace should allow explicit recoverable owner replacement: %v", err)
	}
}

func TestMemoryAdapterInstallsDisabledThenEnablesAndRunsExactJob(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	adapter := NewMemoryAdapter(PlatformLinux, func() time.Time { return now })
	ctx := context.Background()
	definition := testDefinition()
	definition.Arguments = nil
	if err := adapter.Install(ctx, definition, false); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	readback, err := adapter.Readback(ctx, definition.JobID)
	if err != nil || !readback.Installed || readback.Enabled || readback.Health != state.HealthDisabled || !MatchesDefinition(readback, definition) {
		t.Fatalf("disabled install readback = %#v, err=%v", readback, err)
	}
	if err := adapter.Run(ctx, definition.JobID); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled job run error = %v", err)
	}
	if err := adapter.SetEnabled(ctx, definition.JobID, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := adapter.Run(ctx, definition.JobID); err != nil {
		t.Fatalf("run: %v", err)
	}
	readback, err = adapter.Readback(ctx, definition.JobID)
	if err != nil || !readback.Enabled || readback.LastTrigger != now || readback.Health != state.HealthHealthy {
		t.Fatalf("enabled run readback = %#v, err=%v", readback, err)
	}
}

func TestMemoryAdapterOwnerOverlapAndRemovalSafety(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	adapter := NewMemoryAdapter(PlatformWindows, func() time.Time { return now })
	ctx := context.Background()
	definition := testDefinition()
	if err := adapter.Install(ctx, definition, false); err != nil {
		t.Fatal(err)
	}
	other := definition
	other.Owner = "foreign-agent"
	if err := adapter.Install(ctx, other, false); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("install with foreign owner accepted: %v", err)
	}
	if err := adapter.SetEnabled(ctx, definition.JobID, true); err != nil {
		t.Fatal(err)
	}
	adapter.SetActiveInstance(definition.JobID, "inst-1", state.OverlapMultipleActive)
	readback, err := adapter.Readback(ctx, definition.JobID)
	if err != nil || readback.Health != state.HealthOverlap {
		t.Fatalf("overlap readback = %#v, err=%v", readback, err)
	}
	if err := adapter.Remove(ctx, definition.JobID, false); err == nil {
		t.Fatal("remove without force accepted active job")
	}
	adapter.SetActiveInstance(definition.JobID, "", state.OverlapNone)
	if err := adapter.Remove(ctx, definition.JobID, false); err != nil {
		t.Fatalf("remove drained job: %v", err)
	}
	readback, err = adapter.Readback(ctx, definition.JobID)
	if err != nil || readback.Installed || readback.Health != state.HealthMissing {
		t.Fatalf("after remove readback = %#v, err=%v", readback, err)
	}
}
