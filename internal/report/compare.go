package report

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// MetricDir is the regression direction: which way is "bad".
type MetricDir int

const (
	HigherIsBetter MetricDir = iota // e.g. FPS
	LowerIsBetter                   // e.g. CPU%, RAM, jank%, frame_ms
)

// directions maps a metric key to its "bad" direction.
var directions = map[string]MetricDir{
	"fps":            HigherIsBetter,
	"frame_ms":       LowerIsBetter,
	"cpu_pct":        LowerIsBetter,
	"ram_mb":         LowerIsBetter,
	"jank_pct":       LowerIsBetter,
	"battery_pct":    HigherIsBetter, // drained less = higher remaining = better
	"battery_temp_c": LowerIsBetter,  // cooler = better
}

// MetricDelta describes how a single metric changed between baseline and current.
type MetricDelta struct {
	Metric     string  `json:"metric"`
	Stat       string  `json:"stat"` // "mean" | "p90" | "p99"
	Baseline   float64 `json:"baseline"`
	Current    float64 `json:"current"`
	DeltaAbs   float64 `json:"delta_abs"`
	DeltaPct   float64 `json:"delta_pct"`
	Regression bool    `json:"regression"`
}

// CompareResult is the structured outcome of compare; safe to emit as JSON.
type CompareResult struct {
	Baseline     string        `json:"baseline"`
	Current      string        `json:"current"`
	Threshold    float64       `json:"threshold_pct"`
	Regressions  []MetricDelta `json:"regressions"`
	Improvements []MetricDelta `json:"improvements"`
	All          []MetricDelta `json:"all"`
	Pass         bool          `json:"pass"`
}

// CompareFiles loads two RunReport JSONs and computes per-metric deltas
// (mean and p90) for each metric they share.
//
// thresholdPct (e.g. 5.0) is the % beyond which a worsening is considered a
// regression. stats controls which stats to compare; default ["mean","p90"].
func CompareFiles(baselinePath, currentPath string, thresholdPct float64, stats []string) (*CompareResult, error) {
	base, err := loadReport(baselinePath)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	cur, err := loadReport(currentPath)
	if err != nil {
		return nil, fmt.Errorf("current: %w", err)
	}
	if len(stats) == 0 {
		stats = []string{"mean", "p90"}
	}

	res := &CompareResult{
		Baseline:  baselinePath,
		Current:   currentPath,
		Threshold: thresholdPct,
		Pass:      true,
	}

	keys := metricKeys(base.Summary, cur.Summary)
	for _, k := range keys {
		bs, bok := base.Summary[k]
		cs, cok := cur.Summary[k]
		if !bok || !cok {
			continue
		}
		dir := directions[k]
		for _, stat := range stats {
			bv := pickStat(bs, stat)
			cv := pickStat(cs, stat)
			if bv == 0 && cv == 0 {
				continue
			}
			d := MetricDelta{Metric: k, Stat: stat, Baseline: bv, Current: cv, DeltaAbs: cv - bv}
			if bv != 0 {
				d.DeltaPct = (cv - bv) / math.Abs(bv) * 100
			}
			d.Regression = isRegression(dir, d.DeltaPct, thresholdPct)
			res.All = append(res.All, d)
			switch {
			case d.Regression:
				res.Regressions = append(res.Regressions, d)
				res.Pass = false
			case isImprovement(dir, d.DeltaPct, thresholdPct):
				res.Improvements = append(res.Improvements, d)
			}
		}
	}
	return res, nil
}

func loadReport(path string) (*RunReport, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r RunReport
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func metricKeys(a, b map[string]Summary) []string {
	set := map[string]struct{}{}
	for k := range a {
		set[k] = struct{}{}
	}
	for k := range b {
		set[k] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func pickStat(s Summary, stat string) float64 {
	switch stat {
	case "mean":
		return s.Mean
	case "p50":
		return s.P50
	case "p90":
		return s.P90
	case "p99":
		return s.P99
	case "min":
		return s.Min
	case "max":
		return s.Max
	}
	return 0
}

func isRegression(dir MetricDir, deltaPct, threshold float64) bool {
	if math.Abs(deltaPct) <= threshold {
		return false
	}
	if dir == HigherIsBetter {
		return deltaPct < 0 // got smaller, bad
	}
	return deltaPct > 0 // got bigger, bad
}

func isImprovement(dir MetricDir, deltaPct, threshold float64) bool {
	if math.Abs(deltaPct) <= threshold {
		return false
	}
	if dir == HigherIsBetter {
		return deltaPct > 0
	}
	return deltaPct < 0
}

// FormatTable renders a human-readable compare table.
func (c *CompareResult) FormatTable() string {
	var b strings.Builder
	fmt.Fprintf(&b, "baseline: %s\ncurrent:  %s\nthreshold: %.2f%%\n\n", c.Baseline, c.Current, c.Threshold)
	fmt.Fprintf(&b, "%-10s %-6s %12s %12s %10s %10s  %s\n", "metric", "stat", "baseline", "current", "Δ abs", "Δ %", "verdict")
	for _, d := range c.All {
		verdict := "ok"
		if d.Regression {
			verdict = "REGRESSION"
		} else if isImprovement(directions[d.Metric], d.DeltaPct, c.Threshold) {
			verdict = "improved"
		}
		fmt.Fprintf(&b, "%-10s %-6s %12.3f %12.3f %10.3f %9.2f%%  %s\n",
			d.Metric, d.Stat, d.Baseline, d.Current, d.DeltaAbs, d.DeltaPct, verdict)
	}
	fmt.Fprintf(&b, "\nresult: %s (regressions=%d, improvements=%d)\n",
		boolWord(c.Pass, "PASS", "FAIL"), len(c.Regressions), len(c.Improvements))
	return b.String()
}

func boolWord(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

// ParseThreshold accepts forms like "5", "5%", "5.5%" and returns the percent value.
func ParseThreshold(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}
