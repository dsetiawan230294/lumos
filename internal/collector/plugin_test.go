package collector

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

func TestPlugin_StdoutSamples(t *testing.T) {
	p := Plugin{
		Name:    "echo3",
		Command: "/bin/sh",
		Args: []string{"-c", `
echo '{"metrics":{"gpu_temp_c":47.5,"net_rx_kbps":120}}'
echo '# comment ignored'
echo ''
echo '{"t":"2026-05-25T10:00:00Z","metrics":{"gpu_temp_c":48.0}}'
echo 'not-json'
echo '{"metrics":{"fps":60,"jank_pct":1.5,"custom":3.14}}'
`},
	}

	var stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := p.Run(ctx, "DEVICE-1", &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got []metrics.Sample
	for s := range ch {
		got = append(got, s)
	}
	if len(got) != 3 {
		t.Fatalf("samples=%d, want 3: %+v", len(got), got)
	}
	if got[0].Extra["gpu_temp_c"] != 47.5 || got[0].Extra["net_rx_kbps"] != 120 {
		t.Errorf("sample[0].Extra = %+v", got[0].Extra)
	}
	wantT := time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC)
	if !got[1].T.Equal(wantT) {
		t.Errorf("sample[1].T = %v, want %v", got[1].T, wantT)
	}
	if got[2].FPS != 60 || got[2].JankPct != 1.5 {
		t.Errorf("sample[2] FPS/jank = %v/%v", got[2].FPS, got[2].JankPct)
	}
	if got[2].Extra["custom"] != 3.14 {
		t.Errorf("sample[2].Extra[custom] = %v", got[2].Extra["custom"])
	}
	if _, ok := got[2].Extra["fps"]; ok {
		t.Errorf("known key 'fps' should not be in Extra: %+v", got[2].Extra)
	}
}

func TestPlugin_StartError(t *testing.T) {
	p := Plugin{Name: "missing", Command: "/no/such/binary/lumos-test-zzz"}
	if _, err := p.Run(context.Background(), "DEV", nil); err == nil {
		t.Fatalf("expected start error for missing binary")
	}
}

func TestPlugin_ContextCancelStopsPlugin(t *testing.T) {
	p := Plugin{
		Name:    "looper",
		Command: "/bin/sh",
		Args:    []string{"-c", `while true; do echo '{"metrics":{"x":1}}'; sleep 0.02; done`},
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Run(ctx, "DEV", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for sample %d", i)
		}
	}
	cancel()
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("channel did not close after cancel")
	}
}

func TestParsePluginLine_DropsGarbage(t *testing.T) {
	for _, in := range []string{
		"",
		"not json",
		`{}`,
		`{"metrics":{}}`,
		`{"t":"2026","metrics":null}`,
	} {
		if _, ok := parsePluginLine(in); ok {
			t.Errorf("expected drop for %q", in)
		}
	}
}

func TestPlugin_StderrForwarded(t *testing.T) {
	p := Plugin{
		Name:    "noisy",
		Command: "/bin/sh",
		Args:    []string{"-c", `echo hello-stderr 1>&2; echo '{"metrics":{"x":1}}'`},
	}
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := p.Run(ctx, "DEV", &buf)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for range ch {
	}
	if !strings.Contains(buf.String(), "[plugin noisy] hello-stderr") {
		t.Errorf("stderr not forwarded: %q", buf.String())
	}
}

func TestPlugin_DeviceIDEnv(t *testing.T) {
	p := Plugin{
		Name:    "envcheck",
		Command: "/bin/sh",
		Args:    []string{"-c", `printf '{"metrics":{"got":1.0}}\n'; echo "$LUMOS_DEVICE_ID" 1>&2`},
	}
	var stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := p.Run(ctx, "DEV-42", &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for range ch {
	}
	if !strings.Contains(stderr.String(), "DEV-42") {
		t.Errorf("LUMOS_DEVICE_ID not propagated: %q", stderr.String())
	}
}
