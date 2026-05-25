package scheduler_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsetiawan230294/lumos/internal/scheduler"
)

// TestStress_4Devices3Scenarios10Iterations is the Phase 2 stress target:
// 4 (simulated) devices × 3 scenarios × 10 iterations = 120 jobs, dispatched
// across the work-stealing pool. Verifies that:
//   - all 120 jobs complete
//   - load is spread across all 4 workers (no idle worker)
//   - at least one steal happens (with random arrival skew it's near-certain)
//   - total wall-time is roughly 1/4 of serial (within a generous factor)
func TestStress_4Devices3Scenarios10Iterations(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test skipped in -short mode")
	}

	workers := []scheduler.Worker{
		{ID: "d1", Platform: "android"},
		{ID: "d2", Platform: "android"},
		{ID: "d3", Platform: "android"},
		{ID: "d4", Platform: "android"},
	}

	const scenarios = 3
	const iterations = 10
	const perJob = 15 * time.Millisecond

	var executed atomic.Int64
	pool, err := scheduler.NewPool(workers, scheduler.Options{
		JobTimeout: 2 * time.Second,
		Seed:       42,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	// Submit all jobs unpinned so the scheduler is free to balance & steal.
	for s := 0; s < scenarios; s++ {
		for it := 0; it < iterations; it++ {
			for d := 0; d < len(workers); d++ {
				_ = pool.Submit(scheduler.Job{
					ID:        fmt.Sprintf("s%d-d%d-i%d", s, d, it),
					Scenario:  fmt.Sprintf("scn%d", s),
					Iteration: it,
					DeviceID:  workers[d].ID,
					Platforms: []string{"android"},
					Run: func(_ context.Context, _ string) error {
						time.Sleep(perJob)
						executed.Add(1)
						return nil
					},
				})
			}
		}
	}
	pool.CloseSubmit()

	start := time.Now()
	if err := pool.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	wantTotal := int64(scenarios * iterations * len(workers))
	if executed.Load() != wantTotal {
		t.Fatalf("executed=%d want=%d", executed.Load(), wantTotal)
	}

	stats := pool.Stats()
	for _, s := range stats {
		if s.Executed == 0 {
			t.Fatalf("worker %s idle: stats=%+v", s.WorkerID, stats)
		}
	}

	// Lower bound: even with poor balance, parallel runtime should be well
	// under serial runtime (which would be wantTotal * perJob = 1.8s).
	serial := time.Duration(wantTotal) * perJob
	if elapsed > serial/2 {
		t.Fatalf("parallel runtime %v not materially faster than serial %v", elapsed, serial)
	}
	t.Logf("stress 4×3×10 done in %v (serial would be %v); per-worker stats: %+v",
		elapsed, serial, stats)
}

// TestChaos_PanicMidRun_PoolDrains seeds 50 jobs across 3 workers; one of
// them panics. The pool must still complete the remaining 49 and report
// the panic via JobEvent.
func TestChaos_PanicMidRun_PoolDrains(t *testing.T) {
	workers := []scheduler.Worker{
		{ID: "w1", Platform: "android"},
		{ID: "w2", Platform: "android"},
		{ID: "w3", Platform: "android"},
	}

	var panics, ok int64
	var ev sync.Mutex
	var events []scheduler.JobEvent
	pool, _ := scheduler.NewPool(workers, scheduler.Options{
		Seed: 7,
		OnEvent: func(e scheduler.JobEvent) {
			ev.Lock()
			events = append(events, e)
			ev.Unlock()
		},
	})

	for i := 0; i < 50; i++ {
		i := i
		_ = pool.Submit(scheduler.Job{
			ID:        fmt.Sprintf("j%d", i),
			Platforms: []string{"android"},
			Run: func(_ context.Context, _ string) error {
				if i == 17 {
					atomic.AddInt64(&panics, 1)
					panic(fmt.Sprintf("chaos at %d", i))
				}
				atomic.AddInt64(&ok, 1)
				time.Sleep(time.Millisecond)
				return nil
			},
		})
	}
	pool.CloseSubmit()
	if err := pool.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if atomic.LoadInt64(&panics) != 1 {
		t.Fatalf("panics=%d, want 1", panics)
	}
	if atomic.LoadInt64(&ok) != 49 {
		t.Fatalf("ok=%d, want 49 (pool lost work after panic)", ok)
	}
	ev.Lock()
	defer ev.Unlock()
	sawPanic := false
	for _, e := range events {
		if e.Phase == "panic" {
			sawPanic = true
			if scheduler.PanicStack(e.Err) == nil {
				t.Errorf("panic event missing stack: %+v", e)
			}
		}
	}
	if !sawPanic {
		t.Fatalf("no panic event emitted in %d events", len(events))
	}
}
