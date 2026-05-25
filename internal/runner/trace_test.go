package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsetiawan230294/lumos/internal/automation"
	"github.com/dsetiawan230294/lumos/internal/metrics"
)

type fakeTrace struct {
	kind        string
	startErr    error
	pullErr     error
	startedAt   time.Time
	stoppedAt   time.Time
	pulledTo    string
	startCalled bool
	stopCalled  bool
	payload     []byte
}

func (f *fakeTrace) Kind() string { return f.kind }
func (f *fakeTrace) Start(ctx context.Context) error {
	f.startCalled = true
	f.startedAt = time.Now()
	return f.startErr
}
func (f *fakeTrace) StopAndPull(ctx context.Context, localPath string) error {
	f.stopCalled = true
	f.stoppedAt = time.Now()
	f.pulledTo = localPath
	if f.pullErr != nil {
		return f.pullErr
	}
	if f.payload == nil {
		f.payload = []byte("FAKE_PERFETTO_TRACE")
	}
	return os.WriteFile(localPath, f.payload, 0o644)
}

func TestRun_TraceCapture_Success(t *testing.T) {
	dir := t.TempDir()
	tr := &fakeTrace{kind: "perfetto"}
	job := Job{
		Scenario:  "home_scroll",
		Iteration: 2,
		DeviceID:  "DEV-1",
		Platform:  metrics.Android,
		AppID:     "com.example",
		Sampler:   fakeSampler(nil),
		Automation: func(ctx context.Context, o automation.ScenarioOpts) automation.Result {
			return automation.Result{}
		},
		OutDir:  dir,
		Tool:    "lumos",
		Version: "test",
		Trace:   tr,
	}

	res, err := Run(context.Background(), job)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !tr.startCalled || !tr.stopCalled {
		t.Fatalf("trace lifecycle incomplete: start=%v stop=%v", tr.startCalled, tr.stopCalled)
	}
	if !tr.startedAt.Before(tr.stoppedAt) {
		t.Fatalf("trace must start before it stops: %v vs %v", tr.startedAt, tr.stoppedAt)
	}
	if len(res.Run.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(res.Run.Artifacts))
	}
	art := res.Run.Artifacts[0]
	if art.Kind != "perfetto" {
		t.Errorf("artifact kind = %q, want perfetto", art.Kind)
	}
	if art.Size <= 0 {
		t.Errorf("artifact size = %d, want > 0", art.Size)
	}
	// File should physically exist in OutDir.
	if _, err := os.Stat(filepath.Join(dir, art.Path)); err != nil {
		t.Errorf("artifact file missing: %v", err)
	}
}

func TestRun_TraceCapture_StartFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	tr := &fakeTrace{kind: "perfetto", startErr: errors.New("perfetto missing")}
	job := Job{
		Scenario:   "home_scroll",
		Iteration:  1,
		DeviceID:   "DEV-1",
		Platform:   metrics.Android,
		Sampler:    fakeSampler(nil),
		Automation: func(ctx context.Context, o automation.ScenarioOpts) automation.Result { return automation.Result{} },
		OutDir:     dir,
		Tool:       "lumos",
		Version:    "test",
		Trace:      tr,
	}
	res, err := Run(context.Background(), job)
	if err != nil {
		t.Fatalf("Run should not fail when trace start fails: %v", err)
	}
	if tr.stopCalled {
		t.Errorf("Stop must not be called when Start failed")
	}
	if len(res.Run.Artifacts) != 0 {
		t.Errorf("expected no artifacts, got %d", len(res.Run.Artifacts))
	}
}
