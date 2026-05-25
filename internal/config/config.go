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
	App        App        `yaml:"app"`
	Scenarios  []Scenario `yaml:"scenarios"`
	Devices    Devices    `yaml:"devices"`
	Parallel   Parallel   `yaml:"parallel"`
	Thresholds Thresholds `yaml:"thresholds"`
}

// App identifies the application under test on each platform.
type App struct {
	Android string `yaml:"android"`
	IOS     string `yaml:"ios"`
}

// Scenario describes one benchmark scenario driven by a Python script.
type Scenario struct {
	Name        string        `yaml:"name"`
	Script      string        `yaml:"script"`
	Iterations  int           `yaml:"iterations"`
	Warmup      int           `yaml:"warmup"`
	CooldownSec time.Duration `yaml:"cooldown_sec"`
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
type Parallel struct {
	MaxDevices   int  `yaml:"max_devices"`
	WorkStealing bool `yaml:"work_stealing"`
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
	for i, s := range c.Scenarios {
		if s.Name == "" {
			return fmt.Errorf("scenarios[%d].name is required", i)
		}
		if s.Script == "" {
			return fmt.Errorf("scenarios[%d].script is required", i)
		}
		if s.Iterations < 1 {
			return fmt.Errorf("scenarios[%d].iterations must be >= 1", i)
		}
		if s.Warmup < 0 {
			return fmt.Errorf("scenarios[%d].warmup must be >= 0", i)
		}
	}
	return nil
}
