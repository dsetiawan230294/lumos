package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

func writeTrendFixture(t *testing.T, dir, name string, doc RunReport) string {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func makeTrendRun(scenario, dev string, plat metrics.Platform, started time.Time, fps, cpu float64) RunReport {
	r := metrics.Run{
		Scenario:  scenario,
		DeviceID:  dev,
		Platform:  plat,
		StartedAt: started,
		EndedAt:   started.Add(2 * time.Second),
		Samples: []metrics.Sample{
			{T: started, FPS: fps, CPUPct: cpu},
			{T: started.Add(time.Second), FPS: fps + 1, CPUPct: cpu + 1},
		},
	}
	return RunReport{
		Schema:  "1",
		Tool:    "lumos",
		Version: "test",
		Run:     r,
		Summary: SummarizeRun(r),
	}
}

func TestLoadRunReportsRecursive_WalksSubdirsAndSkipsGarbage(t *testing.T) {
	root := t.TempDir()
	// Valid nested file.
	writeTrendFixture(t, filepath.Join(root, "day1"), "scroll_001_DEV.json",
		makeTrendRun("scroll", "DEV", metrics.Android, time.Now(), 60, 10))
	// Non-JSON: ignored.
	_ = os.WriteFile(filepath.Join(root, "README.txt"), []byte("hi"), 0o644)
	// JSON without schema: skipped (logged in skipped list).
	_ = os.WriteFile(filepath.Join(root, "alien.json"), []byte(`{"foo":1}`), 0o644)

	reports, skipped, err := LoadRunReportsRecursive(root)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(reports) != 1 {
		t.Errorf("reports=%d, want 1", len(reports))
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "missing schema") {
		t.Errorf("skipped=%v, want one missing-schema entry", skipped)
	}
}

func TestLoadRunReportsRecursive_MissingRootErrors(t *testing.T) {
	if _, _, err := LoadRunReportsRecursive("/nonexistent/lumos/path"); err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestBuildTrends_GroupsAndOrdersChronologically(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	// Two runs of same scenario+device, out-of-order on disk.
	writeTrendFixture(t, root, "b.json", makeTrendRun("scroll", "DEV1", metrics.Android, t0.Add(2*time.Hour), 70, 20))
	writeTrendFixture(t, root, "a.json", makeTrendRun("scroll", "DEV1", metrics.Android, t0, 60, 15))
	// Different device → separate series.
	writeTrendFixture(t, root, "c.json", makeTrendRun("scroll", "DEV2", metrics.Android, t0, 55, 25))
	// Warmup → excluded.
	writeTrendFixture(t, root, "w.json", makeTrendRun("scroll__warmup", "DEV1", metrics.Android, t0, 1, 1))

	reports, _, err := LoadRunReportsRecursive(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	doc := BuildTrends(reports)

	if len(doc.Series) != 2 {
		t.Fatalf("series=%d, want 2", len(doc.Series))
	}
	// Series are sorted by scenario, then device.
	if doc.Series[0].DeviceID != "DEV1" || doc.Series[1].DeviceID != "DEV2" {
		t.Errorf("device order = %s,%s; want DEV1,DEV2", doc.Series[0].DeviceID, doc.Series[1].DeviceID)
	}
	dev1 := doc.Series[0]
	if dev1.Runs != 2 {
		t.Errorf("DEV1 runs=%d, want 2 (warmup excluded)", dev1.Runs)
	}
	// Find the fps metric.
	var fps *TrendMetric
	for i := range dev1.Metrics {
		if dev1.Metrics[i].Name == "fps" {
			fps = &dev1.Metrics[i]
			break
		}
	}
	if fps == nil {
		t.Fatal("no fps metric")
	}
	if len(fps.Points) != 2 {
		t.Fatalf("fps points=%d, want 2", len(fps.Points))
	}
	// Chronological: earlier (60.5 mean) first, later (70.5 mean) second.
	if !fps.Points[0].T.Before(fps.Points[1].T) {
		t.Errorf("points not chronological: %v then %v", fps.Points[0].T, fps.Points[1].T)
	}
	if fps.First > fps.Last {
		t.Errorf("first=%g should be <= last=%g for improving FPS", fps.First, fps.Last)
	}
	// FPS went up → higher-is-better → improved.
	if fps.Verdict != "improved" {
		t.Errorf("fps verdict=%q, want improved", fps.Verdict)
	}
	// CPU went up → lower-is-better → regressed.
	var cpu *TrendMetric
	for i := range dev1.Metrics {
		if dev1.Metrics[i].Name == "cpu_pct" {
			cpu = &dev1.Metrics[i]
			break
		}
	}
	if cpu == nil {
		t.Fatal("no cpu_pct metric")
	}
	if cpu.Verdict != "regressed" {
		t.Errorf("cpu verdict=%q, want regressed", cpu.Verdict)
	}
}

func TestVerdictFor_FlatDeadband(t *testing.T) {
	if v, _ := verdictFor("fps", 0.4); v != "flat" {
		t.Errorf("fps Δ0.4%% verdict=%q, want flat", v)
	}
	if v, _ := verdictFor("fps", -0.9); v != "flat" {
		t.Errorf("fps Δ-0.9%% verdict=%q, want flat", v)
	}
	if v, _ := verdictFor("fps", 2); v != "improved" {
		t.Errorf("fps Δ+2%% verdict=%q, want improved", v)
	}
}

func TestSvgSparkline_BasicShape(t *testing.T) {
	pts := []TrendPoint{
		{T: time.Now(), Value: 1},
		{T: time.Now().Add(time.Hour), Value: 2},
		{T: time.Now().Add(2 * time.Hour), Value: 3},
	}
	svg := svgSparkline(pts, 1, 3, "#3366cc")
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "polyline") || !strings.Contains(svg, "circle") {
		t.Fatalf("svg missing expected elements: %s", svg)
	}
}

func TestWriteTrendsHTML_EndToEnd(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	writeTrendFixture(t, root, "a.json", makeTrendRun("scroll", "DEV1", metrics.Android, t0, 60, 15))
	writeTrendFixture(t, root, "b.json", makeTrendRun("scroll", "DEV1", metrics.Android, t0.Add(time.Hour), 65, 18))

	out := filepath.Join(t.TempDir(), "trends.html")
	got, err := WriteTrendsHTML([]string{root}, out, "lumos", "test")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if got != out {
		t.Errorf("path=%q, want %q", got, out)
	}
	b, _ := os.ReadFile(out)
	html := string(b)
	for _, want := range []string{"lumos · trends", "scroll", "DEV1", "<svg", "polyline"} {
		if !strings.Contains(html, want) {
			t.Errorf("html missing %q", want)
		}
	}
}

func TestWriteTrendsHTML_EmptyDirErrors(t *testing.T) {
	root := t.TempDir()
	if _, err := WriteTrendsHTML([]string{root}, "", "lumos", "test"); err == nil {
		t.Fatal("expected error for empty results dir")
	}
}
