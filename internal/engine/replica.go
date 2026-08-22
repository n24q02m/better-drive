package engine

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Transferer is the engine surface needed by replica orchestration.
type Transferer interface {
	Bisync(BisyncParams) (BisyncResult, error)
	Copy(CopyParams) error
	Sync(CopyParams) error
}

type ReplicaSpec struct {
	ID                     string
	Target                 string
	Workdir                string
	Required               bool
	MinCompleteRestoreSets int
	Resync                 bool
}

type TransferSpec struct {
	Local     string
	Mode      string
	Direction string
	DryRun    bool
	Resync    bool
	Filters   []string
	Context   context.Context
	Stderr    io.Writer
	Replicas  []ReplicaSpec
}

type ReplicaOutcome struct {
	ID       string
	Target   string
	Required bool
	Status   string
	Err      error
}

type ReplicaSummary struct {
	Status   string
	Outcomes []ReplicaOutcome
}

type ReplicaError struct {
	Summary ReplicaSummary
}

func (e *ReplicaError) Error() string {
	if len(e.Summary.Outcomes) == 1 && e.Summary.Outcomes[0].Err != nil {
		return e.Summary.Outcomes[0].Err.Error()
	}
	for _, outcome := range e.Summary.Outcomes {
		if outcome.Required && outcome.Err != nil {
			return fmt.Sprintf("required replica %q failed: %v", outcome.ID, outcome.Err)
		}
	}
	return "required replica failed"
}

func (e *ReplicaError) Unwrap() error {
	for _, outcome := range e.Summary.Outcomes {
		if outcome.Required && outcome.Err != nil {
			return outcome.Err
		}
	}
	return nil
}

func ValidateTransfer(mode, direction string) error {
	switch mode {
	case "copy", "sync", "bisync":
	default:
		return fmt.Errorf("mode must be one of copy|sync|bisync, got %q", mode)
	}
	switch direction {
	case "push", "pull", "bidirectional":
	default:
		return fmt.Errorf("direction must be one of push|pull|bidirectional, got %q", direction)
	}
	if mode == "bisync" && direction != "bidirectional" {
		return fmt.Errorf("bisync requires bidirectional direction")
	}
	if mode != "bisync" && direction == "bidirectional" {
		return fmt.Errorf("mode %s cannot use bidirectional direction", mode)
	}
	return nil
}

func ExecuteReplicas(transferer Transferer, spec TransferSpec) (ReplicaSummary, error) {
	if transferer == nil {
		return ReplicaSummary{}, fmt.Errorf("replica transferer is nil")
	}
	if err := ValidateTransfer(spec.Mode, spec.Direction); err != nil {
		return ReplicaSummary{}, err
	}
	if strings.TrimSpace(spec.Local) == "" {
		return ReplicaSummary{}, fmt.Errorf("transfer local source is required")
	}
	if len(spec.Replicas) == 0 {
		return ReplicaSummary{}, fmt.Errorf("at least one replica is required")
	}
	for _, replica := range spec.Replicas {
		if replica.MinCompleteRestoreSets != 0 && replica.MinCompleteRestoreSets < 2 {
			return ReplicaSummary{}, fmt.Errorf("replica %q min_complete_restore_sets must be >= 2", replica.ID)
		}
	}
	summary := ReplicaSummary{Status: "ok", Outcomes: make([]ReplicaOutcome, 0, len(spec.Replicas))}
	for _, replica := range spec.Replicas {
		outcome := ReplicaOutcome{ID: replica.ID, Target: replica.Target, Required: replica.Required, Status: "ok"}
		if strings.TrimSpace(replica.ID) == "" || strings.TrimSpace(replica.Target) == "" {
			outcome.Status = "failed"
			outcome.Err = fmt.Errorf("replica id and target are required")
			summary.Outcomes = append(summary.Outcomes, outcome)
			if replica.Required {
				summary.Status = "failed"
			}
			continue
		}

		var err error
		switch spec.Mode {
		case "copy":
			local, remote := spec.Local, replica.Target
			if spec.Direction == "pull" {
				local, remote = remote, local
			}
			err = transferer.Copy(CopyParams{Local: local, Remote: remote, Workdir: replica.Workdir, DryRun: spec.DryRun, Filters: spec.Filters, Context: spec.Context, Stderr: spec.Stderr})
		case "sync":
			local, remote := spec.Local, replica.Target
			if spec.Direction == "pull" {
				local, remote = remote, local
			}
			err = transferer.Sync(CopyParams{Local: local, Remote: remote, Workdir: replica.Workdir, DryRun: spec.DryRun, Filters: spec.Filters, Context: spec.Context, Stderr: spec.Stderr})
		case "bisync":
			err = func() error {
				if strings.TrimSpace(replica.Workdir) == "" {
					return fmt.Errorf("replica workdir is required for bisync")
				}
				_, callErr := transferer.Bisync(BisyncParams{Path1: spec.Local, Path2: replica.Target, Workdir: replica.Workdir, Resync: spec.Resync || replica.Resync, DryRun: spec.DryRun, Filters: spec.Filters, Context: spec.Context, Stderr: spec.Stderr})
				return callErr
			}()
		}
		if err != nil {
			outcome.Status = "failed"
			outcome.Err = err
			if replica.Required {
				summary.Status = "failed"
			} else if summary.Status == "ok" {
				summary.Status = "degraded"
			}
		}
		summary.Outcomes = append(summary.Outcomes, outcome)
	}
	if summary.Status == "failed" {
		if len(summary.Outcomes) == 1 && summary.Outcomes[0].Err != nil {
			return summary, summary.Outcomes[0].Err
		}
		return summary, &ReplicaError{Summary: summary}
	}
	return summary, nil
}
