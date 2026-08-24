package state

import (
	"encoding/json"
	"os"
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
			OverlapState: OverlapNone, OverlapHealth: "ok", ObservedAt: now,
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
	st := validState(time.Now().UTC())
	st.Scheduler.Health = "mystery"
	if err := st.Validate(); err == nil || !strings.Contains(err.Error(), "health") {
		t.Fatal("unknown scheduler health was accepted")
	}
	st = validState(time.Now().UTC())
	st.Scheduler.FreshnessWindow = 0
	if err := st.Validate(); err == nil || !strings.Contains(err.Error(), "freshness_window") {
		t.Fatal("zero freshness window was accepted")
	}
}

func TestEvaluateSchedulerHealthMapsEveryCanonicalEnum(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	// healthy
	st := validState(now)
	if got := EvaluateSchedulerHealth(st.Scheduler, now); got != HealthHealthy {
		t.Fatalf("healthy = %q, want %q", got, HealthHealthy)
	}

	// disabled
	st.Scheduler.Enabled = false
	if got := EvaluateSchedulerHealth(st.Scheduler, now); got != HealthDisabled {
		t.Fatalf("disabled = %q, want %q", got, HealthDisabled)
	}

	// missing owner
	st = validState(now)
	st.Scheduler.Owner = ""
	if got := EvaluateSchedulerHealth(st.Scheduler, now); got != HealthMissing {
		t.Fatalf("missing owner = %q, want %q", got, HealthMissing)
	}

	// owner mismatch
	st = validState(now)
	st.Scheduler.OverlapHealth = "owner_mismatch"
	if got := EvaluateSchedulerHealth(st.Scheduler, now); got != HealthOwnerMismatch {
		t.Fatalf("owner mismatch = %q, want %q", got, HealthOwnerMismatch)
	}

	// overlap
	st = validState(now)
	st.Scheduler.OverlapState = OverlapMultipleActive
	if got := EvaluateSchedulerHealth(st.Scheduler, now); got != HealthOverlap {
		t.Fatalf("overlap = %q, want %q", got, HealthOverlap)
	}

	// stale observed_at
	st = validState(now)
	st.Scheduler.ObservedAt = now.Add(-16 * time.Minute)
	if got := EvaluateSchedulerHealth(st.Scheduler, now); got != HealthStale {
		t.Fatalf("stale = %q, want %q", got, HealthStale)
	}

	// catch-up within grace stays healthy
	st = validState(now)
	st.Scheduler.NextTrigger = now.Add(-5 * time.Minute)
	if got := EvaluateSchedulerHealth(st.Scheduler, now); got != HealthHealthy {
		t.Fatalf("catch-up within grace = %q, want healthy", got)
	}

	// catch-up beyond grace is stale
	st.Scheduler.NextTrigger = now.Add(-7 * time.Hour)
	if got := EvaluateSchedulerHealth(st.Scheduler, now); got != HealthStale {
		t.Fatalf("catch-up beyond grace = %q, want stale", got)
	}

	// future timestamp requires reconciliation
	st = validState(now)
	st.Scheduler.ObservedAt = now.Add(2 * time.Hour)
	if got := EvaluateSchedulerHealth(st.Scheduler, now); got != HealthNeedsReconciliation {
		t.Fatalf("future observed_at = %q, want %q", got, HealthNeedsReconciliation)
	}

	// explicit readback mismatch requires reconciliation
	st = validState(now)
	st.Scheduler.OverlapHealth = "readback-mismatch"
	if got := EvaluateSchedulerHealth(st.Scheduler, now); got != HealthNeedsReconciliation {
		t.Fatalf("readback mismatch = %q, want %q", got, HealthNeedsReconciliation)
	}

	// unknown overlap
	st = validState(now)
	st.Scheduler.OverlapState = "weird"
	if got := EvaluateSchedulerHealth(st.Scheduler, now); got != HealthUnknown {
		t.Fatalf("unknown overlap = %q, want %q", got, HealthUnknown)
	}
}
func TestLoadMigratesEveryLegacySchedulerTokenAndSaveCanonicalizes(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	healthCases := []struct {
		legacy string
		want   string
	}{
		{HealthHealthy, HealthHealthy},
		{HealthStale, HealthStale},
		{"missing-owner", HealthMissing},
		{"owner-mismatch", HealthOwnerMismatch},
		{HealthDisabled, HealthDisabled},
		{"active-overlap", HealthOverlap},
		{"multiple-active", HealthOverlap},
		{"unknown-overlap", HealthUnknown},
		{"catch-up-within-grace", HealthHealthy},
		{"catch-up-beyond-grace", HealthStale},
		{"readback-mismatch", HealthNeedsReconciliation},
	}
	overlapCases := []struct {
		legacy string
		want   string
	}{
		{"", OverlapNone},
		{"none", OverlapNone},
		{"active", OverlapSingleActive},
		{"single", OverlapSingleActive},
		{"multiple", OverlapMultipleActive},
		{"overlap", OverlapMultipleActive},
		{"unknown", OverlapUnknown},
	}
	for _, healthCase := range healthCases {
		for _, overlapCase := range overlapCases {
			t.Run(healthCase.legacy+"/"+overlapCase.legacy, func(t *testing.T) {
				fixture := validState(now)
				fixture.Scheduler.Health = healthCase.legacy
				fixture.Scheduler.OverlapState = overlapCase.legacy
				fixture.Scheduler.OverlapHealth = healthCase.legacy
				body, err := json.Marshal(fixture)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "state.json")
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
				got, err := Load(path)
				if err != nil {
					t.Fatalf("Load(%q,%q): %v", healthCase.legacy, overlapCase.legacy, err)
				}
				if got.Scheduler.Health != healthCase.want || got.Scheduler.OverlapState != overlapCase.want {
					t.Fatalf("scheduler = %#v, want health=%q overlap=%q", got.Scheduler, healthCase.want, overlapCase.want)
				}
				if got.Scheduler.OwnerJobID != fixture.Scheduler.OwnerJobID || !got.Scheduler.ObservedAt.Equal(fixture.Scheduler.ObservedAt) {
					t.Fatalf("evidence lost: got=%#v want owner_job_id=%q observed_at=%s", got.Scheduler, fixture.Scheduler.OwnerJobID, fixture.Scheduler.ObservedAt)
				}
				if err := Save(path, *got); err != nil {
					t.Fatalf("Save: %v", err)
				}
				canonical, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var saved State
				if err := json.Unmarshal(canonical, &saved); err != nil {
					t.Fatal(err)
				}
				if saved.Scheduler.Health != healthCase.want || saved.Scheduler.OverlapState != overlapCase.want {
					t.Fatalf("saved scheduler = %#v, want canonical tokens", saved.Scheduler)
				}
			})
		}
	}
}
