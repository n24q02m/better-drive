package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/credentials"
	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/spf13/cobra"
)

type retentionTestCredentialResolver struct {
	binding      credentials.Binding
	destinations []config.Destination
	err          error
}

func (r *retentionTestCredentialResolver) Resolve(_ context.Context, destination config.Destination) (credentials.Binding, error) {
	r.destinations = append(r.destinations, destination)
	if r.err != nil {
		return credentials.Binding{}, r.err
	}
	return r.binding, nil
}

type retentionTestCoordinator struct {
	calls []retentionTestCoordCall
	err   error
}

type retentionTestCoordCall struct {
	job         config.Job
	destination config.Destination
	outcome     engine.ReplicaOutcome
	minFloor    int
	binding     credentials.Binding
}

func (c *retentionTestCoordinator) Coordinate(_ context.Context, job config.Job, destination config.Destination, outcome engine.ReplicaOutcome, minFloor int, binding credentials.Binding) error {
	c.calls = append(c.calls, retentionTestCoordCall{job: job, destination: destination, outcome: outcome, minFloor: minFloor, binding: binding})
	return c.err
}

func retentionRuntimeTestCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd
}

func quarantineRuntimeTestJob(local, remote string, required bool) config.Job {
	job := testJob(local, remote)
	job.Destinations[0].Required = required
	job.Destinations[0].DeletePolicy = "quarantine"
	job.Destinations[0].MinCompleteRestoreSets = 3
	return job
}

func TestRunSyncOnceDeleteNoneDoesNotRequireRetentionRuntime(t *testing.T) {
	job := testJob(t.TempDir(), "gdrive:none")
	s := &fakeCLISyncer{}
	results, err := runSyncOnce(retentionRuntimeTestCommand(), s, &config.Config{Jobs: []config.Job{job}}, output.FormatTable, false, false, RuntimeDependencies{})
	if err != nil {
		t.Fatalf("runSyncOnce: %v", err)
	}
	if len(s.copyParams) != 1 {
		t.Fatalf("transfer calls = %d, want one", len(s.copyParams))
	}
	if len(results) != 1 || results[0].Status != "ok" {
		t.Fatalf("results = %#v, want one successful result", results)
	}
}

func TestRunSyncOnceQuarantineMissingDependencyPreflightsBeforeTransfer(t *testing.T) {
	job := quarantineRuntimeTestJob(t.TempDir(), "gdrive:quarantine", true)
	s := &fakeCLISyncer{}
	_, err := runSyncOnce(retentionRuntimeTestCommand(), s, &config.Config{Jobs: []config.Job{job}}, output.FormatTable, false, false, RuntimeDependencies{})
	if err == nil || !strings.Contains(err.Error(), "retention") {
		t.Fatalf("runSyncOnce error = %v, want retention dependency error", err)
	}
	if len(s.copyParams) != 0 {
		t.Fatalf("transfer calls = %#v, want none before dependency preflight", s.copyParams)
	}
}

func TestRunSyncOnceQuarantineInvokesTypedRuntimeAfterTransfer(t *testing.T) {
	job := quarantineRuntimeTestJob(t.TempDir(), "gdrive:quarantine", true)
	binding := credentials.Binding{Provider: "drive", Account: "test-account", Root: "test-root", Bucket: "Backups"}
	resolver := &retentionTestCredentialResolver{binding: binding}
	coordinator := &retentionTestCoordinator{}
	s := &fakeCLISyncer{}
	results, err := runSyncOnce(retentionRuntimeTestCommand(), s, &config.Config{Jobs: []config.Job{job}}, output.FormatTable, false, false, RuntimeDependencies{
		CredentialResolver:   resolver,
		RetentionCoordinator: coordinator,
	})
	if err != nil {
		t.Fatalf("runSyncOnce: %v", err)
	}
	if len(s.copyParams) != 1 {
		t.Fatalf("transfer calls = %d, want one before coordination", len(s.copyParams))
	}
	if len(resolver.destinations) != 1 || resolver.destinations[0] != job.Destinations[0] {
		t.Fatalf("resolver destinations = %#v, want exact destination %#v", resolver.destinations, job.Destinations[0])
	}
	if len(coordinator.calls) != 1 {
		t.Fatalf("coordinator calls = %#v, want one", coordinator.calls)
	}
	call := coordinator.calls[0]
	if call.job.ID != job.ID || call.destination != job.Destinations[0] || call.outcome.Status != "ok" || call.minFloor != job.Destinations[0].MinCompleteRestoreSets || call.binding != binding {
		t.Fatalf("coordinator call = %#v, want exact job/destination/success/floor/binding", call)
	}
	if len(results) != 1 || results[0].Status != "ok" || len(results[0].Replicas) != 1 || results[0].Replicas[0].Status != "ok" {
		t.Fatalf("results = %#v, want successful coordinated result", results)
	}
}

func TestRunSyncOnceRequiredRetentionFailureFailsReplicaAndJob(t *testing.T) {
	job := quarantineRuntimeTestJob(t.TempDir(), "gdrive:quarantine", true)
	coordinator := &retentionTestCoordinator{err: errors.New("journal rejected")}
	resolver := &retentionTestCredentialResolver{binding: credentials.Binding{Provider: "drive", Account: "test-account", Root: "test-root"}}
	s := &fakeCLISyncer{}
	results, err := runSyncOnce(retentionRuntimeTestCommand(), s, &config.Config{Jobs: []config.Job{job}}, output.FormatTable, false, false, RuntimeDependencies{
		CredentialResolver:   resolver,
		RetentionCoordinator: coordinator,
	})
	if err == nil {
		t.Fatal("runSyncOnce succeeded, want required coordinator failure")
	}
	if len(results) != 1 || results[0].Status != "failed" || len(results[0].Replicas) != 1 || results[0].Replicas[0].Status != "failed" || !strings.Contains(results[0].Replicas[0].Error, "journal rejected") {
		t.Fatalf("results = %#v, want failed job and replica", results)
	}
}

func TestRunSyncOnceOptionalRetentionFailureIsDegraded(t *testing.T) {
	job := quarantineRuntimeTestJob(t.TempDir(), "gdrive:quarantine", false)
	coordinator := &retentionTestCoordinator{err: errors.New("provider unavailable")}
	resolver := &retentionTestCredentialResolver{binding: credentials.Binding{Provider: "drive", Account: "test-account", Root: "test-root"}}
	s := &fakeCLISyncer{}
	results, err := runSyncOnce(retentionRuntimeTestCommand(), s, &config.Config{Jobs: []config.Job{job}}, output.FormatTable, false, false, RuntimeDependencies{
		CredentialResolver:   resolver,
		RetentionCoordinator: coordinator,
	})
	if err != nil {
		t.Fatalf("runSyncOnce error = %v, want optional retention error to remain degraded", err)
	}
	if len(results) != 1 || results[0].Status != "degraded" || len(results[0].Replicas) != 1 || results[0].Replicas[0].Status != "degraded" || !strings.Contains(results[0].Replicas[0].Error, "provider unavailable") {
		t.Fatalf("results = %#v, want degraded job and replica with explicit error", results)
	}
}

func TestRunSyncOnceFailedOptionalReplicaIsNotCoordinated(t *testing.T) {
	job := quarantineRuntimeTestJob(t.TempDir(), "gdrive:quarantine", false)
	coordinator := &retentionTestCoordinator{}
	resolver := &retentionTestCredentialResolver{binding: credentials.Binding{Provider: "drive", Account: "test-account", Root: "test-root"}}
	s := &fakeCLISyncer{errByRemote: map[string]error{"gdrive:quarantine": errors.New("transfer failed")}}
	results, err := runSyncOnce(retentionRuntimeTestCommand(), s, &config.Config{Jobs: []config.Job{job}}, output.FormatTable, false, false, RuntimeDependencies{
		CredentialResolver:   resolver,
		RetentionCoordinator: coordinator,
	})
	if err != nil {
		t.Fatalf("runSyncOnce error = %v, want optional transfer failure to remain degraded", err)
	}
	if len(coordinator.calls) != 0 || len(resolver.destinations) != 0 {
		t.Fatalf("runtime calls = resolver=%#v coordinator=%#v, want none for failed optional replica", resolver.destinations, coordinator.calls)
	}
	if len(results) != 1 || results[0].Status != "degraded" || results[0].Replicas[0].Status != "failed" {
		t.Fatalf("results = %#v, want explicit degraded job with failed optional replica", results)
	}
}

func TestRetentionRuntimeHelperMatchesOneShotAndDaemonOutcome(t *testing.T) {
	job := quarantineRuntimeTestJob(t.TempDir(), "gdrive:quarantine", false)
	coordinator := &retentionTestCoordinator{err: errors.New("journal unavailable")}
	resolver := &retentionTestCredentialResolver{binding: credentials.Binding{Provider: "drive", Account: "test-account", Root: "test-root"}}
	deps := RuntimeDependencies{CredentialResolver: resolver, RetentionCoordinator: coordinator}
	summary := engine.ReplicaSummary{Status: "ok", Outcomes: []engine.ReplicaOutcome{{ID: job.Destinations[0].RootID, Target: "gdrive:quarantine", Required: false, Status: "ok"}}}
	gotSummary, gotErr := applyRetentionRuntime(context.Background(), job, summary, nil, deps)
	if gotErr != nil || gotSummary.Status != "degraded" || gotSummary.Outcomes[0].Status != "degraded" {
		t.Fatalf("daemon helper = summary=%#v err=%v, want degraded optional outcome", gotSummary, gotErr)
	}

	s := &fakeCLISyncer{}
	results, oneShotErr := runSyncOnce(retentionRuntimeTestCommand(), s, &config.Config{Jobs: []config.Job{job}}, output.FormatTable, false, false, deps)
	if oneShotErr != nil || len(results) != 1 || results[0].Status != "degraded" || results[0].Replicas[0].Status != "degraded" {
		t.Fatalf("one-shot = results=%#v err=%v, want same degraded outcome", results, oneShotErr)
	}
}

func TestReplicasForJobDefersRestoreFloorToRetentionCoordinator(t *testing.T) {
	job := quarantineRuntimeTestJob(t.TempDir(), "gdrive:quarantine", true)
	replicas, err := replicasForJob(job)
	if err != nil {
		t.Fatalf("replicasForJob: %v", err)
	}
	if len(replicas) != 1 || replicas[0].MinCompleteRestoreSets != 0 {
		t.Fatalf("replicas = %#v, want one transfer replica with floor zero", replicas)
	}
}

func TestRunSyncOnceRequiredCredentialResolutionFailureFailsReplicaAndJob(t *testing.T) {
	job := quarantineRuntimeTestJob(t.TempDir(), "gdrive:quarantine", true)
	resolver := &retentionTestCredentialResolver{err: errors.New("binding unavailable")}
	coordinator := &retentionTestCoordinator{}
	s := &fakeCLISyncer{}
	results, err := runSyncOnce(retentionRuntimeTestCommand(), s, &config.Config{Jobs: []config.Job{job}}, output.FormatTable, false, false, RuntimeDependencies{
		CredentialResolver:   resolver,
		RetentionCoordinator: coordinator,
	})
	if err == nil {
		t.Fatal("runSyncOnce succeeded, want required resolver failure")
	}
	if len(coordinator.calls) != 0 || len(results) != 1 || results[0].Status != "failed" || results[0].Replicas[0].Status != "failed" {
		t.Fatalf("results=%#v coordinator=%#v, want failed replica without coordination", results, coordinator.calls)
	}
}
