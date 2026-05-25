package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

func makeRun(t *testing.T, dir, scenario, device string, fps, cpu float64) string {
	t.Helper()
	run := metrics.Run{
		Scenario:  scenario,
		Iteration: 1,
		DeviceID:  device,
		Platform:  metrics.Android,
		StartedAt: time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 5, 25, 10, 0, 5, 0, time.UTC),
		Samples: []metrics.Sample{
			{FPS: fps, CPUPct: cpu, RAMMB: 100, JankPct: 1},
			{FPS: fps - 1, CPUPct: cpu + 1, RAMMB: 110, JankPct: 2},
			{FPS: fps + 1, CPUPct: cpu - 1, RAMMB: 105, JankPct: 1},
		},
	}
	path, err := WriteRun(dir, "lumos", "test", run)
	if err != nil {
		t.Fatalf("WriteRun: %v", err)
	}
	return path
}

func TestLoadRunReports_AndBuildAggregate(t *testing.T) {
	dir := t.TempDir()
	makeRun(t, dir, "smoke", "devA", 60, 10)
	makeRun(t, dir, "smoke", "devB", 30, 50)
	makeRun(t, dir, "scroll", "devA", 55, 25)

	// junk file should be skipped, not fail.
	if err := writeFile(filepath.Join(dir, "junk.json"), "not json"); err != nil {
		t.Fatal(err)
	}

	reports, skipped, err := LoadRunReports(dir)
	if err != nil {
		t.Fatalf("LoadRunReports: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("reports=%d, want 3", len(reports))
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped=%d, want 1: %v", len(skipped), skipped)
	}

	doc := BuildAggregate(reports)
	if len(doc.Scenarios) != 2 {
		t.Fatalf("scenarios=%d, want 2", len(doc.Scenarios))
	}
	// "scroll" sorts before "smoke" alphabetically.
	if doc.Scenarios[0].Name != "scroll" {
		t.Fatalf("scenario[0]=%q", doc.Scenarios[0].Name)
	}
	if len(doc.Scenarios[1].Devices) != 2 {
		t.Fatalf("smoke devices=%d", len(doc.Scenarios[1].Devices))
	}
	if doc.Scenarios[1].Aggregate["fps"].Count == 0 {
		t.Fatalf("aggregate fps not computed")
	}
}

func TestWriteHTMLReport_ContainsKeyFields(t *testing.T) {
	dir := t.TempDir()
	makeRun(t, dir, "smoke", "devA", 60, 10)
	makeRun(t, dir, "smoke", "devB", 30, 50)

	path, err := WriteHTMLReport(dir, "", "lumos", "v0.0.1-test")
	if err != nil {
		t.Fatalf("WriteHTMLReport: %v", err)
	}
	html := readFile(t, path)
	for _, want := range []string{"Lumos report", "v0.0.1-test", "smoke", "devA", "devB", "aggregate"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in HTML output", want)
		}
	}
}

func TestCompareFiles_DetectsRegression(t *testing.T) {
	baseDir := t.TempDir()
	curDir := t.TempDir()
	base := makeRun(t, baseDir, "smoke", "devA", 60, 10) // good
	cur := makeRun(t, curDir, "smoke", "devA", 30, 25)   // bad

	res, err := CompareFiles(base, cur, 5, nil)
	if err != nil {
		t.Fatalf("CompareFiles: %v", err)
	}
	if res.Pass {
		t.Fatalf("expected Pass=false")
	}
	if len(res.Regressions) == 0 {
		t.Fatalf("expected regressions")
	}
	// Must include fps regression (drop) and cpu regression (rise).
	saw := map[string]bool{}
	for _, d := range res.Regressions {
		saw[d.Metric] = true
	}
	if !saw["fps"] {
		t.Errorf("expected fps regression; got %+v", res.Regressions)
	}
	if !saw["cpu_pct"] {
		t.Errorf("expected cpu_pct regression; got %+v", res.Regressions)
	}
	table := res.FormatTable()
	if !strings.Contains(table, "REGRESSION") {
		t.Errorf("FormatTable missing REGRESSION line: %s", table)
	}
	if !strings.Contains(table, "FAIL") {
		t.Errorf("FormatTable missing FAIL: %s", table)
	}
}

func TestCompareFiles_PassWithinThreshold(t *testing.T) {
	baseDir := t.TempDir()
	curDir := t.TempDir()
	base := makeRun(t, baseDir, "smoke", "devA", 60, 20)
	cur := makeRun(t, curDir, "smoke", "devA", 59, 21)

	res, err := CompareFiles(base, cur, 10, nil)
	if err != nil {
		t.Fatalf("CompareFiles: %v", err)
	}
	if !res.Pass {
		t.Fatalf("expected Pass=true within threshold; got regressions=%+v", res.Regressions)
	}
}

func TestParseThreshold(t *testing.T) {
	cases := map[string]float64{"5": 5, "5%": 5, "5.5%": 5.5, " 10 % ": 10, "": 0}
	for in, want := range cases {
		got, err := ParseThreshold(in)
		if err != nil {
			t.Errorf("ParseThreshold(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseThreshold(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseThreshold("bogus"); err == nil {
		t.Errorf("expected error for 'bogus'")
	}
}

func TestSpark_StableForFlatInput(t *testing.T) {
	s := spark([]float64{5, 5, 5, 5})
	if len(s) == 0 {
		t.Fatalf("empty spark")
	}
	first := []rune(s)[0]
	for _, r := range s {
		if r != first {
			t.Fatalf("flat input produced varying ramp: %q", s)
		}
	}
}

// helpers

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
