package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// ConcurrencyConfig bounds parallel replica execution. MaxConcurrent must be
// >=1 so a caller cannot request unbounded goroutines. When MaxConcurrent
// is 0 the caller must have supplied an explicit bound; ExecuteReplicas
// remains the sequential fallback.
type ConcurrencyConfig struct {
	MaxConcurrent int
}

// Validate reports whether c is an explicit bounded configuration.
func (c ConcurrencyConfig) Validate() error {
	if c.MaxConcurrent <= 0 {
		return fmt.Errorf("replica concurrency MaxConcurrent must be >= 1, got %d", c.MaxConcurrent)
	}
	if c.MaxConcurrent > 16 {
		return fmt.Errorf("replica concurrency MaxConcurrent must be <= 16, got %d", c.MaxConcurrent)
	}
	return nil
}

// backendOf extracts the backend prefix from a rclone target (e.g.
// "gdrive:Backups/foo" -> "gdrive"). Used only for per-backend accounting;
// it never decides success.
func backendOf(target string) string {
	backend, _, found := strings.Cut(target, ":")
	if !found {
		return strings.ToLower(strings.TrimSpace(target))
	}
	return strings.ToLower(strings.TrimSpace(backend))
}

// ExecuteReplicasConcurrent attempts every replica with bounded parallelism
// while preserving per-destination order (output order equals input order).
// Validation (mode/direction/restore floor/required bits) runs before any
// transfer so a misconfigured job fails before endpoint resolution. A
// cancelled context fails closed with CycleCancelled semantics and no replica
// is reported as ok.
func ExecuteReplicasConcurrent(transferer Transferer, spec TransferSpec, concurrency ConcurrencyConfig) (ReplicaSummary, error) {
	if transferer == nil {
		return ReplicaSummary{}, fmt.Errorf("replica transferer is nil")
	}
	if err := concurrency.Validate(); err != nil {
		return ReplicaSummary{}, err
	}
	if err := ValidateTransfer(spec.Mode, spec.Direction); err != nil {
		return ReplicaSummary{}, err
	}
	if spec.Mode == "sync" || spec.Mode == "bisync" {
		if spec.Direction == "pull" {
			return ReplicaSummary{}, fmt.Errorf("destructive transfer requires push-side source evidence; pull direction is not enrolled")
		}
		if spec.SourceWasNonEmpty == nil || spec.SourceObjectCount == nil {
			return ReplicaSummary{}, fmt.Errorf("destructive transfer source safety evidence is required")
		}
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
		if replica.MinCompleteRestoreSets >= 2 {
			if err := ValidateRestoreFloor(replica.MinCompleteRestoreSets, replica.RestoreAcks); err != nil {
				return ReplicaSummary{}, fmt.Errorf("replica %q restore floor: %w", replica.ID, err)
			}
		}
	}
	// Bounded per-backend concurrency while preserving per-destination order.
	// A global semaphore caps total goroutines; a per-backend semaphore caps
	// parallel transfers to the same backend so one backend cannot starve
	// others. Both are bounded by concurrency.MaxConcurrent so a caller cannot
	// create unbounded parallelism through many distinct backends.
	backendSem := make(map[string]chan struct{})
	for _, replica := range spec.Replicas {
		backend := backendOf(replica.Target)
		if _, exists := backendSem[backend]; !exists {
			backendSem[backend] = make(chan struct{}, concurrency.MaxConcurrent)
		}
	}
	globalSem := make(chan struct{}, concurrency.MaxConcurrent)

	type indexedOutcome struct {
		index   int
		outcome ReplicaOutcome
	}
	results := make(chan indexedOutcome, len(spec.Replicas))
	var wg sync.WaitGroup
	ctx := spec.Context
	if ctx == nil {
		ctx = context.Background()
	}
	// Capture cancellation before spawning so a pre-cancelled context fails
	// closed without spawning any transfer.
	if err := ctx.Err(); err != nil {
		summary := ReplicaSummary{Status: CycleFailed, Outcomes: make([]ReplicaOutcome, 0, len(spec.Replicas))}
		for _, replica := range spec.Replicas {
			summary.Outcomes = append(summary.Outcomes, ReplicaOutcome{ID: replica.ID, Target: replica.Target, Required: replica.Required, Status: "failed", Err: err})
		}
		summary.Status = CycleFailed
		return summary, &ReplicaError{Summary: summary}
	}

	for idx, replica := range spec.Replicas {
		idx, replica := idx, replica
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Acquire global and per-backend slots. Release via defer so
			// cancellation or panic never leaks a semaphore.
			globalSem <- struct{}{}
			defer func() { <-globalSem }()
			backend := backendOf(replica.Target)
			sem := backendSem[backend]
			sem <- struct{}{}
			defer func() { <-sem }()

			// Fail closed on context cancellation before transfer.
			if err := ctx.Err(); err != nil {
				results <- indexedOutcome{index: idx, outcome: ReplicaOutcome{ID: replica.ID, Target: replica.Target, Required: replica.Required, Status: "failed", Err: err}}
				return
			}
			if strings.TrimSpace(replica.ID) == "" || strings.TrimSpace(replica.Target) == "" {
				results <- indexedOutcome{index: idx, outcome: ReplicaOutcome{ID: replica.ID, Target: replica.Target, Required: replica.Required, Status: "failed", Err: fmt.Errorf("replica id and target are required")}}
				return
			}
			var err error
			switch spec.Mode {
			case "copy":
				local, remote := spec.Local, replica.Target
				if spec.Direction == "pull" {
					local, remote = remote, local
				}
				err = transferer.Copy(CopyParams{Local: local, Remote: remote, Workdir: replica.Workdir, SourceWasNonEmpty: spec.SourceWasNonEmpty, SourceObjectCount: spec.SourceObjectCount, DeleteBudget: spec.DeleteBudget, DryRun: spec.DryRun, Filters: spec.Filters, Context: ctx, Stderr: spec.Stderr})
			case "sync":
				local, remote := spec.Local, replica.Target
				if spec.Direction == "pull" {
					local, remote = remote, local
				}
				err = transferer.Sync(CopyParams{Local: local, Remote: remote, Workdir: replica.Workdir, SourceWasNonEmpty: spec.SourceWasNonEmpty, SourceObjectCount: spec.SourceObjectCount, DeleteBudget: spec.DeleteBudget, DryRun: spec.DryRun, Filters: spec.Filters, Context: ctx, Stderr: spec.Stderr})
			case "bisync":
				if strings.TrimSpace(replica.Workdir) == "" {
					err = fmt.Errorf("replica workdir is required for bisync")
					break
				}
				_, err = transferer.Bisync(BisyncParams{Path1: spec.Local, Path2: replica.Target, Workdir: replica.Workdir, Resync: spec.Resync || replica.Resync, SourceWasNonEmpty: spec.SourceWasNonEmpty, SourceObjectCount: spec.SourceObjectCount, DeleteBudget: spec.DeleteBudget, DryRun: spec.DryRun, Filters: spec.Filters, Context: ctx, Stderr: spec.Stderr})
			}
			outcome := ReplicaOutcome{ID: replica.ID, Target: replica.Target, Required: replica.Required, Status: "ok"}
			if err != nil {
				// Map context cancellation to fail-closed status; do not report
				// degraded for optional replicas when the whole cycle was
				// cancelled.
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				outcome.Status = "failed"
				outcome.Err = err
			}
			results <- indexedOutcome{index: idx, outcome: outcome}
		}()
	}
	wg.Wait()
	close(results)

	ordered := make([]ReplicaOutcome, len(spec.Replicas))
	for r := range results {
		ordered[r.index] = r.outcome
	}
	summary := ReplicaSummary{Status: CycleOK, Outcomes: ordered}
	hasRequiredFailure := false
	hasOptionalFailure := false
	for _, outcome := range ordered {
		if outcome.Status == "failed" {
			if outcome.Required {
				hasRequiredFailure = true
			} else {
				hasOptionalFailure = true
			}
		}
	}
	switch {
	case hasRequiredFailure:
		summary.Status = CycleFailed
	case hasOptionalFailure:
		summary.Status = CycleDegraded
	default:
		summary.Status = CycleOK
	}
	if hasRequiredFailure {
		return summary, &ReplicaError{Summary: summary}
	}
	return summary, nil
}
