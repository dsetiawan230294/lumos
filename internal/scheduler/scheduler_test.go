package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- helpers -----------------------------------------------------------------

func newTestPool(t *testing.T, workers []Worker, opts Options) *Pool {
	t.Helper()
	if opts.Seed == 0 {
		opts.Seed = 1 // deterministic in tests
	}
	p, err := NewPool(workers, opts)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	return p
}

func mkJob(id string, run func(ctx context.Context, workerID string) error) Job {
	return Job{ID: id, Scenario: id, Run: run}
}

// --- tests -------------------------------------------------------------------

func TestSingleWorker_RunsAllJobs(t *testing.T) {
	p := newTestPool(t, []Worker{{ID: "w1", Platform: "android"}}, Options{})

	var n atomic.Int32
	for i := 0; i < 10; i++ {
		if err := p.Submit(mkJob(fmt.Sprintf("j%d", i), func(_ context.Context, _ string) error {
			n.Add(1)
			return nil
		})); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}
	p.CloseSubmit()
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := n.Load(); got != 10 {
		t.Fatalf("executed = %d, want 10", got)
	}
	stats := p.Stats()
	if stats[0].Executed != 10 {
		t.Fatalf("worker executed = %d, want 10", stats[0].Executed)
	}
	if stats[0].StolenIn != 0 || stats[0].StolenOut != 0 {
		t.Fatalf("single worker should not steal: %+v", stats[0])
	}
}

func TestMultiWorker_StealsFromBusy(t *testing.T) {
	// Two workers; pin all 8 jobs to w1. w2 must steal at least some.
	workers := []Worker{
		{ID: "w1", Platform: "android"},
		{ID: "w2", Platform: "android"},
	}
	p := newTestPool(t, workers, Options{})

	done := make(chan string, 8)
	for i := 0; i < 8; i++ {
		j := Job{
			ID:        fmt.Sprintf("j%d", i),
			DeviceID:  "", // unpinned so it can be stolen
			Platforms: []string{"android"},
			Run: func(_ context.Context, wid string) error {
				time.Sleep(20 * time.Millisecond)
				done <- wid
				return nil
			},
		}
		// Force initial placement on w1 by pinning, then unpinning before steal.
		// Instead: submit raw to w1's deque directly via Submit with pinning,
		// then have a thief allowed to steal (DeviceID=="" allows steal).
		j.DeviceID = ""
		// Pre-load w1 only by giving it cheaper jobs first? Simpler: submit all
		// via low-level path that places on w1 explicitly.
		w1 := p.workers[0]
		w1.mu.Lock()
		w1.deque = append(w1.deque, j)
		w1.mu.Unlock()
		p.pending.Add(1)
	}
	p.signal()
	p.CloseSubmit()
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(done)

	counts := map[string]int{}
	for w := range done {
		counts[w]++
	}
	if counts["w1"]+counts["w2"] != 8 {
		t.Fatalf("total executed = %d, want 8 (counts=%v)", counts["w1"]+counts["w2"], counts)
	}
	if counts["w2"] == 0 {
		t.Fatalf("w2 stole zero jobs from w1 (counts=%v)", counts)
	}
	stats := p.Stats()
	var totalStolen int64
	for _, s := range stats {
		totalStolen += s.StolenIn
	}
	if totalStolen == 0 {
		t.Fatalf("expected non-zero steals; stats=%+v", stats)
	}
}

func TestAffinity_OnlyCompatibleStealsAndRuns(t *testing.T) {
	workers := []Worker{
		{ID: "a1", Platform: "android"},
		{ID: "i1", Platform: "ios"},
	}
	p := newTestPool(t, workers, Options{})

	var androidRuns atomic.Int32
	var iosRuns atomic.Int32

	for i := 0; i < 4; i++ {
		_ = p.Submit(Job{
			ID:        fmt.Sprintf("a%d", i),
			Platforms: []string{"android"},
			Run: func(_ context.Context, wid string) error {
				if wid != "a1" {
					t.Errorf("android job ran on %s", wid)
				}
				androidRuns.Add(1)
				return nil
			},
		})
		_ = p.Submit(Job{
			ID:        fmt.Sprintf("i%d", i),
			Platforms: []string{"ios"},
			Run: func(_ context.Context, wid string) error {
				if wid != "i1" {
					t.Errorf("ios job ran on %s", wid)
				}
				iosRuns.Add(1)
				return nil
			},
		})
	}
	p.CloseSubmit()
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if androidRuns.Load() != 4 || iosRuns.Load() != 4 {
		t.Fatalf("android=%d ios=%d, want 4/4", androidRuns.Load(), iosRuns.Load())
	}
}

func TestDeviceIDPinning_NotStolen(t *testing.T) {
	workers := []Worker{
		{ID: "w1", Platform: "android"},
		{ID: "w2", Platform: "android"},
	}
	p := newTestPool(t, workers, Options{})

	// Pin all to w1; verify w2 never runs them.
	var w2Saw atomic.Int32
	for i := 0; i < 5; i++ {
		_ = p.Submit(Job{
			ID:        fmt.Sprintf("p%d", i),
			DeviceID:  "w1",
			Platforms: []string{"android"},
			Run: func(_ context.Context, wid string) error {
				if wid != "w1" {
					w2Saw.Add(1)
				}
				time.Sleep(5 * time.Millisecond)
				return nil
			},
		})
	}
	p.CloseSubmit()
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if w2Saw.Load() != 0 {
		t.Fatalf("pinned jobs ran on wrong worker %d times", w2Saw.Load())
	}
	stats := p.Stats()
	for _, s := range stats {
		if s.WorkerID == "w2" && s.Executed > 0 {
			t.Fatalf("w2 should have executed 0 pinned-to-w1 jobs, got %d", s.Executed)
		}
	}
}

func TestPanicInJob_Recovered_PoolContinues(t *testing.T) {
	workers := []Worker{
		{ID: "w1", Platform: "android"},
		{ID: "w2", Platform: "android"},
	}
	var events []JobEvent
	var emu sync.Mutex
	p := newTestPool(t, workers, Options{
		OnEvent: func(e JobEvent) {
			emu.Lock()
			events = append(events, e)
			emu.Unlock()
		},
	})

	var okRuns atomic.Int32
	_ = p.Submit(Job{ID: "boom", Platforms: []string{"android"}, Run: func(_ context.Context, _ string) error {
		panic("kapow")
	}})
	for i := 0; i < 5; i++ {
		_ = p.Submit(mkJob(fmt.Sprintf("ok%d", i), func(_ context.Context, _ string) error {
			okRuns.Add(1)
			return nil
		}))
	}
	p.CloseSubmit()
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if okRuns.Load() != 5 {
		t.Fatalf("subsequent jobs lost after panic: ran %d/5", okRuns.Load())
	}
	emu.Lock()
	defer emu.Unlock()
	sawPanic := false
	for _, e := range events {
		if e.Phase == "panic" && e.JobID == "boom" {
			sawPanic = true
			if stk := PanicStack(e.Err); len(stk) == 0 {
				t.Errorf("panic event missing stack")
			}
		}
	}
	if !sawPanic {
		t.Fatalf("no panic event emitted; got %d events", len(events))
	}
}

func TestJobTimeout_CancelsRunCtx(t *testing.T) {
	p := newTestPool(t, []Worker{{ID: "w1", Platform: "android"}}, Options{
		JobTimeout: 30 * time.Millisecond,
	})
	var observedErr error
	var mu sync.Mutex
	_ = p.Submit(Job{ID: "slow", Run: func(ctx context.Context, _ string) error {
		select {
		case <-ctx.Done():
			mu.Lock()
			observedErr = ctx.Err()
			mu.Unlock()
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return errors.New("not cancelled")
		}
	}})
	p.CloseSubmit()
	start := time.Now()
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("timeout did not fire: ran %v", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if !errors.Is(observedErr, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded inside job, got %v", observedErr)
	}
}

func TestCancellation_StopsPool(t *testing.T) {
	p := newTestPool(t, []Worker{{ID: "w1", Platform: "android"}}, Options{})
	_ = p.Submit(Job{ID: "block", Run: func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	err := p.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want Canceled", err)
	}
}

func TestPostProcess_LimitsConcurrency(t *testing.T) {
	p := newTestPool(t, []Worker{
		{ID: "w1", Platform: "android"},
		{ID: "w2", Platform: "android"},
		{ID: "w3", Platform: "android"},
	}, Options{PostProcessConcurrency: 1})

	var inCrit atomic.Int32
	var maxObs atomic.Int32
	for i := 0; i < 6; i++ {
		_ = p.Submit(mkJob(fmt.Sprintf("j%d", i), func(ctx context.Context, _ string) error {
			rel, err := p.PostProcess(ctx)
			if err != nil {
				return err
			}
			defer rel()
			n := inCrit.Add(1)
			for {
				cur := maxObs.Load()
				if n <= cur || maxObs.CompareAndSwap(cur, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			inCrit.Add(-1)
			return nil
		}))
	}
	p.CloseSubmit()
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if maxObs.Load() != 1 {
		t.Fatalf("PostProcess concurrency = %d, want 1", maxObs.Load())
	}
}

func TestSubmit_NoCompatibleWorker(t *testing.T) {
	p := newTestPool(t, []Worker{{ID: "w1", Platform: "android"}}, Options{})
	err := p.Submit(Job{ID: "x", Platforms: []string{"ios"}, Run: func(_ context.Context, _ string) error { return nil }})
	if err == nil {
		t.Fatalf("expected error submitting ios job to android-only pool")
	}
}
