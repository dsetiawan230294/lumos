package report

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

func makeCSVRun(scenario, dev string, plat metrics.Platform, iter int, samples []metrics.Sample) RunReport {
	r := metrics.Run{
		Scenario:  scenario,
		DeviceID:  dev,
		Platform:  plat,
		Iteration: iter,
		StartedAt: time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 5, 25, 10, 0, 5, 0, time.UTC),
		Samples:   samples,
	}
	return RunReport{
		Schema:  "1",
		Tool:    "lumos",
		Version: "test",
		Run:     r,
		Summary: SummarizeRun(r),
	}
}

func TestWriteCSV_Samples_BasicShape(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC)
	writeTrendFixture(t, dir, "scroll_001_DEV.json",
		makeCSVRun("scroll", "DEV", metrics.Android, 1, []metrics.Sample{
			{T: t0, FPS: 60, CPUPct: 12.5, RAMMB: 200, JankPct: 1.5},
			{T: t0.Add(time.Second), FPS: 58, CPUPct: 13.0, RAMMB: 205, JankPct: 2.0},
		}),
	)

	var buf bytes.Buffer
	n, err := WriteCSV(dir, &buf, CSVOptions{Mode: "samples"})
	if err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	if n != 2 {
		t.Errorf("rows=%d, want 2", n)
	}

	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("csv rows=%d (incl header), want 3", len(rows))
	}
	header := rows[0]
	wantHeaderPrefix := []string{"scenario", "device_id", "platform", "iteration", "t", "fps", "frame_ms", "cpu_pct", "ram_mb", "jank_pct", "battery_pct", "battery_temp_c"}
	if len(header) != len(wantHeaderPrefix) {
		t.Errorf("header len=%d, want %d (no extras requested)", len(header), len(wantHeaderPrefix))
	}
	for i, h := range wantHeaderPrefix {
		if header[i] != h {
			t.Errorf("header[%d]=%q, want %q", i, header[i], h)
		}
	}

	// Row 1 sanity.
	r1 := rows[1]
	if r1[0] != "scroll" || r1[1] != "DEV" || r1[2] != "android" || r1[3] != "1" {
		t.Errorf("row1 identity wrong: %v", r1[:4])
	}
	// FPS = 60 should render compactly.
	if r1[5] != "60" {
		t.Errorf("fps cell=%q, want %q", r1[5], "60")
	}
	// CPU = 12.5 should render with 1 decimal.
	if r1[7] != "12.5" {
		t.Errorf("cpu cell=%q, want %q", r1[7], "12.5")
	}
	// frame_ms = 0 → empty.
	if r1[6] != "" {
		t.Errorf("zero frame_ms cell=%q, want empty", r1[6])
	}
	// Timestamp should be ISO-8601 UTC.
	if !strings.HasSuffix(r1[4], "Z") {
		t.Errorf("timestamp=%q, want UTC suffix Z", r1[4])
	}
}

func TestWriteCSV_Samples_IncludesExtraColumns(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Now().UTC()
	writeTrendFixture(t, dir, "plugin_001_DEV.json",
		makeCSVRun("plugin", "DEV", metrics.Android, 1, []metrics.Sample{
			{T: t0, FPS: 60, Extra: map[string]float64{"gpu_temp_c": 47.5, "net_rx_kbps": 120}},
			{T: t0.Add(time.Second), FPS: 60, Extra: map[string]float64{"gpu_temp_c": 48.0}},
		}),
	)

	var buf bytes.Buffer
	if _, err := WriteCSV(dir, &buf, CSVOptions{Mode: "samples", IncludeExtra: true}); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	rows, _ := csv.NewReader(&buf).ReadAll()
	header := rows[0]
	// Extras come last in alphabetical order.
	want := []string{"extra.gpu_temp_c", "extra.net_rx_kbps"}
	got := header[len(header)-2:]
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("extra header[%d]=%q, want %q", i, got[i], want[i])
		}
	}
	// Second row had no net_rx_kbps → cell must be empty.
	netCol := len(header) - 1
	if rows[2][netCol] != "" {
		t.Errorf("missing extra cell=%q, want empty", rows[2][netCol])
	}
	if rows[1][netCol] != "120" {
		t.Errorf("present extra cell=%q, want 120", rows[1][netCol])
	}
}

func TestWriteCSV_Summary(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Now()
	writeTrendFixture(t, dir, "scroll_001_DEV.json",
		makeCSVRun("scroll", "DEV", metrics.Android, 1, []metrics.Sample{
			{T: t0, FPS: 60, CPUPct: 10},
			{T: t0.Add(time.Second), FPS: 62, CPUPct: 12},
			{T: t0.Add(2 * time.Second), FPS: 64, CPUPct: 14},
		}),
	)

	var buf bytes.Buffer
	n, err := WriteCSV(dir, &buf, CSVOptions{Mode: "summary"})
	if err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	if n < 2 {
		t.Errorf("rows=%d, want >=2 (fps + cpu_pct)", n)
	}
	rows, _ := csv.NewReader(&buf).ReadAll()
	header := rows[0]
	wantHeader := []string{"scenario", "device_id", "platform", "iteration", "metric", "count", "mean", "p50", "p90", "p99", "min", "max", "std"}
	for i, h := range wantHeader {
		if header[i] != h {
			t.Errorf("summary header[%d]=%q, want %q", i, header[i], h)
		}
	}
	// First metric should be fps (canonical order), with count=3.
	if rows[1][4] != "fps" {
		t.Errorf("first metric=%q, want fps", rows[1][4])
	}
	if rows[1][5] != "3" {
		t.Errorf("count=%q, want 3", rows[1][5])
	}
}

func TestWriteCSV_DeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Now()
	// Insert in scrambled order; output should be sorted by (scenario, device, iter).
	writeTrendFixture(t, dir, "b.json", makeCSVRun("scroll", "DEV2", metrics.Android, 1, []metrics.Sample{{T: t0, FPS: 60}}))
	writeTrendFixture(t, dir, "a.json", makeCSVRun("scroll", "DEV1", metrics.Android, 2, []metrics.Sample{{T: t0, FPS: 60}}))
	writeTrendFixture(t, dir, "c.json", makeCSVRun("scroll", "DEV1", metrics.Android, 1, []metrics.Sample{{T: t0, FPS: 60}}))

	var buf bytes.Buffer
	if _, err := WriteCSV(dir, &buf, CSVOptions{Mode: "samples"}); err != nil {
		t.Fatal(err)
	}
	rows, _ := csv.NewReader(&buf).ReadAll()
	// Expect DEV1/1, DEV1/2, DEV2/1.
	want := [][2]string{{"DEV1", "1"}, {"DEV1", "2"}, {"DEV2", "1"}}
	for i, w := range want {
		got := [2]string{rows[i+1][1], rows[i+1][3]}
		if got != w {
			t.Errorf("row %d=%v, want %v", i, got, w)
		}
	}
}

func TestWriteCSV_RejectsUnknownMode(t *testing.T) {
	dir := t.TempDir()
	writeTrendFixture(t, dir, "a.json", makeCSVRun("s", "D", metrics.Android, 1, []metrics.Sample{{T: time.Now(), FPS: 1}}))

	if _, err := WriteCSV(dir, &bytes.Buffer{}, CSVOptions{Mode: "histogram"}); err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

func TestWriteCSV_EmptyDirIsError(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteCSV(dir, &bytes.Buffer{}, CSVOptions{}); err == nil {
		t.Fatal("expected error for empty results dir")
	}
}

func TestFmtFloat(t *testing.T) {
	cases := map[float64]string{
		0:        "",
		60:       "60",
		60.5:     "60.5",
		12.345:   "12.345",
		12.34567: "12.3457", // rounded to 4 decimals
		0.0001:   "0.0001",
		-3.14:    "-3.14",
		100.0000: "100",
	}
	for in, want := range cases {
		if got := fmtFloat(in); got != want {
			t.Errorf("fmtFloat(%v)=%q, want %q", in, got, want)
		}
	}
}
