package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/dsetiawan230294/lumos/internal/automation"
	"github.com/dsetiawan230294/lumos/internal/budget"
	"github.com/dsetiawan230294/lumos/internal/config"
	"github.com/dsetiawan230294/lumos/internal/device/android"
	"github.com/dsetiawan230294/lumos/internal/device/ios"
	"github.com/dsetiawan230294/lumos/internal/doctor"
	"github.com/dsetiawan230294/lumos/internal/interactive"
	"github.com/dsetiawan230294/lumos/internal/metrics"
	"github.com/dsetiawan230294/lumos/internal/pyharness"
	"github.com/dsetiawan230294/lumos/internal/report"
	"github.com/dsetiawan230294/lumos/internal/runner"
	"github.com/dsetiawan230294/lumos/internal/scheduler"
	"github.com/spf13/cobra"
)

func newDevicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "List attached Android and iOS devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			adb := android.NewADB()
			infos, err := adb.Devices(ctx)
			if err != nil {
				if errors.Is(err, android.ErrADBNotFound) {
					fmt.Fprintln(out, "android: adb not found on PATH (install Android Platform Tools to list Android devices)")
				} else {
					fmt.Fprintf(out, "android: %v\n", err)
				}
			} else if len(infos) == 0 {
				fmt.Fprintln(out, "android: no devices attached")
			} else {
				tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "PLATFORM\tSERIAL\tSTATE\tMODEL\tAPI")
				for _, d := range infos {
					api := ""
					if d.APILevel > 0 {
						api = fmt.Sprintf("%d", d.APILevel)
					}
					fmt.Fprintf(tw, "android\t%s\t%s\t%s\t%s\n", d.Serial, d.State, d.Model, api)
				}
				_ = tw.Flush()
			}

			// iOS discovery.
			iosTools := ios.New()
			iosInfos, ierr := iosTools.Devices(ctx)
			switch {
			case errors.Is(ierr, ios.ErrUnsupportedHost):
				fmt.Fprintln(out, "ios: not supported on this host (requires macOS)")
			case errors.Is(ierr, ios.ErrIDBNotFound):
				fmt.Fprintln(out, "ios: idb not found on PATH (install via `brew install idb-companion` and `pipx install fb-idb`)")
			case errors.Is(ierr, ios.ErrXcrunNotFound):
				fmt.Fprintln(out, "ios: xcrun not found (install Xcode Command Line Tools)")
			case ierr != nil:
				fmt.Fprintf(out, "ios: %v\n", ierr)
			case len(iosInfos) == 0:
				fmt.Fprintln(out, "ios: no devices attached")
			default:
				tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "PLATFORM\tUDID\tSTATE\tNAME\tOS")
				for _, d := range iosInfos {
					state := d.State
					if d.Simulator {
						state += " (sim)"
					}
					fmt.Fprintf(tw, "ios\t%s\t%s\t%s\t%s\n", d.UDID, state, d.Name, d.OSVersion)
				}
				_ = tw.Flush()
			}
			return nil
		},
	}
}

func newRunCmd() *cobra.Command {
	var (
		deviceFilter []string
		outDir       string
		pythonBin    string
		noDevice     bool
		jobTimeout   time.Duration
		trace        bool
		threads      bool
		debug        bool
	)
	cmd := &cobra.Command{
		Use:   "run [config.yaml]",
		Short: "Run a scripted benchmark from a config file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			cfg, err := config.Load(args[0])
			if err != nil {
				return fmt.Errorf("load config %s: %w", args[0], err)
			}

			pyDir, harness, err := pyharness.Extract()
			if err != nil {
				return fmt.Errorf("extract python harness: %w", err)
			}

			// Phase 1: Android only. Pick devices, then run each scenario
			// sequentially per device. Parallel/work-stealing is Phase 2.
			adb := android.NewADB()
			var androidDevs []android.DeviceInfo
			if !noDevice {
				devs, derr := adb.Devices(ctx)
				if derr != nil {
					if errors.Is(derr, android.ErrADBNotFound) {
						fmt.Fprintln(out, "warning: adb not found on PATH; --no-device required for dry runs")
					} else {
						fmt.Fprintf(out, "warning: %v\n", derr)
					}
				}
				for _, d := range devs {
					if d.State != "device" {
						continue
					}
					if len(deviceFilter) > 0 && !containsStr(deviceFilter, d.Serial) {
						continue
					}
					androidDevs = append(androidDevs, d)
				}
			}

			// iOS discovery (macOS only; absent tooling is non-fatal here).
			iosTools := ios.New()
			var iosDevs []ios.DeviceInfo
			if !noDevice && cfg.App.IOS != "" {
				devs, ierr := iosTools.Devices(ctx)
				switch {
				case errors.Is(ierr, ios.ErrUnsupportedHost), errors.Is(ierr, ios.ErrIDBNotFound), errors.Is(ierr, ios.ErrXcrunNotFound):
					fmt.Fprintf(out, "warning: skipping iOS (%v)\n", ierr)
				case ierr != nil:
					fmt.Fprintf(out, "warning: ios: %v\n", ierr)
				default:
					for _, d := range devs {
						if d.Simulator {
							continue // skip simulators in scripted runs by default
						}
						if d.State != "device" {
							continue
						}
						if len(deviceFilter) > 0 && !containsStr(deviceFilter, d.UDID) {
							continue
						}
						iosDevs = append(iosDevs, d)
					}
				}
			}

			plan := buildPlan(cfg, androidDevs, iosDevs, noDevice)
			if len(plan) == 0 {
				return errors.New("no devices found. Plug in an Android device with USB debugging enabled, or pass --no-device for a dry run")
			}

			// Determine dispatch mode.
			mode := cfg.Parallel.Mode
			if mode == "" {
				mode = "distribute"
			}

			// Split scenarios into hooks (run-once setup) and benchmarks.
			var hooks []config.Scenario
			var benchmarks []config.Scenario
			for _, sc := range cfg.Scenarios {
				if sc.Hook {
					hooks = append(hooks, sc)
				} else {
					benchmarks = append(benchmarks, sc)
				}
			}

			// Run hooks once per device, sequentially, before any benchmark
			// scenarios. Hooks are setup-only — no sampling, no JSON output.
			// They run on every device so each device starts in the same
			// prepared state (logged in, permissions granted, seed data, etc).
			// Failure on any device aborts the run.
			if len(hooks) > 0 {
				fmt.Fprintf(out, "lumos run: %d hook(s) on %d device(s)\n", len(hooks), len(plan))
				for _, host := range plan {
					for _, h := range hooks {
						fmt.Fprintf(out, "  [hook] %s on %s → %s\n", h.Name, host.id, h.Script)
						res := automation.Run(ctx, automation.ScenarioOpts{
							PythonBin:  pythonBin,
							HarnessPy:  harness,
							ScriptPath: h.Script,
							DeviceID:   host.id,
							Platform:   string(host.platform),
							AppID:      host.appID,
							Iteration:  1,
							Env:        mergeHookEnv(pyDir),
							Stderr:     os.Stderr,
						})
						if res.Err != nil {
							return fmt.Errorf("hook %q on %s failed: %w", h.Name, host.id, res.Err)
						}
					}
				}
			}

			// Compute (scenario, device) assignments up front, hooks excluded.
			type assignment struct {
				sc       config.Scenario
				p        targetDevice
				platform metrics.Platform
				appID    string
			}
			var jobs []assignment
			switch mode {
			case "replicate":
				// Original behavior: every scenario runs on every device.
				for _, p := range plan {
					for _, sc := range benchmarks {
						jobs = append(jobs, assignment{sc, p, p.platform, p.appID})
					}
				}
			default: // "distribute"
				// Round-robin scenarios across devices: scenario[i] runs on
				// device[i % len(plan)]. Each scenario runs on exactly one
				// device so total wall time scales with the device count.
				for i, sc := range benchmarks {
					p := plan[i%len(plan)]
					jobs = append(jobs, assignment{sc, p, p.platform, p.appID})
				}
			}

			fmt.Fprintf(out, "lumos run: %d scenario(s) on %d device(s) (mode=%s, %d job(s)) → %s\n",
				len(benchmarks), len(plan), mode, len(jobs), outDir)

			// Build one sampler factory per device, reused across scenarios
			// that land on the same device.

			// Build a work-stealing pool: one worker per device. Each
			// (device, scenario) pair becomes one Job. Pinning by DeviceID
			// ensures the device's scenarios stay on its queue by default;
			// idle devices may steal compatible jobs from busy queues.
			workers := make([]scheduler.Worker, 0, len(plan))
			for _, p := range plan {
				workers = append(workers, scheduler.Worker{ID: p.id, Platform: string(p.platform)})
			}
			pool, perr := scheduler.NewPool(workers, scheduler.Options{
				JobTimeout:             jobTimeout,
				PostProcessConcurrency: 4,
				OnEvent: func(e scheduler.JobEvent) {
					switch e.Phase {
					case "steal":
						fmt.Fprintf(out, "  [steal] %s ← %s : %s\n", e.WorkerID, e.From, e.JobID)
					case "panic":
						fmt.Fprintf(out, "  [panic] %s/%s on %s: %v\n", e.JobID, e.Scenario, e.WorkerID, e.Err)
					}
				},
			})
			if perr != nil {
				return perr
			}

			var runErrs atomic.Int32
			// Cache sampler factories per device id (built lazily on first
			// use so we don't spawn factories for devices that get no jobs
			// in distribute mode).
			samplerByDev := map[string]func() runner.Sampler{}
			for _, j := range jobs {
				j := j
				sc := j.sc
				p := j.p
				appID := j.appID
				platform := j.platform

				if _, ok := samplerByDev[p.id]; !ok {
					switch platform {
					case metrics.IOS:
						samplerByDev[p.id] = makeIOSSamplerFactory(iosTools, p, appID)
					default:
						samplerByDev[p.id] = makeSamplerFactory(ctx, adb, p, appID, 0, threads, debug)
					}
				}
				samplerFactory := samplerByDev[p.id]

				job := scheduler.Job{
					ID:        fmt.Sprintf("%s/%s", p.id, sc.Name),
					Scenario:  sc.Name,
					DeviceID:  p.id,
					Platforms: []string{string(platform)},
					Run: func(jobCtx context.Context, workerID string) error {
						in := runner.PlanInput{
							Scenario:   sc,
							DeviceID:   workerID,
							Platform:   platform,
							AppID:      appID,
							OutDir:     outDir,
							Tool:       "lumos",
							Version:    cmd.Root().Version,
							HarnessPy:  harness,
							PyPath:     pyDir,
							PythonBin:  pythonBin,
							NewSampler: samplerFactory,
						}
						if trace && platform == metrics.Android && p.id != "no-device" {
							devID := p.id
							in.NewTrace = func() runner.TraceCapture {
								return android.NewPerfettoCapture(adb, devID, "")
							}
						}
						results, err := runner.RunScenarioWithStderr(jobCtx, in, os.Stderr)
						if err != nil {
							fmt.Fprintf(out, "  [%s/%s] error: %v\n", workerID, sc.Name, err)
							runErrs.Add(1)
							return err
						}
						for _, r := range results {
							kind := "iter"
							if r.Warmup {
								kind = "warmup"
							}
							errStr := ""
							if r.Err != nil {
								errStr = " err=" + r.Err.Error()
								runErrs.Add(1)
							}
							fmt.Fprintf(out, "  [%s/%s] %s %d → %s%s\n",
								workerID, sc.Name, kind, r.Iteration, r.ReportPath, errStr)
						}
						return nil
					},
				}
				if err := pool.Submit(job); err != nil {
					return err
				}
			}
			pool.CloseSubmit()
			if err := pool.Run(ctx); err != nil {
				return err
			}
			if runErrs.Load() > 0 {
				return fmt.Errorf("lumos run: %d iteration(s) failed", runErrs.Load())
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVarP(&deviceFilter, "device", "d", nil, "restrict to specific device IDs (repeatable)")
	cmd.Flags().StringVarP(&outDir, "out", "o", "results", "output directory")
	cmd.Flags().StringVar(&pythonBin, "python", "python3", "python interpreter to invoke for scenario scripts")
	cmd.Flags().BoolVar(&noDevice, "no-device", false, "run scenarios without a device (smoke/dry run; metrics will be empty)")
	cmd.Flags().DurationVar(&jobTimeout, "job-timeout", 0, "per-(device,scenario) timeout; 0 disables")
	cmd.Flags().BoolVar(&trace, "trace", false, "capture a Perfetto trace per measured iteration (Android only; requires perfetto on device, Android 9+)")
	cmd.Flags().BoolVar(&threads, "threads", true, "capture per-thread CPU% breakdown (Android only; adds one adb roundtrip per sample). Use --threads=false to disable.")
	cmd.Flags().BoolVar(&debug, "debug", false, "log per-collector sampler errors to stderr (helps diagnose missing metrics)")
	return cmd
}

type targetDevice struct {
	id       string
	platform metrics.Platform
	appID    string
	ncpu     int
}

func buildPlan(cfg *config.Config, androidDevs []android.DeviceInfo, iosDevs []ios.DeviceInfo, noDevice bool) []targetDevice {
	var plan []targetDevice
	for _, d := range androidDevs {
		if cfg.App.Android == "" {
			continue
		}
		plan = append(plan, targetDevice{id: d.Serial, platform: metrics.Android, appID: cfg.App.Android})
	}
	for _, d := range iosDevs {
		if cfg.App.IOS == "" {
			continue
		}
		plan = append(plan, targetDevice{id: d.UDID, platform: metrics.IOS, appID: cfg.App.IOS})
	}
	if noDevice && len(plan) == 0 {
		appID := cfg.App.Android
		if appID == "" {
			appID = cfg.App.IOS
		}
		plan = append(plan, targetDevice{id: "no-device", platform: metrics.Android, appID: appID})
	}
	return plan
}

// mergeHookEnv builds the env map passed to hook scripts so the vendored
// lumos Python helper resolves the same way it does for benchmark scenarios.
func mergeHookEnv(pyPath string) map[string]string {
	env := map[string]string{}
	if pyPath != "" {
		env["PYTHONPATH"] = pyPath
	}
	return env
}

func makeSamplerFactory(ctx context.Context, adb *android.ADB, t targetDevice, appID string, _ time.Duration, threads, debug bool) func() runner.Sampler {
	return func() runner.Sampler {
		return runner.SamplerFunc(func(ctx context.Context) (<-chan metrics.Sample, error) {
			if t.id == "no-device" {
				// Dry-run: empty closed channel so the runner returns quickly.
				ch := make(chan metrics.Sample)
				go func() { <-ctx.Done(); close(ch) }()
				return ch, nil
			}

			// The scenario may launch the app, so the pid is unlikely to
			// exist at this exact moment. Spin up a coordinator goroutine
			// that polls for the pid, then starts the real sampler and
			// forwards its samples.
			out := make(chan metrics.Sample, 8)
			go func() {
				defer close(out)
				ticker := time.NewTicker(500 * time.Millisecond)
				defer ticker.Stop()
				var pid int
				for pid == 0 {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
					if p, _ := adb.Pidof(ctx, t.id, appID); p > 0 {
						pid = p
					}
				}
				ncpu := t.ncpu
				if ncpu == 0 {
					ncpu = adb.NCPU(ctx, t.id)
				}
				var dbg io.Writer
				if debug {
					dbg = os.Stderr
				}
				inner, err := adb.Sample(ctx, android.SamplerConfig{
					Serial:   t.id,
					AppID:    appID,
					Pid:      pid,
					Interval: time.Second,
					NCPU:     ncpu,
					Threads:  threads,
					Debug:    dbg,
				})
				if err != nil {
					return
				}
				for s := range inner {
					select {
					case out <- s:
					case <-ctx.Done():
						return
					}
				}
			}()
			return out, nil
		})
	}
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// makeIOSSamplerFactory wraps the iOS xctrace-based sampler into the
// runner.Sampler interface. xctrace records continuously while the scenario
// runs and emits all samples once recording stops (then the channel closes).
func makeIOSSamplerFactory(tools *ios.Tools, t targetDevice, appID string) func() runner.Sampler {
	return func() runner.Sampler {
		return runner.SamplerFunc(func(ctx context.Context) (<-chan metrics.Sample, error) {
			samples, _, err := tools.Sample(ctx, ios.SamplerConfig{
				UDID:     t.id,
				BundleID: appID,
				Interval: time.Second,
			})
			return samples, err
		})
	}
}

func newWatchCmd() *cobra.Command {
	var (
		appAndroid string
		appIOS     string
		devices    []string
		outDir     string
		noRaw      bool
		duration   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Interactive manual benchmarking session (TUI)",
		Long:  "Launch the app and stream live metrics while you drive the device manually. Press 's' to start a segment, 'e' to end, 'm' to mark, 'r' to reset focused pane, Tab to switch focus, 'q' to quit and save.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			if appAndroid == "" && appIOS == "" {
				return errors.New("watch: pass --android <pkg> and/or --ios <bundle-id> (e.g. --android com.android.settings)")
			}

			adb := android.NewADB()
			var specs []interactive.DeviceSpec
			deviceAppID := map[string]string{}
			devicePlatform := map[string]metrics.Platform{}

			if appAndroid != "" {
				devs, derr := adb.Devices(ctx)
				if derr != nil && !errors.Is(derr, android.ErrADBNotFound) {
					fmt.Fprintf(out, "warning: %v\n", derr)
				}
				for _, d := range devs {
					if d.State != "device" {
						continue
					}
					if len(devices) > 0 && !containsStr(devices, d.Serial) {
						continue
					}
					specs = append(specs, interactive.DeviceSpec{
						ID: d.Serial, Platform: metrics.Android, Label: d.Model, AppID: appAndroid,
					})
					deviceAppID[d.Serial] = appAndroid
					devicePlatform[d.Serial] = metrics.Android
				}
			}

			iosTools := ios.New()
			if appIOS != "" {
				devs, ierr := iosTools.Devices(ctx)
				if ierr == nil {
					for _, d := range devs {
						if d.Simulator || d.State != "device" {
							continue
						}
						if len(devices) > 0 && !containsStr(devices, d.UDID) {
							continue
						}
						specs = append(specs, interactive.DeviceSpec{
							ID: d.UDID, Platform: metrics.IOS, Label: d.Name, AppID: appIOS,
						})
						deviceAppID[d.UDID] = appIOS
						devicePlatform[d.UDID] = metrics.IOS
					}
				} else if !errors.Is(ierr, ios.ErrUnsupportedHost) &&
					!errors.Is(ierr, ios.ErrIDBNotFound) &&
					!errors.Is(ierr, ios.ErrXcrunNotFound) {
					fmt.Fprintf(out, "warning: ios: %v\n", ierr)
				}
			}

			if len(specs) == 0 {
				if len(devices) > 0 {
					return fmt.Errorf("watch: none of the requested devices matched (--device %v). Run `lumos devices` to list attached devices", devices)
				}
				return errors.New("watch: no devices found. Plug in an Android device with USB debugging enabled, or run `lumos devices` to verify")
			}

			sampler := func(sctx context.Context, dev interactive.DeviceSpec) (<-chan metrics.Sample, error) {
				switch dev.Platform {
				case metrics.IOS:
					ch, _, err := iosTools.Sample(sctx, ios.SamplerConfig{
						UDID:     dev.ID,
						BundleID: dev.AppID,
						Interval: time.Second,
					})
					return ch, err
				default:
					// Android: poll for pid (app may not be running yet), then sample.
					ch := make(chan metrics.Sample, 8)
					go func() {
						defer close(ch)
						ticker := time.NewTicker(500 * time.Millisecond)
						defer ticker.Stop()
						var pid int
						for pid == 0 {
							select {
							case <-sctx.Done():
								return
							case <-ticker.C:
							}
							if p, _ := adb.Pidof(sctx, dev.ID, dev.AppID); p > 0 {
								pid = p
							}
						}
						ncpu := adb.NCPU(sctx, dev.ID)
						inner, err := adb.Sample(sctx, android.SamplerConfig{
							Serial: dev.ID, AppID: dev.AppID, Pid: pid,
							Interval: time.Second, NCPU: ncpu,
						})
						if err != nil {
							return
						}
						for s := range inner {
							select {
							case ch <- s:
							case <-sctx.Done():
								return
							}
						}
					}()
					return ch, nil
				}
			}

			if duration > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, duration)
				defer cancel()
			}

			return interactive.Run(ctx, interactive.Config{
				Devices: specs,
				Sampler: sampler,
				OutDir:  outDir,
				Tool:    "lumos",
				Version: cmd.Root().Version,
				NoRaw:   noRaw,
			})
		},
	}
	cmd.Flags().StringVar(&appAndroid, "android", "", "Android package id (e.g. com.example.app)")
	cmd.Flags().StringVar(&appIOS, "ios", "", "iOS bundle id (e.g. com.example.app)")
	cmd.Flags().StringSliceVarP(&devices, "device", "d", nil, "restrict to specific device IDs (repeatable)")
	cmd.Flags().StringVarP(&outDir, "out", "o", "results", "output directory")
	cmd.Flags().BoolVar(&noRaw, "no-raw", false, "disable raw-mode TUI (sampler still runs; useful for CI/piped output)")
	cmd.Flags().DurationVar(&duration, "duration", 0, "auto-exit after this duration (e.g. 10s); 0 = run until 'q' or Ctrl-C")
	return cmd
}

func newReportCmd() *cobra.Command {
	var (
		outPath     string
		perScenario bool
	)
	cmd := &cobra.Command{
		Use:   "report [results-dir]",
		Short: "Render an HTML report from a results directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := report.WriteHTMLReport(args[0], outPath, "lumos", versionFromCtx(cmd))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
			if perScenario {
				paths, err := report.WritePerScenarioReports(args[0], "lumos", versionFromCtx(cmd))
				if err != nil {
					return err
				}
				for _, p := range paths {
					fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", p)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "output HTML path for the combined report (default: <results-dir>/report.html)")
	cmd.Flags().BoolVar(&perScenario, "per-scenario", false, "also write one HTML per scenario (report_<scenario>.html)")
	return cmd
}

func newCompareCmd() *cobra.Command {
	var (
		thresholdStr string
		jsonOut      bool
		strict       bool
	)
	cmd := &cobra.Command{
		Use:   "compare [baseline.json] [current.json]",
		Short: "Compare a current run against a baseline",
		Long:  "Exit code 0 = pass, 1 = regression, 2 = error. Use --threshold N (percent) to set the regression tolerance.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			th, err := report.ParseThreshold(thresholdStr)
			if err != nil {
				return fmt.Errorf("invalid --threshold: %w", err)
			}
			res, err := report.CompareFiles(args[0], args[1], th, nil)
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(res); err != nil {
					return err
				}
			} else {
				fmt.Fprint(cmd.OutOrStdout(), res.FormatTable())
			}
			if !res.Pass && strict {
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
				return ErrRegression
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&thresholdStr, "threshold", "5%", "regression threshold (e.g. '5%' or '5')")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON instead of a table")
	cmd.Flags().BoolVar(&strict, "strict", true, "exit non-zero when regressions are detected")
	return cmd
}

// ErrRegression is returned by `lumos compare --strict` when regressions are
// detected; main.go maps it to exit code 1.
var ErrRegression = errors.New("regression detected")

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the local environment for common setup issues",
		Long: `Runs a battery of health checks (adb, python3, xcrun, connected devices,
on-device perfetto, …) and prints actionable hints for any problems.
Exits 0 if everything is OK or only WARN, 1 if any check FAILs.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			report := doctor.Run(cmd.Context())
			doctor.Render(cmd.OutOrStdout(), report)
			if report.Verdict() == doctor.Fail {
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
				return errors.New("doctor: required dependencies missing")
			}
			return nil
		},
	}
}

func newExportCmd() *cobra.Command {
	var (
		mode         string
		outPath      string
		includeExtra bool
	)
	cmd := &cobra.Command{
		Use:   "export [results-dir]",
		Short: "Export run results as CSV (samples or per-metric summary)",
		Long: `Walks the results directory and writes a CSV.

  --mode samples   one row per sample (default; raw timeseries for pandas/sheets)
  --mode summary   one row per (run, metric) with count/mean/p50/p90/p99/min/max/std

Writes to stdout unless -o <path> is given. Use --include-extra in samples mode
to also include plugin-supplied 'extra.*' columns.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := report.CSVOptions{Mode: mode, IncludeExtra: includeExtra}
			if outPath == "" || outPath == "-" {
				n, err := report.WriteCSV(args[0], cmd.OutOrStdout(), opts)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "exported %d row(s)\n", n)
				return nil
			}
			n, err := report.WriteCSVFile(args[0], outPath, opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d row(s))\n", outPath, n)
			return nil
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "samples", "output mode: \"samples\" or \"summary\"")
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "output CSV path (default: stdout)")
	cmd.Flags().BoolVar(&includeExtra, "include-extra", false, "include plugin-supplied extra.* columns (samples mode only)")
	return cmd
}

func newCheckCmd() *cobra.Command {
	var (
		budgetPath string
		jsonOut    bool
	)
	cmd := &cobra.Command{
		Use:   "check [results-dir]",
		Short: "Check a results directory against a perf-budget YAML",
		Long: `Asserts absolute per-metric targets ("p90 fps must be >= 55") against
each run in the results directory. Complements 'lumos compare', which detects
relative regressions vs a baseline.

Budget YAML:

  default:
    fps:      { p90: ">= 55", mean: ">= 58" }
    cpu_pct:  { mean: "<= 30" }
  scenarios:
    scroll_feed:
      fps: { p90: ">= 58" }    # tighter than default

Exit code 0 = pass, 1 = budget violation(s), 2 = other error.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if budgetPath == "" {
				return errors.New("--budget is required")
			}
			bg, err := budget.Load(budgetPath)
			if err != nil {
				return err
			}
			res, err := bg.CheckDir(args[0])
			if err != nil {
				return err
			}
			res.Budget = budgetPath
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				_ = enc.Encode(res)
			} else {
				fmt.Fprint(cmd.OutOrStdout(), res.FormatText())
			}
			if !res.Pass {
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
				return ErrRegression
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&budgetPath, "budget", "", "path to budget YAML (required)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

func newTrendsCmd() *cobra.Command {
	var outPath string
	cmd := &cobra.Command{
		Use:   "trends [results-dir...]",
		Short: "Render a historical-trends HTML dashboard across one or more results directories",
		Long: `Walks each results directory recursively, groups runs by (scenario, device,
platform), sorts each group chronologically, and renders a single HTML page
with per-metric trend lines (SVG, no JS). Warmup iterations are excluded.

Use it to compare nightly CI artifacts across time, or to track a single
device's performance drift over a release cycle.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := report.WriteTrendsHTML(args, outPath, "lumos", versionFromCtx(cmd))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
			return nil
		},
	}
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "output HTML path (default: <first-results-dir>/trends.html)")
	return cmd
}

// versionFromCtx pulls the version string set on the root command,
// falling back to "dev".
func versionFromCtx(cmd *cobra.Command) string {
	if v := cmd.Root().Version; v != "" {
		return v
	}
	return "dev"
}
