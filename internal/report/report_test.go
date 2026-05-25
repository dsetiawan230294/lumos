package report

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

func TestSummarize(t *testing.T) {
	r := metrics.Run{
		Samples: []metrics.Sample{
			{FPS: 60, CPUPct: 10},
			{FPS: 55, CPUPct: 20},
			{FPS: 58, CPUPct: 30},
			{FPS: 30, CPUPct: 40},
		},
	}
	sum := SummarizeRun(r)
	fps := sum["fps"]
	if fps.Count != 4 {
		t.Errorf("fps.Count = %d, want 4", fps.Count)
	}
	if math.Abs(fps.Mean-50.75) > 1e-6 {
		t.Errorf("fps.Mean = %v, want 50.75", fps.Mean)
	}
	if fps.Min != 30 || fps.Max != 60 {
		t.Errorf("fps.Min/Max = %v/%v", fps.Min, fps.Max)
	}
}

func TestSummarizeSkipsZero(t *testing.T) {
	r := metrics.Run{
		Samples: []metrics.Sample{{FPS: 0, CPUPct: 10}, {FPS: 60, CPUPct: 0}},
	}
	sum := SummarizeRun(r)
	if sum["fps"].Count != 1 || sum["cpu_pct"].Count != 1 {
		t.Errorf("zero values should be filtered: %+v", sum)
	}
}

func TestWriteRun(t *testing.T) {
	dir := t.TempDir()
	r := metrics.Run{
		Scenario:  "home_scroll",
		Iteration: 2,
		DeviceID:  "ABCD1234",
		Platform:  metrics.Android,
		StartedAt: time.Now(),
		EndedAt:   time.Now(),
		Samples:   []metrics.Sample{{FPS: 60, CPUPct: 10, RAMMB: 200}},
	}
	path, err := WriteRun(dir, "lumos", "test", r)
	if err != nil {
		t.Fatalf("WriteRun: %v", err)
	}
	if filepath.Base(path) != "home_scroll_002_ABCD1234.json" {
		t.Errorf("unexpected filename: %s", filepath.Base(path))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc RunReport
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Schema != SchemaVersion || doc.Run.Scenario != "home_scroll" {
		t.Errorf("bad doc: %+v", doc)
	}
	if doc.Summary["fps"].Count != 1 {
		t.Errorf("fps summary not present")
	}
}

func TestSafeName(t *testing.T) {
	if safeName("home/scroll #1") != "home_scroll__1" {
		t.Errorf("safeName = %q", safeName("home/scroll #1"))
	}
	if safeName("") != "unknown" {
		t.Errorf("empty should become 'unknown'")
	}
}
