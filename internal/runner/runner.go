// Package runner orchestrates one benchmark job: warmup, start metric
// sampling, run the user scenario, collect samples and markers, write the
// JSON report.
package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dsetiawan230294/lumos/internal/automation"
	"github.com/dsetiawan230294/lumos/internal/metrics"
	"github.com/dsetiawan230294/lumos/internal/report"
)

// Sampler is the minimum contract the runner needs from a device. It is
// satisfied by the Android sampler (and, later, the iOS one), but can also
// be faked in tests by returning a pre-populated channel.
type Sampler interface {
	Sample(ctx context.Context) (<-chan metrics.Sample, error)
}

// SamplerFunc adapts a plain function to the Sampler interface.
type SamplerFunc func(ctx context.Context) (<-chan metrics.Sample, error)

// Sample implements Sampler.
func (f SamplerFunc) Sample(ctx context.Context) (<-chan metrics.Sample, error) {
	return f(ctx)
}

// AutomationRunner executes one Python scenario. The default implementation
// is `automation.Run`; tests stub this out.
type AutomationRunner func(ctx context.Context, o automation.ScenarioOpts) automation.Result

// TraceCapture is an optional side-channel capture (e.g. Perfetto) that
// runs in parallel with the sampler+scenario and writes a file artifact.
type TraceCapture interface {
	// Start begins capturing. Called before the scenario starts.
	Start(ctx context.Context) error
	// StopAndPull stops the capture and pulls it to localPath. Called after
	// the scenario finishes (or fails). Implementations must be idempotent.
	StopAndPull(ctx context.Context, localPath string) error
	// Kind names the artifact ("perfetto", "instruments", …).
	Kind() string
}

// Job is one unit of work for the runner.
type Job struct {
	Scenario   string
	Iteration  int
	DeviceID   string
	Platform   metrics.Platform
	AppID      string
	Sampler    Sampler
	Automation AutomationRunner // default automation.Run if nil
	Scenario_  automation.ScenarioOpts
	OutDir     string
	Tool       string
	Version    string
	Stderr     io.Writer

	// Trace is an optional side-channel capture. Failures are recorded in
	// the run's Error/stderr but never block the run.
	Trace TraceCapture
}

// Result of a single job execution.
type Result struct {
	Run        metrics.Run
	ReportPath string
}

// Run executes one job: starts the sampler, kicks off the scenario, collects
// samples until the scenario finishes, then writes the JSON report.
func Run(ctx context.Context, j Job) (Result, error) {
	if j.Sampler == nil {
		return Result{}, fmt.Errorf("runner: Sampler required")
	}
	if j.Automation == nil {
		j.Automation = automation.Run
	}

	startedAt := time.Now()
	sampleCtx, stopSampling := context.WithCancel(ctx)
	defer stopSampling()

	samples, err := j.Sampler.Sample(sampleCtx)
	if err != nil {
		return Result{}, fmt.Errorf("runner: start sampler: %w", err)
	}

	// Optional Perfetto-style side-channel trace.
	traceStarted := false
	if j.Trace != nil {
		if terr := j.Trace.Start(ctx); terr != nil {
			fmt.Fprintf(stderrOr(j.Stderr), "lumos: %s trace start failed: %v (continuing without trace)\n", j.Trace.Kind(), terr)
		} else {
			traceStarted = true
		}
	}

	collected := make([]metrics.Sample, 0, 256)
	drained := make(chan struct{})
	go func() {
		for s := range samples {
			collected = append(collected, s)
		}
		close(drained)
	}()

	opts := j.Scenario_
	opts.DeviceID = j.DeviceID
	opts.Platform = string(j.Platform)
	opts.AppID = j.AppID
	opts.Iteration = j.Iteration
	if opts.Stderr == nil {
		opts.Stderr = j.Stderr
	}
	autoRes := j.Automation(ctx, opts)

	stopSampling()
	<-drained

	run := metrics.Run{
		Scenario:  j.Scenario,
		Iteration: j.Iteration,
		DeviceID:  j.DeviceID,
		Platform:  j.Platform,
		StartedAt: startedAt,
		EndedAt:   time.Now(),
		Samples:   collected,
		Markers:   autoRes.Markers,
	}
	if autoRes.Err != nil {
		run.Error = autoRes.Err.Error()
	}

	// Stop + pull trace. Use a fresh context so cancellation of the parent
	// (e.g. ctrl-c) doesn't strand the on-device session.
	if traceStarted {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		filename := fmt.Sprintf("%s_%03d_%s.%s-trace", safeForFilename(j.Scenario), j.Iteration, safeForFilename(j.DeviceID), j.Trace.Kind())
		local := filepath.Join(j.OutDir, filename)
		// WriteRun lazily creates OutDir, but the pull happens first, so
		// ensure the directory exists ourselves.
		if j.OutDir != "" {
			_ = os.MkdirAll(j.OutDir, 0o755)
		}
		if perr := j.Trace.StopAndPull(stopCtx, local); perr != nil {
			fmt.Fprintf(stderrOr(j.Stderr), "lumos: %s trace pull failed: %v\n", j.Trace.Kind(), perr)
		} else if info, statErr := os.Stat(local); statErr == nil {
			run.Artifacts = append(run.Artifacts, metrics.Artifact{
				Kind: j.Trace.Kind(),
				Path: filepath.Base(local),
				Size: info.Size(),
			})
		}
	}

	res := Result{Run: run}
	if j.OutDir != "" {
		path, werr := report.WriteRun(j.OutDir, j.Tool, j.Version, run)
		if werr != nil {
			return res, fmt.Errorf("runner: write report: %w", werr)
		}
		res.ReportPath = path
	}
	return res, nil
}

// stderrOr returns w, or io.Discard if w is nil.
func stderrOr(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// safeForFilename mirrors report.safeName for filename-safe chars without
// adding a package dep.
func safeForFilename(s string) string {
	if s == "" {
		return "unknown"
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
