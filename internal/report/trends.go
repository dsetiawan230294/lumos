package report

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed templates/trends.html.tmpl
var trendsTmplSrc string

// TrendsDoc is the document fed to the trends HTML template.
type TrendsDoc struct {
	Tool        string
	Version     string
	GeneratedAt string
	Roots       []string
	Series      []TrendSeries
	TotalRuns   int
}

// TrendSeries is one (scenario, device, platform) tuple's history.
type TrendSeries struct {
	Scenario string
	DeviceID string
	Platform string
	Runs     int
	Metrics  []TrendMetric
}

// TrendMetric is the historical trace of one metric for one series.
type TrendMetric struct {
	Name      string
	Points    []TrendPoint
	First     float64
	Last      float64
	Min       float64
	Max       float64
	DeltaPct  float64       // (Last-First)/First*100, 0 if First==0
	Verdict   string        // "improved" | "regressed" | "flat"
	Color     string        // CSS colour for verdict
	SparkSVG  template.HTML // inline SVG
	UnitLabel string        // e.g. "%", "MB", "fps"
}

// TrendPoint is one observation in a metric trace.
type TrendPoint struct {
	T     time.Time
	Value float64
	Label string // human "2026-05-25 14:30"
}

// LoadRunReportsRecursive walks each root directory looking for *.json files
// that decode as RunReport with a non-empty schema. Files that fail to parse
// are returned in `skipped` with their error string. Missing roots return an
// error.
func LoadRunReportsRecursive(roots ...string) ([]RunReport, []string, error) {
	var out []RunReport
	var skipped []string
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, nil, fmt.Errorf("trends: %w", err)
		}
		if !info.IsDir() {
			return nil, nil, fmt.Errorf("trends: %s is not a directory", root)
		}
		werr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable entries, keep walking
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".json") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				skipped = append(skipped, fmt.Sprintf("%s: %v", p, rerr))
				return nil
			}
			var doc RunReport
			if jerr := json.Unmarshal(b, &doc); jerr != nil {
				skipped = append(skipped, fmt.Sprintf("%s: %v", p, jerr))
				return nil
			}
			if doc.Schema == "" {
				skipped = append(skipped, fmt.Sprintf("%s: missing schema", p))
				return nil
			}
			out = append(out, doc)
			return nil
		})
		if werr != nil {
			return nil, skipped, werr
		}
	}
	return out, skipped, nil
}

// BuildTrends groups runs by (scenario, device, platform), sorts each group
// chronologically, and computes per-metric trend lines from each run's mean.
//
// Warmup runs (scenario name ending in "__warmup") are excluded so the trend
// reflects measured iterations only.
func BuildTrends(reports []RunReport) TrendsDoc {
	type key struct {
		scen, dev, plat string
	}
	groups := map[key][]RunReport{}
	for _, r := range reports {
		if strings.HasSuffix(r.Run.Scenario, "__warmup") {
			continue
		}
		k := key{r.Run.Scenario, r.Run.DeviceID, string(r.Run.Platform)}
		groups[k] = append(groups[k], r)
	}

	doc := TrendsDoc{TotalRuns: len(reports)}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].scen != keys[j].scen {
			return keys[i].scen < keys[j].scen
		}
		if keys[i].dev != keys[j].dev {
			return keys[i].dev < keys[j].dev
		}
		return keys[i].plat < keys[j].plat
	})

	for _, k := range keys {
		grp := groups[k]
		sort.Slice(grp, func(i, j int) bool {
			return grp[i].Run.StartedAt.Before(grp[j].Run.StartedAt)
		})

		ts := TrendSeries{
			Scenario: k.scen,
			DeviceID: k.dev,
			Platform: k.plat,
			Runs:     len(grp),
		}

		// Union of metric keys across all runs in the group, deterministic
		// order: well-known metrics first (in canonical sequence), then any
		// extra.* keys alphabetically.
		keySet := map[string]struct{}{}
		for _, r := range grp {
			for m := range r.Summary {
				keySet[m] = struct{}{}
			}
		}
		metricKeys := orderedMetricKeys(keySet)

		for _, m := range metricKeys {
			tm := TrendMetric{Name: m, UnitLabel: unitFor(m)}
			for _, r := range grp {
				s, ok := r.Summary[m]
				if !ok || s.Count == 0 {
					continue
				}
				tm.Points = append(tm.Points, TrendPoint{
					T:     r.Run.StartedAt,
					Value: s.Mean,
					Label: r.Run.StartedAt.Format("2006-01-02 15:04"),
				})
			}
			if len(tm.Points) == 0 {
				continue
			}
			tm.First = tm.Points[0].Value
			tm.Last = tm.Points[len(tm.Points)-1].Value
			tm.Min = tm.Points[0].Value
			tm.Max = tm.Points[0].Value
			for _, p := range tm.Points {
				if p.Value < tm.Min {
					tm.Min = p.Value
				}
				if p.Value > tm.Max {
					tm.Max = p.Value
				}
			}
			if tm.First != 0 {
				tm.DeltaPct = (tm.Last - tm.First) / tm.First * 100.0
			}
			tm.Verdict, tm.Color = verdictFor(m, tm.DeltaPct)
			tm.SparkSVG = template.HTML(svgSparkline(tm.Points, tm.Min, tm.Max, tm.Color))
			ts.Metrics = append(ts.Metrics, tm)
		}
		doc.Series = append(doc.Series, ts)
	}
	return doc
}

var canonicalMetricOrder = []string{
	"fps", "frame_ms", "cpu_pct", "ram_mb", "jank_pct", "battery_pct", "battery_temp_c",
}

func orderedMetricKeys(set map[string]struct{}) []string {
	var ordered []string
	for _, k := range canonicalMetricOrder {
		if _, ok := set[k]; ok {
			ordered = append(ordered, k)
		}
	}
	// Extras (anything not in the canonical list), alphabetised.
	var extras []string
	for k := range set {
		isCanonical := false
		for _, c := range canonicalMetricOrder {
			if c == k {
				isCanonical = true
				break
			}
		}
		if !isCanonical {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)
	return append(ordered, extras...)
}

func unitFor(m string) string {
	switch m {
	case "fps":
		return "fps"
	case "frame_ms":
		return "ms"
	case "cpu_pct", "jank_pct", "battery_pct":
		return "%"
	case "ram_mb":
		return "MB"
	case "battery_temp_c":
		return "°C"
	}
	return ""
}

// verdictFor labels a delta as improved/regressed/flat using the same
// direction table as the compare command. A 1% deadband keeps tiny wiggles
// labelled "flat" rather than colour-coded.
func verdictFor(metric string, deltaPct float64) (verdict, color string) {
	if math.Abs(deltaPct) < 1.0 {
		return "flat", "#888"
	}
	dir, known := directions[metric]
	if !known {
		// Unknown metric (plugin extras): no opinion on direction.
		if deltaPct > 0 {
			return "↑", "#888"
		}
		return "↓", "#888"
	}
	if dir == HigherIsBetter {
		if deltaPct > 0 {
			return "improved", "#2a7"
		}
		return "regressed", "#c44"
	}
	// LowerIsBetter
	if deltaPct < 0 {
		return "improved", "#2a7"
	}
	return "regressed", "#c44"
}

// svgSparkline produces an inline SVG polyline (200×40) for the given points.
// No JS, no external CSS — embeds straight into the HTML report.
func svgSparkline(points []TrendPoint, min, max float64, stroke string) string {
	const W, H, pad = 200.0, 40.0, 2.0
	if len(points) == 0 {
		return ""
	}
	if stroke == "" {
		stroke = "#3366cc"
	}
	span := max - min
	if span == 0 {
		span = 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %g %g" width="%g" height="%g" preserveAspectRatio="none">`, W, H, W, H)
	b.WriteString(`<rect width="100%" height="100%" fill="#fafafa"/>`)

	n := len(points)
	pts := make([]string, 0, n)
	dx := (W - 2*pad) / math.Max(float64(n-1), 1)
	for i, p := range points {
		x := pad + float64(i)*dx
		y := H - pad - (p.Value-min)/span*(H-2*pad)
		pts = append(pts, fmt.Sprintf("%.1f,%.1f", x, y))
	}
	fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="1.5" points="%s"/>`, stroke, strings.Join(pts, " "))
	// Dot at the last point for emphasis.
	lastX := pad + float64(n-1)*dx
	lastY := H - pad - (points[n-1].Value-min)/span*(H-2*pad)
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="2.5" fill="%s"/>`, lastX, lastY, stroke)
	b.WriteString(`</svg>`)
	return b.String()
}

// RenderTrendsHTML writes a trends dashboard to buf.
func RenderTrendsHTML(buf *bytes.Buffer, doc TrendsDoc) error {
	t, err := template.New("trends").Funcs(template.FuncMap{
		"fmt2":  func(f float64) string { return fmt.Sprintf("%.2f", f) },
		"fmtPc": func(f float64) string { return fmt.Sprintf("%+.1f%%", f) },
	}).Parse(trendsTmplSrc)
	if err != nil {
		return err
	}
	return t.Execute(buf, doc)
}

// WriteTrendsHTML loads runs recursively from roots, builds the trends doc,
// and writes the HTML to outPath. Returns the path written.
func WriteTrendsHTML(roots []string, outPath, tool, version string) (string, error) {
	reports, _, err := LoadRunReportsRecursive(roots...)
	if err != nil {
		return "", err
	}
	if len(reports) == 0 {
		return "", fmt.Errorf("no run reports found under %s", strings.Join(roots, ", "))
	}
	doc := BuildTrends(reports)
	doc.Tool = tool
	doc.Version = version
	doc.GeneratedAt = nowFn().Format("2006-01-02 15:04:05")
	doc.Roots = roots

	var buf bytes.Buffer
	if err := RenderTrendsHTML(&buf, doc); err != nil {
		return "", err
	}
	if outPath == "" {
		outPath = filepath.Join(roots[0], "trends.html")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return outPath, nil
}
