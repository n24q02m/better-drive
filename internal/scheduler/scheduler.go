package scheduler

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Platform string

const (
	PlatformWindows Platform = "windows"
	PlatformLinux   Platform = "linux"
	PlatformDarwin  Platform = "darwin"
)

type Definition struct {
	JobID                 string
	Executable            string
	Config                string
	Arguments             []string
	IntervalSeconds       int
	CatchUp               bool
	ExecutionLimitSeconds int
	Owner                 string
}

type OwnerRecord struct {
	Owner string
	JobID string
}

func (d Definition) Validate() error {
	if strings.TrimSpace(d.JobID) == "" || strings.TrimSpace(d.Executable) == "" || strings.TrimSpace(d.Config) == "" {
		return fmt.Errorf("scheduler definition requires job_id, executable, and config")
	}
	if !isAbsolutePath(d.Executable) {
		return fmt.Errorf("scheduler definition requires absolute executable")
	}
	if !isAbsolutePath(d.Config) {
		return fmt.Errorf("scheduler definition requires absolute config")
	}
	if d.IntervalSeconds <= 0 || d.ExecutionLimitSeconds <= 0 {
		return fmt.Errorf("scheduler interval and execution limit must be > 0")
	}
	if strings.TrimSpace(d.Owner) == "" {
		return fmt.Errorf("scheduler owner is required")
	}
	return nil
}

func Render(platform Platform, definition Definition) ([]byte, error) {
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	switch platform {
	case PlatformWindows:
		return renderWindows(definition), nil
	case PlatformLinux:
		return renderLinux(definition), nil
	case PlatformDarwin:
		return renderDarwin(definition), nil
	default:
		return nil, fmt.Errorf("unsupported scheduler platform %q", platform)
	}
}

func ValidateOwner(current OwnerRecord, desired Definition, replace bool) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	if current.Owner == "" && current.JobID == "" {
		return nil
	}
	if current.Owner == desired.Owner && (current.JobID == "" || current.JobID == desired.JobID) {
		return nil
	}
	if !replace {
		return fmt.Errorf("scheduler owner %q/%q differs from desired %q/%q; use --replace", current.Owner, current.JobID, desired.Owner, desired.JobID)
	}
	return nil
}

func isAbsolutePath(value string) bool {
	if filepath.IsAbs(value) {
		return true
	}
	return len(value) >= 3 &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) &&
		value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func commandLine(d Definition) string {
	parts := []string{quote(d.Executable)}
	for _, arg := range schedulerArguments(d) {
		parts = append(parts, quote(arg))
	}
	return strings.Join(parts, " ")
}

func schedulerArguments(d Definition) []string {
	args := append([]string(nil), d.Arguments...)
	if len(args) == 0 {
		return []string{"sync", "--format", "json", "--config", d.Config}
	}
	if !containsArgument(args, "sync") {
		args = append([]string{"sync"}, args...)
	}
	if !containsFlagValue(args, "--format") {
		args = append(args, "--format", "json")
	}
	if !containsFlagValue(args, "--config") {
		args = append(args, "--config", d.Config)
	}
	return args
}

func containsArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsFlagValue(args []string, flag string) bool {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) && strings.TrimSpace(args[i+1]) != "" && !strings.HasPrefix(args[i+1], "-") {
			return true
		}
		if strings.HasPrefix(arg, flag+"=") && strings.TrimSpace(strings.TrimPrefix(arg, flag+"=")) != "" {
			return true
		}
	}
	return false
}

func quote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func quoteArgs(values []string) []string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, quote(value))
	}
	return quoted
}

func formatInterval(seconds int) string {
	if seconds%(60*60) == 0 {
		return fmt.Sprintf("%dh", seconds/(60*60))
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%ds", seconds)
}
