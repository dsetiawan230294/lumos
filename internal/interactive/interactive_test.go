package interactive

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

func TestModel_AddSampleAndSnapshot(t *testing.T) {
	m := NewModel([]*DevicePane{
		{ID: "d1", Platform: metrics.Android, AppID: "com.app"},
		{ID: "d2", Platform: metrics.IOS, AppID: "com.app.ios"},
	})
	m.AddSample("d1", metrics.Sample{FPS: 60, CPUPct: 12.5, RAMMB: 100, T: time.Now()})
	m.AddSample("d1", metrics.Sample{FPS: 55, CPUPct: 18.0, RAMMB: 110, T: time.Now()})
	m.AddSample("d2", metrics.Sample{FPS: 30, T: time.Now()})

	snap := m.Snapshot()
	if len(snap.Panes) != 2 {
		t.Fatalf("panes=%d, want 2", len(snap.Panes))
	}
	if snap.Panes[0].SampleCount != 2 {
		t.Fatalf("d1 SampleCount = %d, want 2", snap.Panes[0].SampleCount)
	}
	if snap.Panes[0].Latest.FPS != 55 {
		t.Fatalf("d1 Latest.FPS = %v, want 55", snap.Panes[0].Latest.FPS)
	}
	if snap.Panes[0].Status != "sampling" {
		t.Fatalf("d1 status = %q, want sampling", snap.Panes[0].Status)
	}
	if len(snap.Panes[0].Samples) != 2 {
		t.Fatalf("d1 Samples len = %d, want 2", len(snap.Panes[0].Samples))
	}
}

func TestModel_RingCapped_ButSamplesUnbounded(t *testing.T) {
	m := NewModel([]*DevicePane{{ID: "d1", Platform: metrics.Android}})
	m.ringCap = 4
	for i := 0; i < 20; i++ {
		m.AddSample("d1", metrics.Sample{CPUPct: float64(i)})
	}
	snap := m.Snapshot()
	if len(snap.Panes[0].Ring) != 4 {
		t.Fatalf("Ring len = %d, want 4", len(snap.Panes[0].Ring))
	}
	if snap.Panes[0].Ring[3].CPUPct != 19 {
		t.Fatalf("Ring tail = %v, want 19", snap.Panes[0].Ring[3].CPUPct)
	}
	if len(snap.Panes[0].Samples) != 20 {
		t.Fatalf("Samples len = %d, want 20 (unbounded)", len(snap.Panes[0].Samples))
	}
}

func TestModel_FocusCycling(t *testing.T) {
	m := NewModel([]*DevicePane{{ID: "a"}, {ID: "b"}, {ID: "c"}})
	if m.Snapshot().Focus != 0 {
		t.Fatalf("initial focus != 0")
	}
	m.FocusNext()
	m.FocusNext()
	if m.Snapshot().Focus != 2 {
		t.Fatalf("after 2x next, focus = %d", m.Snapshot().Focus)
	}
	m.FocusNext()
	if m.Snapshot().Focus != 0 {
		t.Fatalf("wrap-around failed: focus = %d", m.Snapshot().Focus)
	}
	m.FocusPrev()
	if m.Snapshot().Focus != 2 {
		t.Fatalf("prev wrap failed: focus = %d", m.Snapshot().Focus)
	}
}

func TestModel_Segments(t *testing.T) {
	m := NewModel([]*DevicePane{{ID: "d1"}})
	start := m.StartSegment("scroll")
	if start.Kind != "start" || start.Label != "scroll" {
		t.Fatalf("StartSegment marker = %+v", start)
	}
	snap := m.Snapshot()
	if snap.Panes[0].ActiveSegment != "scroll" {
		t.Fatalf("ActiveSegment = %q", snap.Panes[0].ActiveSegment)
	}
	end := m.EndSegment()
	if end.Kind != "end" || end.Label != "scroll" {
		t.Fatalf("EndSegment marker = %+v", end)
	}
	if m.Snapshot().Panes[0].ActiveSegment != "" {
		t.Fatalf("ActiveSegment not cleared")
	}
	// End with no active segment is a no-op.
	noop := m.EndSegment()
	if noop.Label != "" {
		t.Fatalf("expected zero marker, got %+v", noop)
	}
	point := m.MarkPoint("checkpoint")
	if point.Kind != "point" {
		t.Fatalf("MarkPoint kind = %q", point.Kind)
	}
	if got := len(m.Snapshot().Panes[0].Markers); got != 3 {
		t.Fatalf("markers = %d, want 3", got)
	}
}

func TestModel_ResetFocused(t *testing.T) {
	m := NewModel([]*DevicePane{{ID: "d1"}})
	m.AddSample("d1", metrics.Sample{CPUPct: 50})
	m.StartSegment("x")
	m.ResetFocused()
	snap := m.Snapshot()
	if snap.Panes[0].SampleCount != 0 || len(snap.Panes[0].Markers) != 0 {
		t.Fatalf("reset failed: %+v", snap.Panes[0])
	}
	if len(snap.Panes[0].Samples) != 0 {
		t.Fatalf("Samples not cleared")
	}
}

func TestSparkline_BasicShape(t *testing.T) {
	got := Sparkline([]float64{1, 2, 3, 4, 5, 6, 7, 8}, 8, 0)
	want := "▁▂▃▄▅▆▇█"
	if got != want {
		t.Fatalf("Sparkline = %q, want %q", got, want)
	}
}

func TestSparkline_ZerosBecomeGaps(t *testing.T) {
	got := Sparkline([]float64{1, 0, 2, 0}, 4, 0)
	if n := len([]rune(got)); n != 4 {
		t.Fatalf("rune len = %d, want 4 (got %q)", n, got)
	}
	if !strings.Contains(got, " ") {
		t.Fatalf("expected gaps for zero values, got %q", got)
	}
}

func TestSparkline_RightAlignedWhenShort(t *testing.T) {
	got := Sparkline([]float64{1, 1}, 6, 0)
	// 4 leading spaces, then 2 bars.
	if !strings.HasPrefix(got, "    ") {
		t.Fatalf("expected leading spaces, got %q", got)
	}
}

func TestRender_NoColourSnapshot(t *testing.T) {
	m := NewModel([]*DevicePane{
		{ID: "device-1", Platform: metrics.Android, Label: "Pixel 7", AppID: "com.example", Status: "sampling"},
	})
	m.AddSample("device-1", metrics.Sample{FPS: 60, CPUPct: 10, RAMMB: 100, JankPct: 1.0, T: time.Now()})
	var buf bytes.Buffer
	Render(&buf, m.Snapshot(), RenderConfig{Width: 80, NoColour: true})
	out := buf.String()
	for _, want := range []string{
		"Lumos watch",
		"Tab",
		"android",
		"device-1",
		"Pixel 7",
		"FPS",
		"CPU",
		"RAM",
		"JNK",
		"sampling",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q", want)
		}
	}
}

func TestKeys_ReadBasic(t *testing.T) {
	// Feed: 'q', tab, ESC [ A (up arrow), ctrl-c
	r := strings.NewReader("q\t\x1b[A\x03")
	ch := make(chan KeyEvent, 8)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { ReadKeys(ctx, r, ch); cancel() }()
	deadline := time.After(time.Second)
	var got []KeyEvent
	for {
		select {
		case ev := <-ch:
			got = append(got, ev)
			if len(got) == 4 {
				cancel()
				goto check
			}
		case <-deadline:
			t.Fatalf("timeout; got %d events: %+v", len(got), got)
		}
	}
check:
	if len(got) != 4 {
		t.Fatalf("got %d events: %+v", len(got), got)
	}
	if got[0].Key != "rune" || got[0].Rune != 'q' {
		t.Errorf("event[0] = %+v", got[0])
	}
	if got[1].Key != "tab" {
		t.Errorf("event[1] = %+v", got[1])
	}
	if got[2].Key != "up" {
		t.Errorf("event[2] = %+v", got[2])
	}
	if got[3].Key != "ctrl-c" {
		t.Errorf("event[3] = %+v", got[3])
	}
}

func TestRun_NoRaw_WritesReportPerDevice(t *testing.T) {
	dir := t.TempDir()

	specs := []DeviceSpec{
		{ID: "fake-a", Platform: metrics.Android, Label: "FakeA", AppID: "com.x"},
		{ID: "fake-b", Platform: metrics.Android, Label: "FakeB", AppID: "com.x"},
	}
	sampler := func(ctx context.Context, dev DeviceSpec) (<-chan metrics.Sample, error) {
		ch := make(chan metrics.Sample, 4)
		go func() {
			defer close(ch)
			for i := 0; i < 3; i++ {
				select {
				case <-ctx.Done():
					return
				case ch <- metrics.Sample{T: time.Now(), CPUPct: float64(i), FPS: 30 + float64(i)}:
				}
			}
		}()
		return ch, nil
	}

	// Cancel quickly so Run exits.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := Run(ctx, Config{
		Devices:       specs,
		Sampler:       sampler,
		OutDir:        dir,
		Tool:          "lumos",
		Version:       "test",
		FrameInterval: 50 * time.Millisecond,
		Output:        new(bytes.Buffer),
		NoRaw:         true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 2 {
		entries, _ := os.ReadDir(dir)
		t.Fatalf("expected 2 report files, got %d (%v)", len(files), entries)
	}
}
