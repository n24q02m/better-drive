package state

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validState(now time.Time) State {
	return State{
		SchemaVersion: CurrentSchemaVersion,
		EngineVersion: "1.6.0",
		Jobs:          []JobState{{JobID: "job-1", Status: "ok", LastSuccess: now, ObjectCount: 3, ByteCount: 42}},
		Scheduler: SchedulerState{
			Owner: "task-scheduler", OwnerJobID: "job-1", Enabled: true, ActiveInstance: "instance-1",
			OverlapState: "none", OverlapHealth: "ok", ObservedAt: now,
			FreshnessWindow: 15 * time.Minute, CatchUpGrace: 6*time.Hour + 15*time.Minute, Health: HealthHealthy,
		},
	}
}

func TestStateRoundTripPreservesVersionedJobAndSchedulerEvidence(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "state.json")
	want := validState(now)
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.SchemaVersion != CurrentSchemaVersion || got.Jobs[0].JobID != "job-1" || got.Scheduler.OwnerJobID != "job-1" {
		t.Fatalf("state = %#v, want persisted evidence", got)
	}
}

func TestStateRejectsUnknownHealthAndMissingFreshness(t *testing.T) {
	state := validState(time.Now().UTC())
	state.Scheduler.Health = "mystery"
	if err := state.Validate(); err == nil || !strings.Contains(err.Error(), "health") {
		t.Fatal("unknown scheduler health was accepted")
	}
	state = validState(time.Now().UTC())
	state.Scheduler.FreshnessWindow = 0
	if err := state.Validate(); err == nil || !strings.Contains(err.Error(), "freshness_window") {
		t.Fatal("zero freshness window was accepted")
	}
}

func TestEvaluateSchedulerHealthMapsFreshStaleOverlapAndCatchupStates(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	state := validState(now)
	if got := EvaluateSchedulerHealth(state.Scheduler, now); got != HealthHealthy {
		t.Fatalf("fresh health = %q, want healthy", got)
	}
	state.Scheduler.ObservedAt = now.Add(-16 * time.Minute)
	if got := EvaluateSchedulerHealth(state.Scheduler, now); got != HealthStale {
		t.Fatalf("stale health = %q, want stale", got)
	}
	state.Scheduler = validState(now).Scheduler
	state.Scheduler.OverlapState = "multiple"
	if got := EvaluateSchedulerHealth(state.Scheduler, now); got != HealthMultipleActive {
		t.Fatalf("multiple health = %q, want multiple-active", got)
	}
	state.Scheduler = validState(now).Scheduler
	state.Scheduler.NextTrigger = now.Add(-5 * time.Minute)
	if got := EvaluateSchedulerHealth(state.Scheduler, now); got != HealthCatchUpWithinGrace {
		t.Fatalf("catch-up health = %q, want catch-up-within-grace", got)
	}
	state.Scheduler.NextTrigger = now.Add(-7 * time.Hour)
	if got := EvaluateSchedulerHealth(state.Scheduler, now); got != HealthCatchUpBeyondGrace {
		t.Fatalf("late catch-up health = %q, want catch-up-beyond-grace", got)
	}
}
