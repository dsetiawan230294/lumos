// Package report writes JSON results and (later) HTML reports and
// baseline comparisons.
package report

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

// SchemaVersion is bumped on incompatible JSON layout changes.
const SchemaVersion = "1"

// Summary aggregates a set of samples into descriptive stats.
type Summary struct {
	Count int     `json:"count"`
	Mean  float64 `json:"mean"`
	P50   float64 `json:"p50"`
	P90   float64 `json:"p90"`
	P99   float64 `json:"p99"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Std   float64 `json:"std"`
}

// RunReport is the on-disk JSON document for one scenario iteration.
type RunReport struct {
	Schema    string             `json:"schema"`
	Tool      string             `json:"tool"`
	Version   string             `json:"version"`
	WrittenAt time.Time          `json:"written_at"`
	Run       metrics.Run        `json:"run"`
	Summary   map[string]Summary `json:"summary"`
}

// SummarizeRun builds per-metric summaries from a run's samples.
func SummarizeRun(r metrics.Run) map[string]Summary {
	out := map[string]Summary{}
	if len(r.Samples) == 0 {
		return out
	}
	extract := func(get func(s metrics.Sample) float64) Summary {
		xs := make([]float64, 0, len(r.Samples))
		for _, s := range r.Samples {
			v := get(s)
			if v != 0 {
				xs = append(xs, v)
			}
		}
		return summarize(xs)
	}
	out["fps"] = extract(func(s metrics.Sample) float64 { return s.FPS })
	out["frame_ms"] = extract(func(s metrics.Sample) float64 { return s.FrameMS })
	out["cpu_pct"] = extract(func(s metrics.Sample) float64 { return s.CPUPct })
	out["ram_mb"] = extract(func(s metrics.Sample) float64 { return s.RAMMB })
	out["jank_pct"] = extract(func(s metrics.Sample) float64 { return s.JankPct })
	out["battery_pct"] = extract(func(s metrics.Sample) float64 { return s.BatteryPct })
	out["battery_temp_c"] = extract(func(s metrics.Sample) float64 { return s.BatteryTempC })

	// Summarise any custom keys from plugins. We pre-scan once to find the
	// union of keys, then summarise each.
	extraKeys := map[string]struct{}{}
	for _, s := range r.Samples {
		for k := range s.Extra {
			extraKeys[k] = struct{}{}
		}
	}
	for k := range extraKeys {
		key := k
		out["extra."+key] = extract(func(s metrics.Sample) float64 {
			if s.Extra == nil {
				return 0
			}
			return s.Extra[key]
		})
	}
	return out
}

func summarize(xs []float64) Summary {
	if len(xs) == 0 {
		return Summary{}
	}
	sorted := make([]float64, len(xs))
	copy(sorted, xs)
	sort.Float64s(sorted)
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	var sq float64
	for _, x := range xs {
		d := x - mean
		sq += d * d
	}
	std := math.Sqrt(sq / float64(len(xs)))
	return Summary{
		Count: len(xs),
		Mean:  mean,
		P50:   pct(sorted, 50),
		P90:   pct(sorted, 90),
		P99:   pct(sorted, 99),
		Min:   sorted[0],
		Max:   sorted[len(sorted)-1],
		Std:   std,
	}
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := int(float64(len(sorted)-1) * p / 100.0)
	return sorted[idx]
}

// WriteRun writes a single Run as JSON under dir/<scenario>_<iter>_<device>.json.
// Returns the file path that was written.
func WriteRun(dir, tool, version string, r metrics.Run) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %q: %w", dir, err)
	}
	doc := RunReport{
		Schema:    SchemaVersion,
		Tool:      tool,
		Version:   version,
		WrittenAt: time.Now(),
		Run:       r,
		Summary:   SummarizeRun(r),
	}
	name := fmt.Sprintf("%s_%03d_%s.json", safeName(r.Scenario), r.Iteration, safeName(r.DeviceID))
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&doc); err != nil {
		return "", err
	}
	return path, nil
}

func safeName(s string) string {
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
