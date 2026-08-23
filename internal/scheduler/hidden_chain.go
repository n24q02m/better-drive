package scheduler

import "fmt"

type ProcessSnapshot struct {
	Role         string `json:"role"`
	PID          uint32 `json:"pid"`
	ParentPID    uint32 `json:"parent_pid"`
	CommandLine  string `json:"command_line,omitempty"`
	WindowTitle  string `json:"window_title,omitempty"`
	WindowHandle uint64 `json:"window_handle,omitempty"`
	Subsystem    string `json:"subsystem,omitempty"`
}

type HiddenChainReadback struct {
	ExitCode  int               `json:"exit_code"`
	Processes []ProcessSnapshot `json:"processes"`
}

func ValidateHiddenChain(readback HiddenChainReadback) error {
	if readback.ExitCode != 0 {
		return fmt.Errorf("hidden-chain diagnostic exit code %d", readback.ExitCode)
	}
	seen := make(map[string]ProcessSnapshot, len(readback.Processes))
	for _, process := range readback.Processes {
		if process.Role == "" {
			continue
		}
		if process.WindowHandle != 0 || process.WindowTitle != "" {
			return fmt.Errorf("hidden-chain role %q has a visible window", process.Role)
		}
		if process.Role == "conhost" {
			return fmt.Errorf("hidden-chain observed conhost child")
		}
		seen[process.Role] = process
	}
	for _, role := range []string{"wscript", "powershell", "better-drive", "rclone"} {
		if _, ok := seen[role]; !ok {
			return fmt.Errorf("hidden-chain missing required role %q", role)
		}
	}
	return nil
}
