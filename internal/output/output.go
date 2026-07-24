// Package output renders command results either as the human table format or
// as JSON, so the same data serves a person and an agent.
package output

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

const (
	FormatTable = "table"
	FormatJSON  = "json"
)

// PairStatus is one configured sync pair, as reported by `status`.
type PairStatus struct {
	Local    string `json:"local"`
	Remote   string `json:"remote"`
	Mode     string `json:"mode"`
	Interval string `json:"interval"`
}

// PairResult is the outcome of syncing one pair, as reported by `sync`.
type PairResult struct {
	Local  string `json:"local"`
	Remote string `json:"remote"`
	Mode   string `json:"mode"`
	Status string `json:"status"` // ok | failed | skipped
	Error  string `json:"error,omitempty"`
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

// RenderJSON writes v as indented JSON followed by a newline.
func RenderJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
