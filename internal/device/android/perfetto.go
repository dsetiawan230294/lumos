package android

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PerfettoSession is a running on-device perfetto trace capture. Stop+Pull it
// when the scenario finishes.
type PerfettoSession struct {
	adb        *ADB
	serial     string
	tag        string // --detach=<tag> handle
	devicePath string // remote .perfetto-trace path
}

// DefaultPerfettoConfig is a small, broadly-useful trace config that captures
// scheduling, frame timeline, and gfx events. ~64 MB ring buffer; pulls fine
// over USB. Customise via StartPerfettoCustom if you need more.
//
// `write_into_file: true` is required for --detach mode: perfetto streams
// the buffer to disk while running rather than holding everything in RAM.
const DefaultPerfettoConfig = `
buffers: { size_kb: 65536 fill_policy: RING_BUFFER }
write_into_file: true
file_write_period_ms: 1000
data_sources {
  config {
    name: "linux.ftrace"
    ftrace_config {
      ftrace_events: "sched/sched_switch"
      ftrace_events: "sched/sched_waking"
      ftrace_events: "power/cpu_frequency"
      ftrace_events: "power/cpu_idle"
      atrace_categories: "gfx"
      atrace_categories: "view"
      atrace_categories: "input"
      atrace_categories: "am"
    }
  }
}
data_sources { config { name: "linux.process_stats" } }
data_sources { config { name: "android.surfaceflinger.frametimeline" } }
data_sources { config { name: "android.gpu.memory" } }
`

// StartPerfetto starts a detached perfetto capture on the device using
// DefaultPerfettoConfig and returns a session that can be stopped + pulled.
//
// Requires Android 9+ (perfetto on PATH). On older devices StartPerfetto
// returns an error and the caller should fall back to running without a trace.
func (a *ADB) StartPerfetto(ctx context.Context, serial string) (*PerfettoSession, error) {
	return a.StartPerfettoCustom(ctx, serial, DefaultPerfettoConfig)
}

// StartPerfettoCustom is StartPerfetto with a caller-supplied text config.
func (a *ADB) StartPerfettoCustom(ctx context.Context, serial, txtConfig string) (*PerfettoSession, error) {
	tag := fmt.Sprintf("lumos-%d", nowMillis())
	remote := fmt.Sprintf("/data/misc/perfetto-traces/%s.perfetto-trace", tag)

	// We pipe the config via `echo … | perfetto -c - --txt`. Use a single
	// shell invocation so the config never lands in a temp file.
	//
	// perfetto's --background flag returns immediately and prints the PID.
	// We use --detach=<tag> instead, which gives us a named handle we can
	// reattach to with --attach=<tag> --stop later.
	script := fmt.Sprintf(
		`cat <<'LUMOS_PERFETTO_EOF' | perfetto -c - --txt -o %s --detach=%s
%s
LUMOS_PERFETTO_EOF`, remote, tag, txtConfig)

	out, err := a.Shell(ctx, serial, "sh", "-c", script)
	if err != nil {
		return nil, fmt.Errorf("perfetto start: %w (output: %s)", err, strings.TrimSpace(out))
	}
	low := strings.ToLower(out)
	if strings.Contains(low, "perfetto: not found") || strings.Contains(low, "inaccessible or not found") {
		return nil, fmt.Errorf("perfetto: not available on device (requires Android 9+)")
	}
	// perfetto_cmd prints diagnostics that contain the word "error" only on
	// real failures; the success path is silent under --detach.
	if strings.Contains(low, "perfetto_cmd.cc") && (strings.Contains(low, "error") || strings.Contains(low, "must be")) {
		return nil, fmt.Errorf("perfetto start: %s", strings.TrimSpace(out))
	}
	return &PerfettoSession{adb: a, serial: serial, tag: tag, devicePath: remote}, nil
}

// Stop tells perfetto to flush + close the trace file. Idempotent.
func (s *PerfettoSession) Stop(ctx context.Context) error {
	if s == nil || s.tag == "" {
		return nil
	}
	_, err := s.adb.Shell(ctx, s.serial, "perfetto", "--attach="+s.tag, "--stop")
	return err
}

// Pull copies the trace off-device into localPath. Caller should Stop first.
func (s *PerfettoSession) Pull(ctx context.Context, localPath string) error {
	if s == nil || s.devicePath == "" {
		return fmt.Errorf("perfetto: no session to pull")
	}
	out, err := s.adb.Pull(ctx, s.serial, s.devicePath, localPath)
	if err != nil {
		return fmt.Errorf("perfetto pull: %w (output: %s)", err, strings.TrimSpace(out))
	}
	// Best-effort cleanup; don't fail the whole capture if rm fails.
	_, _ = s.adb.Shell(ctx, s.serial, "rm", "-f", s.devicePath)
	return nil
}

// nowMillis returns the current Unix time in milliseconds.
// Used to generate unique perfetto session tags.
func nowMillis() int64 { return time.Now().UnixMilli() }
