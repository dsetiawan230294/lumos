package report

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed templates/report.html.tmpl
var reportTmplSrc string

// AggregateReport is the document fed to the HTML template. It contains
// one entry per scenario+device (RunBlock) plus a cross-device aggregate
// per scenario.
type AggregateReport struct {
	Tool        string
	Version     string
	GeneratedAt string
	Scenarios   []ScenarioBlock
}

type ScenarioBlock struct {
	Name      string
	Devices   []RunBlock
	Aggregate map[string]Summary // summary across all samples on all devices
}

type RunBlock struct {
	DeviceID   string
	Platform   string
	Iterations int // number of measured iterations rolled into this row
	Started    string
	Ended      string
	Summary    map[string]Summary
	Sparkline  map[string]string    // metric -> unicode sparkline (legacy)
	Series     map[string][]float64 // metric -> raw per-sample series (for SVG)
	Markers    int
	Error      string

	// Threads is the per-thread CPU% breakdown, averaged across every
	// sample on this device, sorted descending by mean. Empty when the
	// run was captured without --threads.
	Threads []ThreadRow

	// ThreadSeries is the per-sample CPU% series for the top-N hottest
	// threads (by mean), used to draw the stacked-area "Threads over time"
	// chart. Order matches the bottom-up stack order in the chart.
	ThreadSeries []ThreadSeries
}

// ThreadRow is one thread name's aggregated CPU footprint for a device.
type ThreadRow struct {
	Comm    string  // thread name (process comm), possibly a normalized group key like "binder:*"
	Mean    float64 // mean CPU% across all samples that observed this thread group
	Max     float64 // peak CPU% in any single sample
	Samples int     // number of samples in which this thread group appeared
	Count   int     // number of distinct raw thread names rolled into this group (>=1)
}

// ThreadSeries is one thread's per-sample CPU% timeline aligned to the
// device's concatenated sample timeline (same length as Series["fps"]).
type ThreadSeries struct {
	Comm   string
	Color  string
	Values []float64
}

// LoadRunReports scans dir for *.json files matching the WriteRun layout and
// decodes them. Files that do not parse are skipped (with their name and
// error returned in the second slice).
func LoadRunReports(dir string) ([]RunReport, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var out []RunReport
	var skipped []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(full)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		var doc RunReport
		if err := json.Unmarshal(b, &doc); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		if doc.Schema == "" {
			// Not one of ours.
			skipped = append(skipped, fmt.Sprintf("%s: missing schema", e.Name()))
			continue
		}
		out = append(out, doc)
	}
	return out, skipped, nil
}

// BuildAggregate groups run reports by scenario, then collapses iterations
// per device into a single averaged row. The per-row "Series" is the
// concatenation of every measured iteration's samples in chronological order
// so the inline chart shows the full timeline.
func BuildAggregate(reports []RunReport) AggregateReport {
	byScenario := map[string][]RunReport{}
	for _, r := range reports {
		byScenario[r.Run.Scenario] = append(byScenario[r.Run.Scenario], r)
	}
	names := make([]string, 0, len(byScenario))
	for n := range byScenario {
		names = append(names, n)
	}
	sort.Strings(names)

	var doc AggregateReport
	for _, n := range names {
		group := byScenario[n]
		// Group by device, then sort each device's runs by iteration so the
		// concatenated series reads left-to-right in run order.
		byDevice := map[string][]RunReport{}
		for _, r := range group {
			byDevice[r.Run.DeviceID] = append(byDevice[r.Run.DeviceID], r)
		}
		devices := make([]string, 0, len(byDevice))
		for d := range byDevice {
			devices = append(devices, d)
		}
		sort.Strings(devices)

		block := ScenarioBlock{Name: n}
		all := []metricsSampleRef{}

		for _, dev := range devices {
			runs := byDevice[dev]
			sort.Slice(runs, func(i, j int) bool {
				return runs[i].Run.StartedAt.Before(runs[j].Run.StartedAt)
			})
			// Concatenate every iteration's samples for this device.
			combined := []metricsSampleRef{}
			markers := 0
			firstErr := ""
			started := runs[0].Run.StartedAt
			ended := runs[len(runs)-1].Run.EndedAt
			// Per-thread aggregation across every iteration on this device.
			// We bucket by normalizeThreadName so e.g. binder:11840_1 and
			// binder:10678_4 collapse into a single "binder:*" group.
			type threadAcc struct {
				sum, max float64
				n        int
				rawNames map[string]struct{}
			}
			threadAccs := map[string]*threadAcc{}
			// Per-sample thread map keyed by *group name*. Multiple raw
			// threads sharing a group contribute additively to the same
			// per-tick CPU% value.
			perSample := []map[string]float64{}
			for _, r := range runs {
				markers += len(r.Run.Markers)
				if firstErr == "" {
					firstErr = r.Run.Error
				}
				for _, s := range r.Run.Samples {
					combined = append(combined, metricsSampleRef{
						FPS: s.FPS, FrameMS: s.FrameMS, CPUPct: s.CPUPct, RAMMB: s.RAMMB, JankPct: s.JankPct,
						BatteryPct: s.BatteryPct, BatteryTempC: s.BatteryTempC,
					})
					grouped := map[string]float64{}
					for name, pct := range s.Threads {
						key := normalizeThreadName(name)
						grouped[key] += pct
						a := threadAccs[key]
						if a == nil {
							a = &threadAcc{rawNames: map[string]struct{}{}}
							threadAccs[key] = a
						}
						a.rawNames[name] = struct{}{}
					}
					perSample = append(perSample, grouped)
					for key, pct := range grouped {
						a := threadAccs[key]
						a.sum += pct
						a.n++
						if pct > a.max {
							a.max = pct
						}
					}
				}
			}
			all = append(all, combined...)

			var threads []ThreadRow
			for name, a := range threadAccs {
				if a.n == 0 {
					continue
				}
				threads = append(threads, ThreadRow{
					Comm:    name,
					Mean:    a.sum / float64(a.n),
					Max:     a.max,
					Samples: a.n,
					Count:   len(a.rawNames),
				})
			}
			sort.Slice(threads, func(i, j int) bool {
				return threads[i].Mean > threads[j].Mean
			})

			// Build per-sample series for the top-N hottest threads. The
			// remainder is grouped into a synthetic "other" bucket so the
			// stacked area matches the process-wide CPU% line.
			threadSeries := buildThreadSeries(threads, perSample)

			block.Devices = append(block.Devices, RunBlock{
				DeviceID:     dev,
				Platform:     string(runs[0].Run.Platform),
				Iterations:   len(runs),
				Started:      started.Format("15:04:05"),
				Ended:        ended.Format("15:04:05"),
				Summary:      summarizeAll(combined),
				Series:       seriesFromRefs(combined),
				Markers:      markers,
				Error:        firstErr,
				Threads:      threads,
				ThreadSeries: threadSeries,
			})
		}

		block.Aggregate = summarizeAll(all)
		doc.Scenarios = append(doc.Scenarios, block)
	}
	return doc
}

// seriesFromRefs returns one per-sample slice per metric for the inline charts.
func seriesFromRefs(xs []metricsSampleRef) map[string][]float64 {
	n := len(xs)
	out := map[string][]float64{
		"fps":      make([]float64, n),
		"cpu_pct":  make([]float64, n),
		"ram_mb":   make([]float64, n),
		"jank_pct": make([]float64, n),
	}
	for i, s := range xs {
		out["fps"][i] = s.FPS
		out["cpu_pct"][i] = s.CPUPct
		out["ram_mb"][i] = s.RAMMB
		out["jank_pct"][i] = s.JankPct
	}
	return out
}

type metricsSampleRef struct {
	FPS, FrameMS, CPUPct, RAMMB, JankPct float64
	BatteryPct, BatteryTempC             float64
}

func summarizeAll(xs []metricsSampleRef) map[string]Summary {
	pick := func(get func(s metricsSampleRef) float64) Summary {
		out := make([]float64, 0, len(xs))
		for _, s := range xs {
			v := get(s)
			if v != 0 {
				out = append(out, v)
			}
		}
		return summarize(out)
	}
	return map[string]Summary{
		"fps":            pick(func(s metricsSampleRef) float64 { return s.FPS }),
		"frame_ms":       pick(func(s metricsSampleRef) float64 { return s.FrameMS }),
		"cpu_pct":        pick(func(s metricsSampleRef) float64 { return s.CPUPct }),
		"ram_mb":         pick(func(s metricsSampleRef) float64 { return s.RAMMB }),
		"jank_pct":       pick(func(s metricsSampleRef) float64 { return s.JankPct }),
		"battery_pct":    pick(func(s metricsSampleRef) float64 { return s.BatteryPct }),
		"battery_temp_c": pick(func(s metricsSampleRef) float64 { return s.BatteryTempC }),
	}
}

// sparkRamp is the unicode block ramp used by spark.
var sparkRamp = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

func spark(xs []float64) string {
	if len(xs) == 0 {
		return ""
	}
	mn, mx := xs[0], xs[0]
	for _, v := range xs {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	if mx == mn {
		return strings.Repeat(string(sparkRamp[len(sparkRamp)/2]), len(xs))
	}
	var b strings.Builder
	for _, v := range xs {
		ratio := (v - mn) / (mx - mn)
		idx := int(ratio * float64(len(sparkRamp)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkRamp) {
			idx = len(sparkRamp) - 1
		}
		b.WriteRune(sparkRamp[idx])
	}
	return b.String()
}

// RenderHTML writes an HTML report for the given aggregate to w.
func RenderHTML(buf *bytes.Buffer, doc AggregateReport) error {
	t, err := template.New("report").Funcs(template.FuncMap{
		"fmt0": func(f float64) string { return fmt.Sprintf("%.0f", f) },
		"fmt1": func(f float64) string { return fmt.Sprintf("%.1f", f) },
		"fmt2": func(f float64) string { return fmt.Sprintf("%.2f", f) },
		"svgLine": func(xs []float64, color string) template.HTML {
			return template.HTML(svgLineSeries(xs, color))
		},
		"svgThreads": threadsWidget,
		// verdict returns a CSS class for a metric value relative to soft/hard
		// thresholds. Direction comes from metric name.
		"verdictClass": verdictClass,
		// metricColor maps a metric name to a stable accent colour.
		"metricColor": metricColor,
		// hasMetric reports whether the summary has any samples for that metric.
		"hasMetric": func(m map[string]Summary, key string) bool {
			s, ok := m[key]
			return ok && s.Count > 0
		},
		"metricLabel": func(key string) string {
			switch key {
			case "fps":
				return "FPS"
			case "frame_ms":
				return "frame ms"
			case "cpu_pct":
				return "CPU %"
			case "ram_mb":
				return "RAM MB"
			case "jank_pct":
				return "jank %"
			case "battery_pct":
				return "battery %"
			case "battery_temp_c":
				return "batt °C"
			}
			return key
		},
		"sub": func(a, b int) int { return a - b },
	}).Parse(reportTmplSrc)
	if err != nil {
		return err
	}
	return t.Execute(buf, doc)
}

// svgLineSeries draws a self-scaling SVG line chart for a per-sample series.
// 220×48 viewbox; transparent background so the panel colour shows through.
// Returns "" when there are no non-zero samples.
func svgLineSeries(xs []float64, color string) string {
	if len(xs) == 0 {
		return ""
	}
	// Drop leading/trailing zeros so empty metrics don't dominate the chart.
	var mn, mx float64
	have := false
	for _, v := range xs {
		if v == 0 {
			continue
		}
		if !have {
			mn, mx = v, v
			have = true
			continue
		}
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	if !have {
		return ""
	}
	// Pad min/max by 5% so the line never grazes the edge.
	pad := (mx - mn) * 0.05
	if pad == 0 {
		pad = 0.5
	}
	mn -= pad
	mx += pad

	// Geometry: leave a left gutter for y-labels and a bottom gutter for
	// x-labels so axes render outside the data area.
	const (
		W       = 800.0
		H       = 200.0
		marginL = 48.0
		marginR = 12.0
		marginT = 10.0
		marginB = 18.0
	)
	plotW := W - marginL - marginR
	plotH := H - marginT - marginB
	if color == "" {
		color = "#22d3ee"
	}

	// Axis colours / typography (literal because SVG can't read CSS vars).
	const (
		axisColor = "#323844"
		gridColor = "#262b33"
		textColor = "#9aa3af"
		font      = "ui-monospace,Menlo,monospace"
		fontSize  = 8.0
	)

	// Helpers to map data → screen coords.
	dx := plotW / float64(maxInt(len(xs)-1, 1))
	xAt := func(i int) float64 { return marginL + float64(i)*dx }
	yAt := func(v float64) float64 {
		return marginT + plotH - (v-mn)/(mx-mn)*plotH
	}

	// Line + area paths.
	var pts strings.Builder
	for i, v := range xs {
		if v == 0 {
			v = mn
		}
		if i > 0 {
			pts.WriteByte(' ')
		}
		fmt.Fprintf(&pts, "%.1f,%.1f", xAt(i), yAt(v))
	}
	var area strings.Builder
	fmt.Fprintf(&area, "M%.1f,%.1f ", marginL, marginT+plotH)
	for i, v := range xs {
		if v == 0 {
			v = mn
		}
		fmt.Fprintf(&area, "L%.1f,%.1f ", xAt(i), yAt(v))
	}
	fmt.Fprintf(&area, "L%.1f,%.1f Z", xAt(len(xs)-1), marginT+plotH)

	gid := fmt.Sprintf("g%x", unsafePointerOf(xs)&0xFFFF)
	var b strings.Builder
	// preserveAspectRatio defaults to xMidYMid meet so axis labels never stretch.
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %g %g">`, W, H)
	fmt.Fprintf(&b, `<defs><linearGradient id="%s" x1="0" x2="0" y1="0" y2="1"><stop offset="0%%" stop-color="%s" stop-opacity="0.35"/><stop offset="100%%" stop-color="%s" stop-opacity="0"/></linearGradient></defs>`, gid, color, color)

	// Y-axis: 4 ticks (mn, +1/3, +2/3, mx). Horizontal grid + label.
	for i := 0; i <= 3; i++ {
		v := mn + (mx-mn)*float64(i)/3.0
		y := yAt(v)
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1" stroke-dasharray="2 3"/>`,
			marginL, y, W-marginR, y, gridColor)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-family="%s" font-size="%.0f" fill="%s" text-anchor="end" dominant-baseline="middle">%s</text>`,
			marginL-6, y, font, fontSize, textColor, formatTick(v))
	}
	// X-axis: 5 labels at evenly spaced sample indices.
	xTicks := 4
	if len(xs)-1 < xTicks {
		xTicks = maxInt(len(xs)-1, 1)
	}
	for i := 0; i <= xTicks; i++ {
		idx := i * (len(xs) - 1) / xTicks
		if len(xs) == 1 {
			idx = 0
		}
		x := xAt(idx)
		// Tick mark.
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1"/>`,
			x, marginT+plotH, x, marginT+plotH+4, axisColor)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-family="%s" font-size="%.0f" fill="%s" text-anchor="middle">%d</text>`,
			x, marginT+plotH+12, font, fontSize, textColor, idx)
	}
	// Axis lines (bottom + left) drawn after grid so they sit on top.
	fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1"/>`,
		marginL, marginT, marginL, marginT+plotH, axisColor)
	fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1"/>`,
		marginL, marginT+plotH, W-marginR, marginT+plotH, axisColor)

	// Series path + gradient fill.
	fmt.Fprintf(&b, `<path d="%s" fill="url(#%s)"/>`, area.String(), gid)
	fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" points="%s"/>`,
		color, pts.String())

	b.WriteString(`</svg>`)
	return b.String()
}

// formatTick renders an axis tick label compactly: integer when |v|>=10, else
// 1 decimal place, with trailing zeros stripped.
func formatTick(v float64) string {
	abs := v
	if abs < 0 {
		abs = -abs
	}
	var s string
	if abs >= 100 {
		s = fmt.Sprintf("%.0f", v)
	} else if abs >= 10 {
		s = fmt.Sprintf("%.1f", v)
	} else {
		s = fmt.Sprintf("%.2f", v)
	}
	// Trim trailing zeros + trailing decimal point.
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// unsafePointerOf returns a pseudo-unique identifier for the slice header,
// used to give each inline SVG <linearGradient> a non-colliding id without
// pulling in a counter. Collisions don't break anything visually because the
// same colour is reused per metric, but we still try to vary it.
func unsafePointerOf(xs []float64) uintptr {
	if len(xs) == 0 {
		return 0
	}
	return uintptr(len(xs)*131) + uintptr(int64(xs[0]*1000))
}

// metricColor returns the stable theme colour used for each metric.
func metricColor(metric string) string {
	switch metric {
	case "fps":
		return "#22d3ee" // cyan
	case "cpu_pct":
		return "#f97316" // orange
	case "ram_mb":
		return "#a78bfa" // violet
	case "jank_pct":
		return "#f43f5e" // rose
	case "frame_ms":
		return "#fbbf24" // amber
	case "battery_pct":
		return "#34d399" // emerald
	case "battery_temp_c":
		return "#fb7185" // pink
	}
	return "#9ca3af"
}

// verdictClass returns "good", "warn", "bad", or "" depending on whether the
// metric value crosses soft / hard thresholds. Thresholds are deliberately
// loose — they're hints for at-a-glance scanning, not a perf gate.
func verdictClass(metric string, value float64) string {
	if value == 0 {
		return ""
	}
	switch metric {
	case "fps":
		switch {
		case value >= 55:
			return "good"
		case value >= 45:
			return "warn"
		default:
			return "bad"
		}
	case "cpu_pct":
		switch {
		case value <= 25:
			return "good"
		case value <= 50:
			return "warn"
		default:
			return "bad"
		}
	case "ram_mb":
		switch {
		case value <= 200:
			return "good"
		case value <= 400:
			return "warn"
		default:
			return "bad"
		}
	case "jank_pct":
		switch {
		case value <= 2:
			return "good"
		case value <= 5:
			return "warn"
		default:
			return "bad"
		}
	case "frame_ms":
		switch {
		case value <= 16.7:
			return "good"
		case value <= 33:
			return "warn"
		default:
			return "bad"
		}
	case "battery_temp_c":
		switch {
		case value <= 35:
			return "good"
		case value <= 40:
			return "warn"
		default:
			return "bad"
		}
	}
	return ""
}

// WriteHTMLReport scans resultsDir, builds an aggregate, and writes
// <resultsDir>/report.html (or the explicit outPath if provided).
// Returns the written path.
func WriteHTMLReport(resultsDir, outPath, tool, version string) (string, error) {
	reports, _, err := LoadRunReports(resultsDir)
	if err != nil {
		return "", err
	}
	if len(reports) == 0 {
		return "", fmt.Errorf("no run reports found in %s", resultsDir)
	}
	doc := BuildAggregate(reports)
	doc.Tool = tool
	doc.Version = version
	doc.GeneratedAt = nowFn().Format("2006-01-02 15:04:05")

	var buf bytes.Buffer
	if err := RenderHTML(&buf, doc); err != nil {
		return "", err
	}
	if outPath == "" {
		outPath = filepath.Join(resultsDir, "report.html")
	}
	if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return outPath, nil
}

// WritePerScenarioReports writes one HTML report per scenario into
// resultsDir, named report_<scenario>.html. Useful when you want to share
// or diff a single scenario without the combined aggregate. Returns the
// list of written file paths in alphabetical scenario order.
func WritePerScenarioReports(resultsDir, tool, version string) ([]string, error) {
	reports, _, err := LoadRunReports(resultsDir)
	if err != nil {
		return nil, err
	}
	if len(reports) == 0 {
		return nil, fmt.Errorf("no run reports found in %s", resultsDir)
	}
	byScenario := map[string][]RunReport{}
	for _, r := range reports {
		byScenario[r.Run.Scenario] = append(byScenario[r.Run.Scenario], r)
	}
	names := make([]string, 0, len(byScenario))
	for n := range byScenario {
		names = append(names, n)
	}
	sort.Strings(names)

	genAt := nowFn().Format("2006-01-02 15:04:05")
	written := make([]string, 0, len(names))
	for _, n := range names {
		doc := BuildAggregate(byScenario[n])
		doc.Tool = tool
		doc.Version = version
		doc.GeneratedAt = genAt
		var buf bytes.Buffer
		if err := RenderHTML(&buf, doc); err != nil {
			return nil, fmt.Errorf("render %s: %w", n, err)
		}
		outPath := filepath.Join(resultsDir, "report_"+safeName(n)+".html")
		if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", outPath, err)
		}
		written = append(written, outPath)
	}
	return written, nil
}

// threadPalette is a stable, colour-blind-tolerant cycle for stacked thread
// areas. Ordering is fixed so the same thread name lands on the same colour
// across reports (combined with sort-by-mean determinism upstream).
var threadPalette = []string{
	"#6ee7b7", // mint
	"#60a5fa", // blue
	"#f59e0b", // amber
	"#f472b6", // pink
	"#a78bfa", // violet
	"#34d399", // emerald
	"#fb7185", // rose
	"#fbbf24", // yellow
	"#22d3ee", // cyan
	"#c084fc", // purple
}

// maxThreadSeries caps the number of top threads rendered as distinct
// bands in the stacked chart; everything else collapses into "other".
const maxThreadSeries = 8

// normalizeThreadName collapses dynamic per-thread identifiers (pids, indexes)
// into a stable group key by replacing every maximal run of ASCII digits with
// "*". Examples:
//
//	binder:11840_1   -> binder:*_*
//	HwBinder:228_3   -> HwBinder:*_*
//	pool-1-thread-4  -> pool-*-thread-*
//	RenderThread     -> RenderThread        (unchanged)
//	mali-cmar-backe  -> mali-cmar-backe     (unchanged)
//
// This is a deliberately simple rule that matches how the Android kernel
// labels short-lived worker threads — a digit run almost always encodes a
// process or thread index, not a meaningful name component.
func normalizeThreadName(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	inDigits := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			if !inDigits {
				b.WriteByte('*')
				inDigits = true
			}
			continue
		}
		inDigits = false
		b.WriteByte(c)
	}
	return b.String()
}

// buildThreadSeries returns one per-sample CPU% slice per top-N thread
// (by mean), in stack order (largest at bottom). A synthetic "other"
// band rolls up everything else so the stack total matches the
// process-wide CPU% line.
func buildThreadSeries(threads []ThreadRow, perSample []map[string]float64) []ThreadSeries {
	if len(threads) == 0 || len(perSample) == 0 {
		return nil
	}
	top := threads
	if len(top) > maxThreadSeries {
		top = top[:maxThreadSeries]
	}
	topSet := make(map[string]int, len(top))
	out := make([]ThreadSeries, 0, len(top)+1)
	for i, t := range top {
		topSet[t.Comm] = i
		out = append(out, ThreadSeries{
			Comm:   t.Comm,
			Color:  threadPalette[i%len(threadPalette)],
			Values: make([]float64, len(perSample)),
		})
	}
	other := ThreadSeries{
		Comm:   "other",
		Color:  "#64748b",
		Values: make([]float64, len(perSample)),
	}
	otherUsed := false
	for i, sm := range perSample {
		for name, pct := range sm {
			if idx, ok := topSet[name]; ok {
				out[idx].Values[i] += pct
			} else {
				other.Values[i] += pct
				if pct > 0 {
					otherUsed = true
				}
			}
		}
	}
	if otherUsed {
		out = append(out, other)
	}
	return out
}

// defaultVisibleThreads is the number of top threads enabled by default in
// the interactive dropdown; the rest are unchecked so the chart focuses on
// the hottest series. Users can toggle the rest via the dropdown.
const defaultVisibleThreads = 1

// threadsWidget renders a complete interactive threads panel: stacked-area
// SVG chart + JSON data island + dropdown thread picker. The chart's
// polygons are redrawn client-side whenever the picker selection changes.
// svgID must be unique within the document.
func threadsWidget(series []ThreadSeries, svgID string) template.HTML {
	svg := svgStackedThreads(series, svgID)
	if svg == "" {
		return ""
	}
	// Embed series data as JSON so the inline JS can recompute polygons.
	// We only need {color, values} per series — geometry constants are
	// re-derived from the SVG's viewBox + data-ymax.
	type wireSeries struct {
		Color  string    `json:"c"`
		Values []float64 `json:"v"`
	}
	wire := make([]wireSeries, len(series))
	for i, s := range series {
		wire[i] = wireSeries{Color: s.Color, Values: s.Values}
	}
	data, _ := json.Marshal(wire)
	// Defensive: prevent premature </script> termination if a comm contains it.
	safe := strings.ReplaceAll(string(data), "</", "<\\/")

	var b strings.Builder
	b.WriteString(`<div class="threads-chart" data-lumos-threads="`)
	b.WriteString(svgID)
	b.WriteString(`">`)
	b.WriteString(string(svg))
	fmt.Fprintf(&b, `<script type="application/json" class="lumos-thread-data">%s</script>`, safe)
	b.WriteString(`</div>`)

	// Dropdown picker. <details> + <summary> gives us native click-to-toggle
	// with no JS plumbing; the inner list holds one checkbox per thread.
	// Top-N threads start checked; the rest start unchecked.
	visible := defaultVisibleThreads
	if visible > len(series) {
		visible = len(series)
	}
	fmt.Fprintf(&b, `<details class="threads-picker" data-default-visible="%d"><summary>`, visible)
	fmt.Fprintf(&b, `<span class="threads-picker-label">Threads</span> <span class="threads-picker-count">%d / %d</span><span class="threads-picker-caret">▾</span>`, visible, len(series))
	b.WriteString(`</summary><div class="threads-picker-panel">`)
	b.WriteString(`<div class="threads-picker-tools"><button type="button" data-lumos-threads-all="1">all</button><button type="button" data-lumos-threads-all="0">none</button><button type="button" data-lumos-threads-top="1">top ` + fmt.Sprintf("%d", visible) + `</button></div>`)
	b.WriteString(`<ul class="threads-picker-list">`)
	for i, s := range series {
		checked := ""
		if i < visible {
			checked = " checked"
		}
		fmt.Fprintf(&b,
			`<li><label class="lg%s" data-thread="%d"><input type="checkbox"%s data-thread="%d"><span class="sw" style="background:%s"></span><span class="tn">%s</span></label></li>`,
			func() string {
				if checked == "" {
					return " off"
				}
				return ""
			}(),
			i, checked, i, s.Color, template.HTMLEscapeString(s.Comm))
	}
	b.WriteString(`</ul></div></details>`)
	return template.HTML(b.String())
}

// across the timeline. Returns "" when there are no series.
//
// Renders one polyline per thread (no fill), matching the metric-chart
// aesthetic above. Each line is tagged with data-thread="<idx>" so the
// inline JS toggles visibility when the dropdown selection changes.
//
// Note: the function name is kept for stability; the chart is no longer
// a stacked-area — overlaid lines read cleaner when a user picks just one
// or two threads from the dropdown.
func svgStackedThreads(series []ThreadSeries, svgID string) template.HTML {
	if len(series) == 0 {
		return ""
	}
	n := len(series[0].Values)
	if n < 2 {
		return ""
	}
	// Peak across ALL series so the initial axis covers any selection the
	// user may make via the dropdown. JS rescales to the visible peak at
	// runtime so the focused line uses the full plot height.
	peak := 0.0
	for _, s := range series {
		for _, v := range s.Values {
			if v > peak {
				peak = v
			}
		}
	}
	if peak <= 0 {
		return ""
	}
	ymax := niceCeil(peak)

	const (
		w       = 800.0
		h       = 200.0
		marginL = 40.0
		marginR = 8.0
		marginT = 8.0
		marginB = 18.0
	)
	plotW := w - marginL - marginR
	plotH := h - marginT - marginB
	xAt := func(i int) float64 { return marginL + plotW*float64(i)/float64(n-1) }
	yAt := func(v float64) float64 { return marginT + plotH - plotH*(v/ymax) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg id="%s" viewBox="0 0 %.0f %.0f" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Per-thread CPU%% over time" data-ymax="%g">`, svgID, w, h, ymax)
	// Axis group: dashed gridlines + tick labels (JS only updates ytick text).
	b.WriteString(`<g class="axes">`)
	for t := 0; t <= 4; t++ {
		v := ymax * float64(t) / 4.0
		y := yAt(v)
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#1f2937" stroke-width="1" stroke-dasharray="2 4"/>`, marginL, y, w-marginR, y)
		fmt.Fprintf(&b, `<text class="ytick" x="%.1f" y="%.1f" text-anchor="end" dominant-baseline="middle" fill="#64748b" font-size="8" font-family="ui-monospace, Menlo, monospace">%s</text>`, marginL-4, y, formatTick(v))
	}
	for t := 0; t <= 4; t++ {
		idx := (n - 1) * t / 4
		x := xAt(idx)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle" fill="#64748b" font-size="8" font-family="ui-monospace, Menlo, monospace">%d</text>`, x, h-4, idx)
	}
	b.WriteString(`</g>`)

	// Lines. Only the first defaultVisibleThreads series are visible; the
	// rest carry display:none so the dropdown's default state matches.
	b.WriteString(`<g class="bands" fill="none" stroke-linejoin="round" stroke-linecap="round">`)
	for idx, s := range series {
		var p strings.Builder
		for i, v := range s.Values {
			if i > 0 {
				p.WriteByte(' ')
			}
			fmt.Fprintf(&p, "%.1f,%.1f", xAt(i), yAt(v))
		}
		hidden := ""
		if idx >= defaultVisibleThreads {
			hidden = ` style="display:none"`
		}
		fmt.Fprintf(&b, `<polyline data-thread="%d" points="%s" stroke="%s" stroke-width="1.8" stroke-opacity="0.95"%s/>`, idx, p.String(), s.Color, hidden)
	}
	b.WriteString(`</g></svg>`)
	return template.HTML(b.String())
}

// niceCeil rounds v up to a "tidy" round number for axis labels.
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	// Snap to next multiple of 10/25/50/100/…
	mag := 1.0
	for v/mag > 10 {
		mag *= 10
	}
	r := v / mag
	switch {
	case r <= 1:
		return 1 * mag
	case r <= 2:
		return 2 * mag
	case r <= 2.5:
		return 2.5 * mag
	case r <= 5:
		return 5 * mag
	default:
		return 10 * mag
	}
}
