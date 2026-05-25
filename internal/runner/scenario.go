package runner

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/dsetiawan230294/lumos/internal/automation"
	"github.com/dsetiawan230294/lumos/internal/config"
	"github.com/dsetiawan230294/lumos/internal/metrics"
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
