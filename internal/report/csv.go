package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

// CSVOptions controls CSV output shape.
type CSVOptions struct {
	// Mode picks the row granularity:
	//   - "samples"  → one row per sample (raw timeseries; default)
	//   - "summary"  → one row per (run, metric) with mean/p50/p90/p99/min/max/std
	Mode string
	// IncludeExtra includes plugin-supplied `sample.extra.*` columns in
	// samples mode (sorted alphabetically). Ignored for summary mode.
	IncludeExtra bool
}

// WriteCSV walks resultsDir, loads every RunReport JSON, and writes a CSV
// representation to w. Returns the number of rows emitted (excluding the
// header).
//
// In "samples" mode the column set is:
//
//	scenario, device_id, platform, iteration, t,
//	fps, frame_ms, cpu_pct, ram_mb, jank_pct, battery_pct, battery_temp_c
//	[+ extra.<key>... if IncludeExtra]
//
// In "summary" mode it is:
//
//	scenario, device_id, platform, iteration, metric,
//	count, mean, p50, p90, p99, min, max, std
//
// Files that don't parse are skipped silently — use LoadRunReports first if
// you need diagnostics.
func WriteCSV(resultsDir string, w io.Writer, opts CSVOptions) (int, error) {
	reports, _, err := LoadRunReports(resultsDir)
	if err != nil {
		return 0, err
	}
	if len(reports) == 0 {
		return 0, fmt.Errorf("no run reports found in %s", resultsDir)
	}

	// Deterministic order: scenario, device, iteration.
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].Run.Scenario != reports[j].Run.Scenario {
			return reports[i].Run.Scenario < reports[j].Run.Scenario
		}
		if reports[i].Run.DeviceID != reports[j].Run.DeviceID {
			return reports[i].Run.DeviceID < reports[j].Run.DeviceID
		}
		return reports[i].Run.Iteration < reports[j].Run.Iteration
	})

	cw := csv.NewWriter(w)
	defer cw.Flush()

	switch opts.Mode {
	case "", "samples":
		return writeSamplesCSV(cw, reports, opts.IncludeExtra)
	case "summary":
		return writeSummaryCSV(cw, reports)
	default:
		return 0, fmt.Errorf("unknown CSV mode %q (want \"samples\" or \"summary\")", opts.Mode)
	}
}

func writeSamplesCSV(cw *csv.Writer, reports []RunReport, includeExtra bool) (int, error) {
	header := []string{
		"scenario", "device_id", "platform", "iteration", "t",
		"fps", "frame_ms", "cpu_pct", "ram_mb", "jank_pct",
		"battery_pct", "battery_temp_c",
	}
	var extraKeys []string
	if includeExtra {
		set := map[string]struct{}{}
		for _, r := range reports {
			for _, s := range r.Run.Samples {
				for k := range s.Extra {
					set[k] = struct{}{}
				}
			}
		}
		extraKeys = make([]string, 0, len(set))
		for k := range set {
			extraKeys = append(extraKeys, k)
		}
		sort.Strings(extraKeys)
		for _, k := range extraKeys {
			header = append(header, "extra."+k)
		}
	}
	if err := cw.Write(header); err != nil {
		return 0, err
	}

	n := 0
	for _, r := range reports {
		for _, s := range r.Run.Samples {
			row := []string{
				r.Run.Scenario,
				r.Run.DeviceID,
				string(r.Run.Platform),
				strconv.Itoa(r.Run.Iteration),
				s.T.UTC().Format("2006-01-02T15:04:05.000Z"),
				fmtFloat(s.FPS),
				fmtFloat(s.FrameMS),
				fmtFloat(s.CPUPct),
				fmtFloat(s.RAMMB),
				fmtFloat(s.JankPct),
				fmtFloat(s.BatteryPct),
				fmtFloat(s.BatteryTempC),
			}
			if includeExtra {
				for _, k := range extraKeys {
					row = append(row, fmtFloat(extraOr0(s, k)))
				}
			}
			if err := cw.Write(row); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

func writeSummaryCSV(cw *csv.Writer, reports []RunReport) (int, error) {
	header := []string{
		"scenario", "device_id", "platform", "iteration", "metric",
		"count", "mean", "p50", "p90", "p99", "min", "max", "std",
	}
	if err := cw.Write(header); err != nil {
		return 0, err
	}

	n := 0
	for _, r := range reports {
		// Stable metric order: canonical first, then alphabetised extras.
		metricNames := orderedSummaryMetricKeys(r.Summary)
		for _, m := range metricNames {
			s := r.Summary[m]
			if s.Count == 0 {
				continue
			}
			row := []string{
				r.Run.Scenario,
				r.Run.DeviceID,
				string(r.Run.Platform),
				strconv.Itoa(r.Run.Iteration),
				m,
				strconv.Itoa(s.Count),
				fmtFloat(s.Mean), fmtFloat(s.P50), fmtFloat(s.P90), fmtFloat(s.P99),
				fmtFloat(s.Min), fmtFloat(s.Max), fmtFloat(s.Std),
			}
			if err := cw.Write(row); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

func orderedSummaryMetricKeys(m map[string]Summary) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for _, k := range canonicalMetricOrder {
		if _, ok := m[k]; ok {
			out = append(out, k)
		}
	}
	var extras []string
	for k := range m {
		canonical := false
		for _, c := range canonicalMetricOrder {
			if c == k {
				canonical = true
				break
			}
		}
		if !canonical {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)
	return append(out, extras...)
}

func extraOr0(s metrics.Sample, key string) float64 {
	if s.Extra == nil {
		return 0
	}
	return s.Extra[key]
}

// fmtFloat formats a sample value: empty for zero/NaN (CSV-friendly), else
// up to 4 significant decimals trimmed of trailing zeros. We treat 0 as
// "missing" to match how the JSON report serialises absent metrics
// (omitempty + zero-valued).
func fmtFloat(f float64) string {
	if f == 0 {
		return ""
	}
	s := strconv.FormatFloat(f, 'f', 4, 64)
	// Trim trailing zeros + trailing dot, but keep at least one digit.
	if i := len(s) - 1; i > 0 {
		for i > 0 && s[i] == '0' {
			i--
		}
		if s[i] == '.' {
			i--
		}
		s = s[:i+1]
	}
	return s
}

// WriteCSVFile is a convenience for writing CSV to outPath. If outPath is
// "-" the output goes to stdout via the caller — use WriteCSV directly in
// that case; this helper always opens a file.
func WriteCSVFile(resultsDir, outPath string, opts CSVOptions) (int, error) {
	f, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return WriteCSV(resultsDir, f, opts)
}
