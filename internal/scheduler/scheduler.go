// Package scheduler implements a work-stealing scheduler that dispatches
// benchmark jobs across a pool of device workers.
//
// Design (Phase 2):
//   - One Worker per device. Each worker owns a deque (slice + mutex).
//   - The owner pops/pushes the BOTTOM of its deque (LIFO local — good cache
//     reuse on the same device).
//   - Idle workers STEAL from the TOP of a sibling's deque (FIFO steal —
//     they grab the oldest, most likely independent work).
//   - Submit() places jobs on the device that matches j.DeviceID; if
//     unspecified, it picks the least-loaded compatible worker.
//   - Affinity guard: a worker will not run a job whose Platforms list
//     excludes its own platform; if it ends up holding one (e.g. via a
//     bad submit) it re-homes the job to a compatible worker.
//   - Bypass when only one worker exists: same code path, just no
//     contention — the lock is uncontended and stealing is a no-op.
//   - Per-job timeout: each Run call is wrapped in context.WithTimeout
//     (configurable via JobTimeout option). Panics in user Run funcs are
//     recovered and reported as errors so one worker crashing does not
//     bring the pool down.
//
// This is intentionally not the lock-free Chase–Lev deque — at the device
// counts Lumos targets (≤32) a sync.Mutex is plenty fast and the code is
// far easier to audit.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// Job is one unit of work the scheduler dispatches to a worker.
type Job struct {
	// ID uniquely identifies the job (used for logs / events).
	ID string

	// Scenario / Iteration are informational labels surfaced in JobEvent.
	Scenario  string
	Iteration int

	// DeviceID, if non-empty, pins the job to that specific worker. A thief
	// will not steal it unless its own ID matches DeviceID.
	DeviceID string

	// Platforms is the set of platforms the job can run on (e.g. {"android"}).
	// Empty == any.
	Platforms []string

	// Run does the actual work. workerID is the device serial that picked
	// up the job. ctx is the scheduler ctx, possibly wrapped with the
	// per-job timeout.
	Run func(ctx context.Context, workerID string) error
}

// Worker describes one device available to the pool.
type Worker struct {
	ID       string
	Platform string
}

// JobEvent is emitted at lifecycle points. Optional sink for observability.
type JobEvent struct {
	Phase    string // "start", "done", "error", "steal", "rehome", "panic"
	JobID    string
	Scenario string
	WorkerID string
	From     string // for steal/rehome: the deque the job was taken/sent from
	Err      error
	Elapsed  time.Duration
}

// Options configures a Pool.
type Options struct {
	// JobTimeout caps any single Job.Run invocation. Zero = no timeout.
	JobTimeout time.Duration
	// OnEvent, if set, is called synchronously for each JobEvent. It must
	// not block; use a buffered channel inside the callback if needed.
	OnEvent func(JobEvent)
	// PostProcessConcurrency caps how many goroutines may hold the
	// post-process semaphore at once (acquired via PostProcess). Zero =
	// unlimited. Used to throttle report writes.
	PostProcessConcurrency int
	// Seed for steal-victim selection. Zero = time-based.
	Seed int64
}

// Pool is the work-stealing scheduler.
type Pool struct {
	opts    Options
	workers []*workerState
	wakeup  chan struct{} // counting "new work" signal

	postSem chan struct{}

	pending atomic.Int64 // total un-finished jobs across all deques + in flight

	rng   *rand.Rand
	rngMu sync.Mutex

	stopOnce sync.Once
	stopped  chan struct{}
}

type workerState struct {
	w         Worker
	mu        sync.Mutex
	deque     []Job
	executed  atomic.Int64
	stolenIn  atomic.Int64
	stolenOut atomic.Int64
}

// NewPool constructs a pool. Workers must be non-empty and have unique IDs.
func NewPool(workers []Worker, opts Options) (*Pool, error) {
	if len(workers) == 0 {
		return nil, errors.New("scheduler: at least one worker required")
	}
	seen := map[string]bool{}
	ws := make([]*workerState, len(workers))
	for i, w := range workers {
		if w.ID == "" {
			return nil, fmt.Errorf("scheduler: worker %d missing ID", i)
		}
		if seen[w.ID] {
			return nil, fmt.Errorf("scheduler: duplicate worker ID %q", w.ID)
		}
		seen[w.ID] = true
		ws[i] = &workerState{w: w}
	}
	seed := opts.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	p := &Pool{
		opts:    opts,
		workers: ws,
		wakeup:  make(chan struct{}, len(ws)*4),
		stopped: make(chan struct{}),
		rng:     rand.New(rand.NewSource(seed)),
	}
	if opts.PostProcessConcurrency > 0 {
		p.postSem = make(chan struct{}, opts.PostProcessConcurrency)
	}
	return p, nil
}

// Submit places a job on a worker's deque. Safe to call before or during Run,
// from any goroutine.
func (p *Pool) Submit(j Job) error {
	if j.Run == nil {
		return errors.New("scheduler: Job.Run is nil")
	}
	target := p.selectTarget(j)
	if target == nil {
		return fmt.Errorf("scheduler: no worker compatible with job %q (platforms=%v device=%q)",
			j.ID, j.Platforms, j.DeviceID)
	}
	target.mu.Lock()
	target.deque = append(target.deque, j)
	target.mu.Unlock()
	p.pending.Add(1)
	p.signal()
	return nil
}

// selectTarget picks the initial worker for a job. Pinned device wins; else
// the least-loaded compatible worker.
func (p *Pool) selectTarget(j Job) *workerState {
	if j.DeviceID != "" {
		for _, w := range p.workers {
			if w.w.ID == j.DeviceID {
				if compatible(j.Platforms, w.w.Platform) {
					return w
				}
				return nil
			}
		}
		return nil
	}
	var best *workerState
	bestN := -1
	for _, w := range p.workers {
		if !compatible(j.Platforms, w.w.Platform) {
			continue
		}
		w.mu.Lock()
		n := len(w.deque)
		w.mu.Unlock()
		if best == nil || n < bestN {
			best, bestN = w, n
		}
	}
	return best
}

func compatible(platforms []string, workerPlatform string) bool {
	if len(platforms) == 0 {
		return true
	}
	for _, p := range platforms {
		if p == workerPlatform {
			return true
		}
	}
	return false
}

func (p *Pool) signal() {
	for range p.workers {
		select {
		case p.wakeup <- struct{}{}:
		default:
			return
		}
	}
}

// Run starts one goroutine per worker and blocks until either:
//   - all submitted jobs have completed AND CloseSubmit has been called, OR
//   - ctx is cancelled.
//
// Returns ctx.Err() on cancellation, nil otherwise.
func (p *Pool) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(len(p.workers))
	for _, w := range p.workers {
		go func(w *workerState) {
			defer wg.Done()
			p.workerLoop(ctx, w)
		}(w)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// CloseSubmit signals that no more jobs will be submitted. Workers exit
// once their deques drain. Safe to call more than once.
func (p *Pool) CloseSubmit() {
	p.stopOnce.Do(func() {
		close(p.stopped)
		p.signal()
	})
}

func (p *Pool) workerLoop(ctx context.Context, w *workerState) {
	for {
		if ctx.Err() != nil {
			return
		}
		j, ok := p.takeLocal(w)
		if !ok {
			j, ok = p.trySteal(w)
		}
		if !ok {
			if p.shouldExit() {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-p.wakeup:
				continue
			case <-time.After(20 * time.Millisecond):
				// periodic guard against missed signals
				continue
			}
		}
		if !compatible(j.Platforms, w.w.Platform) {
			if !p.rehome(w, j) {
				p.emit(JobEvent{Phase: "error", JobID: j.ID, Scenario: j.Scenario,
					WorkerID: w.w.ID, Err: fmt.Errorf("no compatible worker for platforms=%v", j.Platforms)})
				p.pending.Add(-1)
			}
			continue
		}
		p.execute(ctx, w, j)
	}
}

func (p *Pool) shouldExit() bool {
	if p.pending.Load() > 0 {
		return false
	}
	select {
	case <-p.stopped:
		return true
	default:
		return false
	}
}

func (p *Pool) takeLocal(w *workerState) (Job, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(w.deque)
	if n == 0 {
		return Job{}, false
	}
	j := w.deque[n-1]
	w.deque = w.deque[:n-1]
	return j, true
}

func (p *Pool) trySteal(thief *workerState) (Job, bool) {
	for _, vi := range p.shuffleVictims(thief) {
		victim := p.workers[vi]
		victim.mu.Lock()
		if len(victim.deque) == 0 {
			victim.mu.Unlock()
			continue
		}
		taken := -1
		for k, j := range victim.deque {
			if !compatible(j.Platforms, thief.w.Platform) {
				continue
			}
			if j.DeviceID != "" && j.DeviceID != thief.w.ID {
				continue
			}
			taken = k
			break
		}
		if taken < 0 {
			victim.mu.Unlock()
			continue
		}
		j := victim.deque[taken]
		victim.deque = append(victim.deque[:taken], victim.deque[taken+1:]...)
		victim.mu.Unlock()

		victim.stolenOut.Add(1)
		thief.stolenIn.Add(1)
		p.emit(JobEvent{Phase: "steal", JobID: j.ID, Scenario: j.Scenario,
			WorkerID: thief.w.ID, From: victim.w.ID})
		return j, true
	}
	return Job{}, false
}

func (p *Pool) shuffleVictims(self *workerState) []int {
	idx := make([]int, 0, len(p.workers)-1)
	for i, w := range p.workers {
		if w == self {
			continue
		}
		idx = append(idx, i)
	}
	p.rngMu.Lock()
	p.rng.Shuffle(len(idx), func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
	p.rngMu.Unlock()
	return idx
}

// rehome pushes j onto any compatible worker's deque. Returns false if none.
func (p *Pool) rehome(from *workerState, j Job) bool {
	for _, w := range p.workers {
		if w == from {
			continue
		}
		if !compatible(j.Platforms, w.w.Platform) {
			continue
		}
		w.mu.Lock()
		w.deque = append(w.deque, j)
		w.mu.Unlock()
		p.emit(JobEvent{Phase: "rehome", JobID: j.ID, Scenario: j.Scenario,
			WorkerID: w.w.ID, From: from.w.ID})
		p.signal()
		return true
	}
	return false
}

func (p *Pool) execute(ctx context.Context, w *workerState, j Job) {
	jobCtx := ctx
	if p.opts.JobTimeout > 0 {
		var cancel context.CancelFunc
		jobCtx, cancel = context.WithTimeout(ctx, p.opts.JobTimeout)
		defer cancel()
	}
	start := time.Now()
	p.emit(JobEvent{Phase: "start", JobID: j.ID, Scenario: j.Scenario, WorkerID: w.w.ID})

	err := runWithRecover(jobCtx, j, w.w.ID)
	elapsed := time.Since(start)
	w.executed.Add(1)

	phase := "done"
	if err != nil {
		phase = "error"
		if isPanicErr(err) {
			phase = "panic"
		}
	}
	p.emit(JobEvent{Phase: phase, JobID: j.ID, Scenario: j.Scenario, WorkerID: w.w.ID,
		Err: err, Elapsed: elapsed})
	p.pending.Add(-1)
	p.signal()
}

type panicErr struct {
	v     any
	stack []byte
}

func (e *panicErr) Error() string { return fmt.Sprintf("panic: %v", e.v) }

func isPanicErr(err error) bool {
	_, ok := err.(*panicErr)
	return ok
}

// PanicStack returns the captured goroutine stack from a panic error
// produced by a Job, or nil if err did not originate from a panic.
func PanicStack(err error) []byte {
	if pe, ok := err.(*panicErr); ok {
		return pe.stack
	}
	return nil
}

func runWithRecover(ctx context.Context, j Job, workerID string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &panicErr{v: r, stack: debug.Stack()}
		}
	}()
	return j.Run(ctx, workerID)
}

// PostProcess acquires a slot in the backpressure semaphore. Caller must
// invoke the returned release function. If PostProcessConcurrency was zero,
// this is a no-op.
func (p *Pool) PostProcess(ctx context.Context) (release func(), err error) {
	if p.postSem == nil {
		return func() {}, nil
	}
	select {
	case p.postSem <- struct{}{}:
		return func() { <-p.postSem }, nil
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
}

// Stats is a snapshot of per-worker counters, useful for tests and logs.
type Stats struct {
	WorkerID   string
	Executed   int64
	StolenIn   int64
	StolenOut  int64
	QueueDepth int
}

// Stats returns a snapshot of per-worker counters.
func (p *Pool) Stats() []Stats {
	out := make([]Stats, len(p.workers))
	for i, w := range p.workers {
		w.mu.Lock()
		depth := len(w.deque)
		w.mu.Unlock()
		out[i] = Stats{
			WorkerID:   w.w.ID,
			Executed:   w.executed.Load(),
			StolenIn:   w.stolenIn.Load(),
			StolenOut:  w.stolenOut.Load(),
			QueueDepth: depth,
		}
	}
	return out
}

func (p *Pool) emit(ev JobEvent) {
	if p.opts.OnEvent != nil {
		p.opts.OnEvent(ev)
	}
}
