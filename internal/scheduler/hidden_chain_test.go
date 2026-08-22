package scheduler

import (
	"strings"
	"testing"
)

func TestValidateHiddenChainAcceptsNoWindowNoConsoleFixture(t *testing.T) {
	readback := HiddenChainReadback{ExitCode: 0, Processes: []ProcessSnapshot{
		{Role: "wscript", PID: 1, ParentPID: 0, Subsystem: "windows"},
		{Role: "powershell", PID: 2, ParentPID: 1, Subsystem: "console"},
		{Role: "better-drive", PID: 3, ParentPID: 2, Subsystem: "windows"},
		{Role: "rclone", PID: 4, ParentPID: 3, Subsystem: "console"},
	}}
	if err := ValidateHiddenChain(readback); err != nil {
		t.Fatalf("ValidateHiddenChain: %v", err)
	}
}

func TestValidateHiddenChainRejectsPopupAndConhostEvidence(t *testing.T) {
	base := HiddenChainReadback{ExitCode: 0, Processes: []ProcessSnapshot{
		{Role: "wscript", PID: 1, ParentPID: 0}, {Role: "powershell", PID: 2, ParentPID: 1},
		{Role: "better-drive", PID: 3, ParentPID: 2}, {Role: "rclone", PID: 4, ParentPID: 3},
	}}
	base.Processes[2].WindowTitle = "better-drive console"
	if err := ValidateHiddenChain(base); err == nil || !strings.Contains(err.Error(), "window") {
		t.Fatal("popup window evidence was accepted")
	}
	base.Processes[2].WindowTitle = ""
	base.Processes = append(base.Processes, ProcessSnapshot{Role: "conhost", PID: 5, ParentPID: 2})
	if err := ValidateHiddenChain(base); err == nil || !strings.Contains(err.Error(), "conhost") {
		t.Fatal("conhost child evidence was accepted")
	}
}

func TestValidateHiddenChainRejectsMissingRoleAndNonzeroExit(t *testing.T) {
	if err := ValidateHiddenChain(HiddenChainReadback{ExitCode: 1}); err == nil || !strings.Contains(err.Error(), "exit") {
		t.Fatal("nonzero diagnostic exit was accepted")
	}
	if err := ValidateHiddenChain(HiddenChainReadback{ExitCode: 0, Processes: []ProcessSnapshot{{Role: "wscript"}}}); err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatal("incomplete process chain was accepted")
	}
}
