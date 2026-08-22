package scheduler

import (
	"fmt"
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

func commandLine(d Definition) string {
	parts := []string{quote(d.Executable), "sync", "--format", "json", "--config", quote(d.Config)}
	if len(d.Arguments) > 0 {
		parts = append([]string{quote(d.Executable)}, quoteArgs(d.Arguments)...)
	}
	return strings.Join(parts, " ")
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
