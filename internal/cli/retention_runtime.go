package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/n24q02m/better-drive/internal/artifactcrypto"
	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/credentials"
	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/restore"
	"github.com/n24q02m/better-drive/internal/scheduler"
)

// DestinationCredentialResolver resolves metadata for one exact configured
// destination. Implementations must not return secret material in the binding.
type DestinationCredentialResolver interface {
	Resolve(context.Context, config.Destination) (credentials.Binding, error)
}

// RetentionCoordinator owns retention's provider-facing work: inventory,
// restore-floor/readback checks, leases, journals, signed capabilities, and
// provider mutation. The CLI only hands it a successful transfer outcome and
// the exact destination identity; it never performs source deletes or purges.
type RetentionCoordinator interface {
	Coordinate(context.Context, config.Job, config.Destination, engine.ReplicaOutcome, int, credentials.Binding) error
}

// RuntimeDependencies are optional runtime services injected by the command
// boundary. Production constructors intentionally use the zero value until a
// host wires enrolled credential and retention implementations.
type RuntimeDependencies struct {
	CredentialResolver   DestinationCredentialResolver
	RetentionCoordinator RetentionCoordinator
	ArtifactResolver     artifactcrypto.Resolver
	StagingVerifier      restore.StagingVerifier
	SchedulerAdapter     scheduler.Adapter
}

func (deps RuntimeDependencies) validateForConfig(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	needsRetention := false
	for _, job := range cfg.Jobs {
		for _, destination := range job.Destinations {
			if destination.DeletePolicy == "quarantine" {
				needsRetention = true
				break
			}
		}
		if needsRetention {
			break
		}
	}
	if !needsRetention {
		return nil
	}
	missing := make([]string, 0, 2)
	if deps.CredentialResolver == nil {
		missing = append(missing, "destination credential resolver")
	}
	if deps.RetentionCoordinator == nil {
		missing = append(missing, "retention coordinator")
	}
	if len(missing) > 0 {
		return fmt.Errorf("quarantine retention runtime requires %s", strings.Join(missing, " and "))
	}
	return nil
}

func replicaIDForDestination(jobID string, index int, destination config.Destination) string {
	if destination.RootID != "" {
		return destination.RootID
	}
	return fmt.Sprintf("%s-%d", jobID, index)
}

// applyRetentionRuntime runs the shared post-transfer retention path used by
// one-shot sync and daemon callbacks. It only resolves/coordinates successful
// quarantine replicas, and turns required failures into a job error while
// retaining optional failures as explicit degraded replica outcomes.
func applyRetentionRuntime(ctx context.Context, job config.Job, summary engine.ReplicaSummary, cycleErr error, deps RuntimeDependencies) (engine.ReplicaSummary, error) {
	if err := deps.validateForConfig(&config.Config{Jobs: []config.Job{job}}); err != nil {
		return summary, errors.Join(cycleErr, err)
	}
	if deps.CredentialResolver == nil || deps.RetentionCoordinator == nil {
		return summary, cycleErr
	}

	outcomeByID := make(map[string]int, len(summary.Outcomes))
	for index, outcome := range summary.Outcomes {
		if _, exists := outcomeByID[outcome.ID]; !exists {
			outcomeByID[outcome.ID] = index
		}
	}
	var requiredErrors []error
	for index, destination := range job.Destinations {
		if destination.DeletePolicy != "quarantine" {
			continue
		}
		replicaIndex, ok := outcomeByID[replicaIDForDestination(job.ID, index, destination)]
		if !ok || replicaIndex < 0 || replicaIndex >= len(summary.Outcomes) {
			continue
		}
		outcome := summary.Outcomes[replicaIndex]
		if outcome.Status != "ok" || outcome.Err != nil {
			continue
		}

		binding, err := deps.CredentialResolver.Resolve(ctx, destination)
		if err == nil {
			err = deps.RetentionCoordinator.Coordinate(ctx, job, destination, outcome, destination.MinCompleteRestoreSets, binding)
		}
		if err == nil {
			continue
		}
		err = fmt.Errorf("job %q destination %q retention: %w", job.ID, replicaIDForDestination(job.ID, index, destination), err)
		if destination.Required {
			outcome.Status = "failed"
			requiredErrors = append(requiredErrors, err)
			summary.Status = "failed"
		} else {
			outcome.Status = "degraded"
			if summary.Status != "failed" {
				summary.Status = "degraded"
			}
		}
		outcome.Err = err
		summary.Outcomes[replicaIndex] = outcome
	}
	return summary, errors.Join(cycleErr, errors.Join(requiredErrors...))
}

func summaryOutcomeError(summary engine.ReplicaSummary) error {
	var errs []error
	for _, outcome := range summary.Outcomes {
		if outcome.Err != nil {
			errs = append(errs, outcome.Err)
		}
	}
	return errors.Join(errs...)
}

func pairResultError(summary engine.ReplicaSummary, cycleErr error) string {
	if cycleErr != nil {
		return cycleErr.Error()
	}
	if summary.Status == "degraded" {
		return errorString(summaryOutcomeError(summary))
	}
	return ""
}
