package runner

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dsetiawan230294/lumos/internal/automation"
	"github.com/dsetiawan230294/lumos/internal/config"
	"github.com/dsetiawan230294/lumos/internal/metrics"
	"github.com/dsetiawan230294/lumos/internal/report"
)

// PlanInput describes what RunScenario needs in addition to the YAML config.
type PlanInput struct {
	Scenario  config.Scenario
	DeviceID  string
	Platform  metrics.Platform
	AppID     string
	OutDir    string
	Tool      string
	Version   string
	HarnessPy string            // path to harness.py
	PyPath    string            // dir to prepend to PYTHONPATH (vendored helper)
	PythonBin string            // default "python3"
	ExtraEnv  map[string]string // extra env passed to the scenario subprocess

	// NewSampler is called once per iteration to construct a fresh sampler
	// bound to ctx. Tests can inject a fake here.
	NewSampler func() Sampler

	// NewTrace, if non-nil, is called once per measured iteration to
	// construct a side-channel trace capture (e.g. Perfetto). Warmup
	// iterations skip tracing to keep the artefact count manageable.
	NewTrace func() TraceCapture

	// Cooldown override; if zero, Scenario.CooldownSec is used.
	Cooldown time.Duration
}

// IterationResult is the per-iteration outcome.
type IterationResult struct {
	Iteration  int
	ReportPath string
	Warmup     bool
	Err        error
}

// RunScenario executes warmup + measured iterations of a single scenario on a
// single device. Each iteration is a fresh sampler + scenario subprocess +
// JSON report. Warmup iterations are still executed (so caches/JIT warm up)
// but their reports are tagged and can be filtered out later.
func RunScenario(ctx context.Context, in PlanInput) ([]IterationResult, error) {
	return RunScenarioWithStderr(ctx, in, nil)
}

// RunScenarioWithStderr is like RunScenario but pipes the Python subprocess's
// stderr to the given writer (useful for surfacing scenario crashes during
// real-device runs).
func RunScenarioWithStderr(ctx context.Context, in PlanInput, stderr io.Writer) ([]IterationResult, error) {
	if in.NewSampler == nil {
		return nil, fmt.Errorf("RunScenario: NewSampler required")
	}
	if in.Scenario.Iterations < 1 {
		return nil, fmt.Errorf("RunScenario: scenario.iterations must be >= 1")
	}
	cooldown := in.Cooldown
	if cooldown <= 0 {
		cooldown = in.Scenario.CooldownSec
	}

	if in.Scenario.InProcessIterations {
		return runInProcess(ctx, in, stderr)
	}

	total := in.Scenario.Warmup + in.Scenario.Iterations
	results := make([]IterationResult, 0, total)

	// Measured-phase start time, used to honor Scenario.Timebox: after the
	// configured Iterations finish, we keep iterating until elapsed >=
	// Timebox. Set when we transition out of warmup.
	var measuredStart time.Time

	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		warmup := i < in.Scenario.Warmup
		iteration := i + 1
		if !warmup {
			iteration = i - in.Scenario.Warmup + 1
			if measuredStart.IsZero() {
				measuredStart = time.Now()
			}
		}

		// Stop condition: ran at least Iterations measured passes AND
		// (no timebox OR timebox elapsed). When Timebox==0 this collapses
		// to the historical "stop after Iterations" behavior.
		if !warmup && iteration > in.Scenario.Iterations {
			if in.Scenario.Timebox <= 0 || time.Since(measuredStart) >= in.Scenario.Timebox {
				break
			}
		}

		job := Job{
			Scenario:  scenarioLabel(in.Scenario.Name, warmup),
			Iteration: iteration,
			DeviceID:  in.DeviceID,
			Platform:  in.Platform,
			AppID:     in.AppID,
			Sampler:   in.NewSampler(),
			OutDir:    in.OutDir,
			Tool:      in.Tool,
			Version:   in.Version,
			Stderr:    stderr,
			Scenario_: automation.ScenarioOpts{
				PythonBin:  in.PythonBin,
				HarnessPy:  in.HarnessPy,
				ScriptPath: in.Scenario.Script,
				Env:        mergeEnv(in.ExtraEnv, in.PyPath),
				Stderr:     stderr,
			},
		}
		if !warmup && in.NewTrace != nil {
			job.Trace = in.NewTrace()
		}

		res, err := Run(ctx, job)
		results = append(results, IterationResult{
			Iteration:  iteration,
			ReportPath: res.ReportPath,
			Warmup:     warmup,
			Err:        err,
		})
		// Don't abort the whole scenario on a single failure — record and
		// continue, mirroring CI-friendly behaviour. Caller decides whether
		// to bail based on err count in results.

		// Cooldown between iterations. We sleep after every iteration; the
		// loop's stop-condition above will short-circuit the next pass when
		// done so this doesn't add a trailing sleep at the end.
		if cooldown > 0 {
			select {
			case <-time.After(cooldown):
			case <-ctx.Done():
				return results, ctx.Err()
			}
		}
	}
	return results, nil
}

func scenarioLabel(name string, warmup bool) string {
	if warmup {
		return name + "__warmup"
	}
	return name
}

func mergeEnv(extra map[string]string, pyPath string) map[string]string {
	out := map[string]string{}
	for k, v := range extra {
		out[k] = v
	}
	if pyPath != "" {
		// Prepend, preserving any caller-supplied PYTHONPATH.
		if existing, ok := out["PYTHONPATH"]; ok && existing != "" {
			out["PYTHONPATH"] = pyPath + string(filepath.ListSeparator) + existing
		} else {
			out["PYTHONPATH"] = pyPath
		}
	}
	return out
}

// runInProcess spawns the harness exactly once for the whole scenario.
// The harness opens the driver, calls setup() once, loops run() N times
// emitting iterStart/iterEnd markers, then calls teardown(). A single
// sampler runs continuously for the entire subprocess lifetime; after
// the subprocess exits we slice samples by the per-iteration markers and
// write one JSON report per iteration. Use this for scenarios where
// re-opening the driver every iteration is too expensive (e.g. cold
// install + login per iteration).
//
// Timebox, NewTrace, and per-iteration cooldown are NOT supported in
// this mode — they assume per-iteration subprocess boundaries.
func runInProcess(ctx context.Context, in PlanInput, stderr io.Writer) ([]IterationResult, error) {
	if in.NewTrace != nil {
		fmt.Fprintln(stderrOr(stderr), "lumos: in_process_iterations: side-channel trace ignored (unsupported in this mode)")
	}

	total := in.Scenario.Warmup + in.Scenario.Iterations

	// Set up env so the harness loops internally.
	env := mergeEnv(in.ExtraEnv, in.PyPath)
	env["LUMOS_ITER_MODE"] = "in-process"
	env["LUMOS_ITER_TOTAL"] = strconv.Itoa(total)
	env["LUMOS_ITER_WARMUP"] = strconv.Itoa(in.Scenario.Warmup)
	if in.Scenario.Timebox > 0 {
		env["LUMOS_ITER_TIMEBOX_MS"] = strconv.FormatInt(
			in.Scenario.Timebox.Milliseconds(), 10,
		)
	}

	// Start a single long-running sampler for the whole subprocess.
	startedAt := time.Now()
	sampleCtx, stopSampling := context.WithCancel(ctx)
	defer stopSampling()

	sampler := in.NewSampler()
	samples, err := sampler.Sample(sampleCtx)
	if err != nil {
		return nil, fmt.Errorf("runner: start sampler: %w", err)
	}
	collected := make([]metrics.Sample, 0, 1024)
	drained := make(chan struct{})
	go func() {
		for s := range samples {
			collected = append(collected, s)
		}
		close(drained)
	}()

	autoRes := automation.Run(ctx, automation.ScenarioOpts{
		PythonBin:  in.PythonBin,
		HarnessPy:  in.HarnessPy,
		ScriptPath: in.Scenario.Script,
		DeviceID:   in.DeviceID,
		Platform:   string(in.Platform),
		AppID:      in.AppID,
		Iteration:  0, // unused in in-process mode (harness loops itself)
		Env:        env,
		Stderr:     stderr,
	})

	stopSampling()
	<-drained
	endedAt := time.Now()

	// Slice samples + markers by iter_start/iter_end marker pairs.
	type window struct {
		iteration int
		warmup    bool
		start     time.Time
		end       time.Time
		label     string
	}
	var windows []window
	open := map[string]*window{} // label → in-flight window
	for _, m := range autoRes.Markers {
		switch m.Kind {
		case "iter_start":
			it, warmup := parseIterLabel(m.Label)
			open[m.Label] = &window{
				iteration: it,
				warmup:    warmup,
				start:     m.T,
				label:     m.Label,
			}
		case "iter_end":
			if w, ok := open[m.Label]; ok {
				w.end = m.T
				windows = append(windows, *w)
				delete(open, m.Label)
			}
		}
	}
	// Drop any never-closed windows (subprocess crashed mid-iteration)
	// after recording them as failed iterations with end=endedAt.
	for _, w := range open {
		w.end = endedAt
		windows = append(windows, *w)
	}
	// Stable ordering: warmup first by iteration, then measured by iteration.
	sort.Slice(windows, func(i, j int) bool {
		if windows[i].warmup != windows[j].warmup {
			return windows[i].warmup
		}
		return windows[i].iteration < windows[j].iteration
	})

	results := make([]IterationResult, 0, len(windows))
	for _, w := range windows {
		runRec := metrics.Run{
			Scenario:  scenarioLabel(in.Scenario.Name, w.warmup),
			Iteration: w.iteration,
			DeviceID:  in.DeviceID,
			Platform:  in.Platform,
			StartedAt: w.start,
			EndedAt:   w.end,
			Samples:   sliceSamples(collected, w.start, w.end),
			Markers:   sliceMarkers(autoRes.Markers, w.start, w.end),
		}
		// Surface the subprocess-level error on every iteration record
		// when the harness aborted mid-loop, so callers see the failure.
		if autoRes.Err != nil && w.end.Equal(endedAt) {
			runRec.Error = autoRes.Err.Error()
		}
		var path string
		if in.OutDir != "" {
			path, err = report.WriteRun(in.OutDir, in.Tool, in.Version, runRec)
			if err != nil {
				return results, fmt.Errorf("runner: write report: %w", err)
			}
		}
		results = append(results, IterationResult{
			Iteration:  w.iteration,
			ReportPath: path,
			Warmup:     w.warmup,
			Err:        nil,
		})
	}

	// If the harness errored before emitting any iterStart, surface a
	// single failed iteration so the caller's err count is non-zero.
	if len(results) == 0 && autoRes.Err != nil {
		runRec := metrics.Run{
			Scenario:  in.Scenario.Name,
			Iteration: 1,
			DeviceID:  in.DeviceID,
			Platform:  in.Platform,
			StartedAt: startedAt,
			EndedAt:   endedAt,
			Samples:   collected,
			Markers:   autoRes.Markers,
			Error:     autoRes.Err.Error(),
		}
		var path string
		if in.OutDir != "" {
			path, _ = report.WriteRun(in.OutDir, in.Tool, in.Version, runRec)
		}
		results = append(results, IterationResult{
			Iteration:  1,
			ReportPath: path,
			Err:        autoRes.Err,
		})
	}
	return results, nil
}

// parseIterLabel parses a harness-emitted iter label like "3" or
// "2:warmup" into (iteration, warmup).
func parseIterLabel(label string) (int, bool) {
	warmup := false
	if idx := strings.IndexByte(label, ':'); idx >= 0 {
		if label[idx+1:] == "warmup" {
			warmup = true
		}
		label = label[:idx]
	}
	n, _ := strconv.Atoi(label)
	return n, warmup
}

func sliceSamples(in []metrics.Sample, start, end time.Time) []metrics.Sample {
	out := make([]metrics.Sample, 0, len(in))
	for _, s := range in {
		if (s.T.Equal(start) || s.T.After(start)) && (s.T.Equal(end) || s.T.Before(end)) {
			out = append(out, s)
		}
	}
	return out
}

func sliceMarkers(in []metrics.Marker, start, end time.Time) []metrics.Marker {
	out := make([]metrics.Marker, 0, len(in))
	for _, m := range in {
		if m.Kind == "iter_start" || m.Kind == "iter_end" {
			continue
		}
		if (m.T.Equal(start) || m.T.After(start)) && (m.T.Equal(end) || m.T.Before(end)) {
			out = append(out, m)
		}
	}
	return out
}
