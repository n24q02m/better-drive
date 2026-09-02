package engine

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type concurrencyFakeTransferer struct {
	mu           sync.Mutex
	calls        []replicaCall
	active       int32
	maxActive    int32
	backendCalls map[string]int32
	errByTarget  map[string]error
	delay        time.Duration
}

func (f *concurrencyFakeTransferer) CountSourceObjects(context.Context, string, []string, io.Writer) (int64, error) {
	return 0, nil
}
func (f *concurrencyFakeTransferer) Bisync(p BisyncParams) (BisyncResult, error) {
	return f.record("bisync", p.Path1, p.Path2, p.Workdir, f.errByTarget[p.Path2])
}
func (f *concurrencyFakeTransferer) Copy(p CopyParams) error {
	_, err := f.record("copy", p.Local, p.Remote, p.Workdir, f.errByTarget[p.Remote])
	return err
}
func (f *concurrencyFakeTransferer) Sync(p CopyParams) error {
	_, err := f.record("sync", p.Local, p.Remote, p.Workdir, f.errByTarget[p.Remote])
	return err
}
func (f *concurrencyFakeTransferer) record(kind, local, remote, workdir string, err error) (BisyncResult, error) {
	cur := atomic.AddInt32(&f.active, 1)
	for {
		max := atomic.LoadInt32(&f.maxActive)
		if cur > max && atomic.CompareAndSwapInt32(&f.maxActive, max, cur) {
			break
		}
		if cur <= max {
			break
		}
	}
	if f.backendCalls != nil {
		backend := backendOf(remote)
		f.mu.Lock()
		f.backendCalls[backend]++
		f.mu.Unlock()
	}
	f.mu.Lock()
	f.calls = append(f.calls, replicaCall{kind: kind, local: local, remote: remote, workdir: workdir})
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	atomic.AddInt32(&f.active, -1)
	return BisyncResult{}, err
}

func TestExecuteReplicasConcurrentPreservesOrderAndAttemptsAll(t *testing.T) {
	f := &concurrencyFakeTransferer{errByTarget: map[string]error{"r2:optional": errors.New("optional failed")}, delay: 5 * time.Millisecond}
	spec := TransferSpec{
		Local: "C:/source", Mode: "copy", Direction: "push",
		Replicas: []ReplicaSpec{
			{ID: "r1", Target: "gdrive:required", Required: true, Workdir: "wd/r1"},
			{ID: "r2", Target: "r2:optional", Required: false, Workdir: "wd/r2"},
			{ID: "r3", Target: "crypt:required2", Required: true, Workdir: "wd/r3"},
		},
	}
	summary, err := ExecuteReplicasConcurrent(f, spec, ConcurrencyConfig{MaxConcurrent: 2})
	if err != nil {
		t.Fatalf("optional failure should be degraded, got error %v", err)
	}
	if summary.Status != CycleDegraded {
		t.Fatalf("status = %q, want degraded", summary.Status)
	}
	if len(summary.Outcomes) != 3 || summary.Outcomes[0].ID != "r1" || summary.Outcomes[1].ID != "r2" || summary.Outcomes[2].ID != "r3" {
		t.Fatalf("outcomes order = %#v, want input order", summary.Outcomes)
	}
	if len(f.calls) != 3 {
		t.Fatalf("calls = %d, want 3 (every replica attempted)", len(f.calls))
	}
}

func TestExecuteReplicasConcurrentBoundsParallelism(t *testing.T) {
	f := &concurrencyFakeTransferer{errByTarget: map[string]error{}, delay: 10 * time.Millisecond}
	spec := TransferSpec{
		Local: "C:/source", Mode: "copy", Direction: "push",
		Replicas: []ReplicaSpec{
			{ID: "r1", Target: "gdrive:a", Required: true, Workdir: "wd/1"},
			{ID: "r2", Target: "gdrive:b", Required: true, Workdir: "wd/2"},
			{ID: "r3", Target: "gdrive:c", Required: true, Workdir: "wd/3"},
			{ID: "r4", Target: "gdrive:d", Required: true, Workdir: "wd/4"},
		},
	}
	_, err := ExecuteReplicasConcurrent(f, spec, ConcurrencyConfig{MaxConcurrent: 2})
	if err != nil {
		t.Fatalf("ExecuteReplicasConcurrent: %v", err)
	}
	if got := atomic.LoadInt32(&f.maxActive); got > 2 {
		t.Fatalf("max concurrent = %d, want <=2", got)
	}
	if got := atomic.LoadInt32(&f.maxActive); got < 2 {
		t.Fatalf("max concurrent = %d, want to use bounded parallelism (expected 2)", got)
	}
}

func TestExecuteReplicasConcurrentFailsClosedOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &concurrencyFakeTransferer{errByTarget: map[string]error{}}
	spec := TransferSpec{
		Local: "C:/source", Mode: "copy", Direction: "push", Context: ctx,
		Replicas: []ReplicaSpec{
			{ID: "r1", Target: "gdrive:a", Required: true, Workdir: "wd/1"},
			{ID: "r2", Target: "r2:b", Required: false, Workdir: "wd/2"},
		},
	}
	summary, err := ExecuteReplicasConcurrent(f, spec, ConcurrencyConfig{MaxConcurrent: 2})
	if err == nil || !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
		// ExecuteReplicasConcurrent wraps required failures; we assert the
		// summary is failed and no replica is reported ok.
		t.Fatalf("cancelled error = %v, want fail-closed cancellation", err)
	}
	if summary.Status != CycleFailed {
		t.Fatalf("cancelled status = %q, want failed", summary.Status)
	}
	for _, outcome := range summary.Outcomes {
		if outcome.Status != "failed" {
			t.Fatalf("cancelled outcome = %#v, want every replica failed", outcome)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("cancelled pre-spawn calls = %d, want 0", len(f.calls))
	}
}

func TestExecuteReplicasConcurrentValidatesBeforeSpawn(t *testing.T) {
	f := &concurrencyFakeTransferer{errByTarget: map[string]error{}}
	spec := TransferSpec{Local: "C:/source", Mode: "copy", Direction: "bidirectional", Replicas: []ReplicaSpec{{ID: "r1", Target: "gdrive:a", Required: true}}}
	_, err := ExecuteReplicasConcurrent(f, spec, ConcurrencyConfig{MaxConcurrent: 2})
	if err == nil || len(f.calls) != 0 {
		t.Fatalf("validation error = %v calls=%d, want pre-spawn rejection", err, len(f.calls))
	}
}

func TestConcurrencyConfigValidateRequiresExplicitBound(t *testing.T) {
	if err := (ConcurrencyConfig{MaxConcurrent: 0}).Validate(); err == nil || !strings.Contains(err.Error(), ">= 1") {
		t.Fatalf("zero bound error = %v, want explicit bound rejection", err)
	}
	if err := (ConcurrencyConfig{MaxConcurrent: 17}).Validate(); err == nil || !strings.Contains(err.Error(), "<=") {
		t.Fatalf("unbounded error = %v, want upper bound rejection", err)
	}
}
