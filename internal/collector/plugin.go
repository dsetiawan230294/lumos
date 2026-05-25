// Package collector defines the plugin protocol for user-provided collectors.
//
// A plugin is any executable that, when launched by lumos with the device ID
// as its first argument (or via $LUMOS_DEVICE_ID env var), prints
// newline-delimited JSON objects to stdout, one per sample. Each object has:
//
//	{"t": "2026-05-25T10:00:00Z", "metrics": {"gpu_temp_c": 47.5, "net_rx_kbps": 120}}
//
// or the shorter form (lumos stamps the timestamp on receipt):
//
//	{"metrics": {"gpu_temp_c": 47.5}}
//
// Plugins are subprocess-isolated: a crashing plugin never crashes lumos.
// Stderr is captured into the run's logs. The plugin is sent SIGTERM (or
// killed on Windows) when the context is cancelled.
//
// This is deliberately the same shape as the Python automation bridge — any
// language with stdout and JSON can implement it.
package collector

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

// Plugin describes how to launch a single collector plugin.
type Plugin struct {
	// Name is used in error messages and as a prefix for stderr lines.
	Name string

	// Command is the executable; Args are passed verbatim. The literal
	// token "{device_id}" inside any Args entry is substituted with the
	// runtime device ID.
	Command string
	Args    []string

	// Env is appended to the inherited environment. LUMOS_DEVICE_ID is
	// always set automatically.
	Env []string
}

// pluginLine is one JSON record from a plugin's stdout.
type pluginLine struct {
	T       string             `json:"t,omitempty"`
	Metrics map[string]float64 `json:"metrics"`
}

// Run launches the plugin and returns a channel that emits one Sample per
// stdout line. The channel is closed when the plugin exits or ctx is
// cancelled. Stderr lines are forwarded to stderrSink (may be nil).
//
// Run never returns an error for parse failures on individual lines — those
// lines are silently dropped so a noisy plugin can't break the run.
func (p Plugin) Run(ctx context.Context, deviceID string, stderrSink io.Writer) (<-chan metrics.Sample, error) {
	if p.Command == "" {
		return nil, fmt.Errorf("plugin %q: Command is required", p.Name)
	}
	args := make([]string, len(p.Args))
	for i, a := range p.Args {
		args[i] = strings.ReplaceAll(a, "{device_id}", deviceID)
	}

	cmd := exec.CommandContext(ctx, p.Command, args...)
	cmd.Env = append(cmd.Environ(), "LUMOS_DEVICE_ID="+deviceID)
	cmd.Env = append(cmd.Env, p.Env...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("plugin %q: start: %w", p.Name, err)
	}

	if stderrSink != nil {
		go forwardStderr(stderr, stderrSink, p.Name)
	} else {
		go io.Copy(io.Discard, stderr)
	}

	out := make(chan metrics.Sample, 8)
	go func() {
		defer close(out)
		defer cmd.Wait()
		sc := bufio.NewScanner(stdout)
		// Plugins may emit large lines (e.g. wide metric maps); raise the
		// default 64 KiB cap.
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			s, ok := parsePluginLine(line)
			if !ok {
				continue
			}
			select {
			case out <- s:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func parsePluginLine(line string) (metrics.Sample, bool) {
	var pl pluginLine
	if err := json.Unmarshal([]byte(line), &pl); err != nil {
		return metrics.Sample{}, false
	}
	if len(pl.Metrics) == 0 {
		return metrics.Sample{}, false
	}
	s := metrics.Sample{T: parseTime(pl.T), Extra: map[string]float64{}}
	for k, v := range pl.Metrics {
		// Promote a handful of known keys onto typed fields so they flow
		// through summaries naturally.
		switch k {
		case "fps":
			s.FPS = v
		case "frame_ms":
			s.FrameMS = v
		case "cpu_pct":
			s.CPUPct = v
		case "ram_mb":
			s.RAMMB = v
		case "jank_pct":
			s.JankPct = v
		case "gpu_pct":
			s.GPUPct = v
		case "battery_pct":
			s.BatteryPct = v
		case "battery_temp_c":
			s.BatteryTempC = v
		default:
			s.Extra[k] = v
		}
	}
	if len(s.Extra) == 0 {
		s.Extra = nil
	}
	return s, true
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Now()
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Now()
}

func forwardStderr(r io.Reader, w io.Writer, name string) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fmt.Fprintf(w, "[plugin %s] %s\n", name, sc.Text())
	}
}
