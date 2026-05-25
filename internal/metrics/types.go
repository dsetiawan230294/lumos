// Package metrics defines the unified metric model produced by all device collectors.
package metrics

import "time"

// Platform identifies a mobile OS.
type Platform string

const (
	Android Platform = "android"
	IOS     Platform = "ios"
)

// Sample is a single point-in-time observation from a collector.
type Sample struct {
	T       time.Time `json:"t"`
	FPS     float64   `json:"fps,omitempty"`
	FrameMS float64   `json:"frame_ms,omitempty"`
	CPUPct  float64   `json:"cpu_pct,omitempty"`
	RAMMB   float64   `json:"ram_mb,omitempty"`
	JankPct float64   `json:"jank_pct,omitempty"`
	GPUPct  float64   `json:"gpu_pct,omitempty"`

	// Battery telemetry (Android: dumpsys battery; iOS: not yet).
	BatteryPct   float64 `json:"battery_pct,omitempty"`
	BatteryTempC float64 `json:"battery_temp_c,omitempty"`

	// Extra is a free-form map of metric name → value, populated by
	// user-provided collector plugins. Keys MUST be snake_case ASCII
	// (e.g. "gpu_temp_c", "net_rx_kbps") to keep reports tidy.
	Extra map[string]float64 `json:"extra,omitempty"`

	// Threads is a per-thread CPU% breakdown for this tick, keyed by the
	// thread name (comm). When several threads share a comm (e.g. multiple
	// "GLThread"s), their contributions are summed under that key.
	// Android-only for now; iOS leaves this empty.
	Threads map[string]float64 `json:"threads,omitempty"`
}

// Marker is a user- or script-emitted event on the timeline.
type Marker struct {
	T     time.Time `json:"t"`
	Label string    `json:"label"`
	Kind  string    `json:"kind"` // "start" | "end" | "point"
}

// Run is the complete record of one scenario iteration on one device.
type Run struct {
	Scenario  string    `json:"scenario"`
	Iteration int       `json:"iteration"`
	DeviceID  string    `json:"device_id"`
	Platform  Platform  `json:"platform"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Samples   []Sample  `json:"samples"`
	Markers   []Marker  `json:"markers"`
	Error     string    `json:"error,omitempty"`

	// Artifacts are out-of-band files captured alongside the run, e.g. a
	// Perfetto trace. Paths are relative to the run JSON's directory.
	Artifacts []Artifact `json:"artifacts,omitempty"`
}

// Artifact is a side-file referenced from a Run (Perfetto trace, screenshot, …).
type Artifact struct {
	Kind string `json:"kind"` // "perfetto", "screenshot", …
	Path string `json:"path"` // relative to OutDir
	Size int64  `json:"size_bytes,omitempty"`
}
