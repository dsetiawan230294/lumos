// Package config defines the Lumos benchmark configuration schema and loader.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level benchmark configuration parsed from YAML.
type Config struct {
	App        App               `yaml:"app"`
	Scenarios  []Scenario        `yaml:"scenarios"`
	Devices    Devices           `yaml:"devices"`
	Parallel   Parallel          `yaml:"parallel"`
	Thresholds Thresholds        `yaml:"thresholds"`
	// Env is a map of extra environment variables exported to every
	// scenario and hook subprocess. Useful for `PYTHONPATH`, feature
	// flags, or anything else your scripts read via os.environ.
	// PYTHONPATH is special-cased: Lumos's vendored helper dir is
	// prepended so the `lumos` Python module always resolves.
	Env        map[string]string `yaml:"env"`
}

// App identifies the application under test on each platform.
type App struct {
	Android string  `yaml:"android"`
	IOS     string  `yaml:"ios"`
	Appium  *Appium `yaml:"appium"` // optional: enables auto driver injection
}

// Appium configures auto driver creation for scenarios and hooks. When set,
// the Python harness opens an Appium WebDriver session before calling the
// scenario's `run()` function and passes it in as the `driver` argument:
//
//	def run(device, driver, iteration):
//	    interact.driver = driver
//	    ...
//
// All fields are optional; reasonable defaults apply.
type Appium struct {
	// ServerURL is the Appium server endpoint. Defaults to the
	// LUMOS_APPIUM_URL env var, or http://localhost:4723 if neither set.
	ServerURL string `yaml:"server_url"`

	// Activity is appActivity (Android). Needed when AutoLaunch=false and
	// the app has multiple launcher activities.
	Activity string `yaml:"activity"`

	// AutoLaunch toggles Appium's session-start `am start`. Default true.
	// Set false for apps with ambiguous launcher intents (driven via
	// deeplinks / start_activity instead).
	AutoLaunch *bool `yaml:"auto_launch"`

	// NoReset preserves app data between sessions. Default false. Set
	// true when a hook prepares state (env switch, login) that later
	// scenarios should reuse.
	NoReset *bool `yaml:"no_reset"`

	// TerminateOnQuit controls whether the harness force-stops the app
	// when the scenario subprocess exits. Default true (clean state
	// between scenarios). Set false on a hook scenario so the next
	// scenario can resume in-memory state (e.g. an unlocked PIN screen)
	// rather than cold-launching.
	TerminateOnQuit *bool `yaml:"terminate_on_quit"`

	// Caps merges arbitrary extra W3C capabilities on top of the above.
	Caps map[string]any `yaml:"caps"`
}

// Scenario describes one benchmark scenario driven by a Python script.
//
// A scenario marked Hook=true is a setup/teardown script that runs exactly
// once at the start of the whole `lumos run`, on the first device in the
// plan. Hooks are NOT sampled, NOT timed, and produce NO JSON output — they
// exist to prepare device/app state (login, seed data, grant permissions,
// dismiss dialogs, etc.) before benchmark scenarios start.
//
// Timebox sets a minimum wall-clock duration for the measured phase. When
// non-zero, lumos keeps running additional iterations after `iterations`
// have completed until the elapsed measured time ≥ Timebox. Useful for
// flake-prone metrics where you want "at least N minutes of samples"
// regardless of how fast each iteration runs.
type Scenario struct {
	Name        string        `yaml:"name"`
	Script      string        `yaml:"script"`
	Iterations  int           `yaml:"iterations"`
	Warmup      int           `yaml:"warmup"`
	CooldownSec time.Duration `yaml:"cooldown_sec"`
	Hook        bool          `yaml:"hook"`
	Timebox     time.Duration `yaml:"timebox"`

	// InProcessIterations switches the iteration model from
	// "one subprocess per iteration" (default) to "one subprocess for
	// the whole scenario". When true the harness opens the driver
	// once, calls setup() once, loops run(device, driver, i) N times
	// emitting per-iteration markers, then calls teardown() once.
	// Each iteration still produces its own JSON report (samples are
	// sliced by the per-iteration markers). Use this when re-opening
	// the driver between iterations is too expensive (e.g. cold
	// install + login per iteration).
	InProcessIterations bool `yaml:"in_process_iterations"`

	// Appium overrides the top-level `app.appium` block for this
	// scenario only. Useful when a hook needs `no_reset: false` (clean
	// install for a deterministic login) while the benchmark scenarios
	// keep `no_reset: true` (cold-start from the hook's prepared state).
	// Only set fields override; nil/missing fields fall through to the
	// top-level config.
	Appium *Appium `yaml:"appium"`
}

// Devices filters which attached devices participate.
type Devices struct {
	Filter DeviceFilter `yaml:"filter"`
}

// DeviceFilter selects devices by platform/version.
type DeviceFilter struct {
	OS     []string `yaml:"os"`
	MinAPI int      `yaml:"min_api"`
	MinIOS string   `yaml:"min_ios"`
}

// Parallel controls multi-device execution.
//
// Mode controls how scenarios are mapped to devices:
//
//   - "distribute" (default): each scenario runs on exactly one device.
//     Scenarios are round-robin assigned across the device pool. Total wall
//     time scales with the number of devices — best for throughput.
//   - "replicate": every scenario runs on every device. Best for
//     cross-device comparison reports.
type Parallel struct {
	MaxDevices   int    `yaml:"max_devices"`
	WorkStealing bool   `yaml:"work_stealing"`
	Mode         string `yaml:"mode"` // "distribute" (default) | "replicate"
}

// Thresholds are pass/fail gates for the benchmark.
type Thresholds struct {
	FPSp10Min  float64 `yaml:"fps_p10_min"`
	CPUAvgMax  float64 `yaml:"cpu_avg_max"`
	RAMAvgMaxM float64 `yaml:"ram_avg_max_mb"`
}

// Load reads, parses, and validates a YAML config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return &c, nil
}

// Validate checks the configuration for obvious errors.
func (c *Config) Validate() error {
	if c.App.Android == "" && c.App.IOS == "" {
		return errors.New("app.android or app.ios must be set")
	}
	if len(c.Scenarios) == 0 {
		return errors.New("at least one scenario is required")
	}
	switch c.Parallel.Mode {
	case "", "distribute", "replicate":
	default:
		return fmt.Errorf("parallel.mode must be 'distribute' or 'replicate', got %q", c.Parallel.Mode)
	}
	for i, s := range c.Scenarios {
		if s.Name == "" {
			return fmt.Errorf("scenarios[%d].name is required", i)
		}
		if s.Script == "" {
			return fmt.Errorf("scenarios[%d].script is required", i)
		}
		if s.Hook {
			// Hooks are setup-only; iteration/warmup/timebox are ignored.
			continue
		}
		if s.Iterations < 1 {
			return fmt.Errorf("scenarios[%d].iterations must be >= 1", i)
		}
		if s.Warmup < 0 {
			return fmt.Errorf("scenarios[%d].warmup must be >= 0", i)
		}
		if s.Timebox < 0 {
			return fmt.Errorf("scenarios[%d].timebox must be >= 0", i)
		}
	}
	return nil
}
