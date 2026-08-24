// Package output renders command results either as the human table format or
// as JSON, so the same data serves a person and an agent.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/n24q02m/better-drive/internal/runlog"
	"github.com/spf13/cobra"
)

const (
	FormatTable = "table"
	FormatJSON  = "json"
)

// SchedulerStatus exposes the live/persisted scheduler health and ownership
// readback in the nested status JSON envelope.
type SchedulerStatus struct {
	Owner           string        `json:"owner"`
	OwnerJobID      string        `json:"owner_job_id"`
	Enabled         bool          `json:"enabled"`
	LastTrigger     *time.Time    `json:"last_trigger,omitempty"`
	NextTrigger     *time.Time    `json:"next_trigger,omitempty"`
	ActiveInstance  string        `json:"active_instance"`
	OverlapState    string        `json:"overlap_state"`
	OverlapHealth   string        `json:"overlap_health"`
	ObservedAt      time.Time     `json:"observed_at"`
	FreshnessWindow time.Duration `json:"freshness_window"`
	CatchUpGrace    time.Duration `json:"catch_up_grace"`
	Health          string        `json:"health"`
	Warnings        []string      `json:"warnings,omitempty"`
}

// StatusEnvelope combines the nested scheduler record with per-pair/job
// evidence in one explicit machine-readable object.
type StatusEnvelope struct {
	Scheduler SchedulerStatus `json:"scheduler"`
	Pairs     []PairStatus    `json:"pairs"`
}

// PairStatus is one destination of a normalized job, as reported by `status`.
type PairStatus struct {
	JobID       string     `json:"job_id,omitempty"`
	Local       string     `json:"local"`
	Remote      string     `json:"remote"`
	Mode        string     `json:"mode"`
	Interval    string     `json:"interval"`
	JobStatus   string     `json:"job_status,omitempty"`
	Health      string     `json:"health,omitempty"`
	LastSuccess *time.Time `json:"last_success,omitempty"`
	NextDue     *time.Time `json:"next_due,omitempty"`
	ObjectCount int64      `json:"object_count"`
	ByteCount   int64      `json:"byte_count"`
	Warnings    []string   `json:"warnings,omitempty"`
}

// ReplicaResult is one destination outcome within a job's sync cycle.
type ReplicaResult struct {
	ID       string `json:"id"`
	Target   string `json:"target"`
	Required bool   `json:"required"`
	Status   string `json:"status"` // ok | failed
	Error    string `json:"error,omitempty"`
}

type PairResult struct {
	JobID       string          `json:"job_id,omitempty"`
	Local       string          `json:"local"`
	Remote      string          `json:"remote"`
	Mode        string          `json:"mode"`
	Status      string          `json:"status"` // ok | degraded | failed | skipped
	Error       string          `json:"error,omitempty"`
	Replicas    []ReplicaResult `json:"replicas,omitempty"`
	ObjectCount int64           `json:"object_count,omitempty"`
	ByteCount   int64           `json:"byte_count,omitempty"`
	NextDue     time.Time       `json:"next_due,omitempty"`
	Warnings    []string        `json:"warnings,omitempty"`
	// DryRun reports whether this cycle only previewed changes (--dry-run)
	// rather than applying them.
	DryRun bool `json:"dry_run,omitempty"`
}

// Quota is a remote's storage usage in bytes, as reported by `account list
// --quota`. It mirrors engine.Quota by value rather than embedding it, for
// the same reason PairStatus mirrors config.Pair: this package renders what
// commands report and stays free of the domain packages that produce it, so
// the wire shape can never drift because an internal type was refactored.
type Quota struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
	Free  int64 `json:"free"`
}

// AccountStatus is one Google Drive remote, as reported by `account list`.
// Quota is a pointer so it disappears from the JSON entirely when it was not
// requested or could not be read - a zero-valued object inline would be
// indistinguishable from a genuinely empty Drive.
type AccountStatus struct {
	Name       string   `json:"name"`
	Configured bool     `json:"configured"`
	Pairs      []string `json:"pairs"`
	Quota      *Quota   `json:"quota,omitempty"`
}

// AddFormatFlag registers --format on cmd, defaulting to the table format.
func AddFormatFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVar(target, "format", FormatTable, "output format: table|json")
}

// Validate rejects an unknown --format value early, before any work is done.
func Validate(format string) error {
	switch format {
	case FormatTable, FormatJSON:
		return nil
	default:
		return fmt.Errorf("unknown --format %q: want %q or %q", format, FormatTable, FormatJSON)
	}
}

// RenderJSON writes v as indented JSON followed by a newline after redacting
// credential-shaped values from the serialized payload.
func RenderJSON(w io.Writer, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	redacted := runlog.Redact(buf.String())
	_, err := io.WriteString(w, redacted)
	return err
}
