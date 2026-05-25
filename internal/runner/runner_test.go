package runner

import (
	"context"
	"testing"
	"time"

	"github.com/dsetiawan230294/lumos/internal/automation"
	"github.com/dsetiawan230294/lumos/internal/metrics"
)

// fakeSampler emits the given samples then closes the channel.
func fakeSampler(samples []metrics.Sample) Sampler {
	return SamplerFunc(func(ctx context.Context) (<-chan metrics.Sample, error) {
		ch := make(chan metrics.Sample, len(samples))
		// Pre-fill so emission never races with cancellation.
		for _, s := range samples {
			ch <- s
		}
		go func() {
			<-ctx.Done()
			close(ch)
		}()
		return ch, nil
	})
}

func TestRun_HappyPath(t *testing.T) {
	now := time.Now()
	samples := []metrics.Sample{
		{T: now, FPS: 60, CPUPct: 10, RAMMB: 200},
		{T: now.Add(time.Second), FPS: 58, CPUPct: 12, RAMMB: 210},
	}

	dir := t.TempDir()
	job := Job{
		Scenario:  "home_scroll",
		Iteration: 1,
		DeviceID:  "FAKE-1",
		Platform:  metrics.Android,
		AppID:     "com.example.app",
		Sampler:   fakeSampler(samples),
		Automation: func(ctx context.Context, o automation.ScenarioOpts) automation.Result {
			// Pretend the scenario marks one segment then returns.
			return automation.Result{
				Markers: []metrics.Marker{
					{T: time.Now(), Label: "scroll", Kind: "start"},
					{T: time.Now(), Label: "scroll", Kind: "end"},
				},
			}
		},
		OutDir:  dir,
		Tool:    "lumos",
		Version: "test",
	}

	res, err := Run(context.Background(), job)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Run.Samples) != 2 {
		t.Errorf("samples = %d, want 2", len(res.Run.Samples))
	}
	if len(res.Run.Markers) != 2 {
		t.Errorf("markers = %d, want 2", len(res.Run.Markers))
	}
	if res.ReportPath == "" {
		t.Errorf("expected report path")
	}
}

func TestRun_RequiresSampler(t *testing.T) {
	_, err := Run(context.Background(), Job{})
	if err == nil {
		t.Fatal("expected error when Sampler is nil")
	}
}
