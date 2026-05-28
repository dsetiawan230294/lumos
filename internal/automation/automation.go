// Package automation hosts the Python scenario subprocess bridge.
//
// Lumos spawns a small Python harness as a child process and communicates
// with it via line-delimited JSON-RPC over stdio. The harness invokes
// setup/run/teardown on the user's scenario module and forwards markers,
// log lines, and file attachments back to Lumos as JSON notifications.
package automation

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

// ScenarioOpts configures one scenario invocation.
type ScenarioOpts struct {
	PythonBin  string // default "python3"
	HarnessPy  string // path to harness script (Lumos-provided)
	ScriptPath string // user scenario path
	DeviceID   string
	Platform   string
	AppID      string
	Iteration  int
	Env        map[string]string
	Timeout    time.Duration
	Stderr     io.Writer // optional sink for the subprocess's stderr
}

// Result is the outcome of one scenario run.
type Result struct {
	Markers []metrics.Marker
	Logs    []LogLine
	Files   []string
	Err     error
}

// LogLine is a structured log emitted by the scenario.
type LogLine struct {
	T     time.Time
	Level string
	Msg   string
}

// rpcMessage is the on-the-wire JSON-RPC envelope. v1 only uses notifications
// (one-way scenario → host), so id is absent and result/error are unused.
type rpcMessage struct {
	Jsonrpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// Run launches the scenario subprocess and consumes its notifications until
// it exits. The returned Result is partial on error.
func Run(ctx context.Context, o ScenarioOpts) Result {
	if o.PythonBin == "" {
		o.PythonBin = "python3"
	}
	if o.HarnessPy == "" {
		return Result{Err: errors.New("automation: HarnessPy required")}
	}
	if o.ScriptPath == "" {
		return Result{Err: errors.New("automation: ScriptPath required")}
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, o.PythonBin, o.HarnessPy, o.ScriptPath)
	cmd.Env = buildEnv(o)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{Err: fmt.Errorf("stdout pipe: %w", err)}
	}
	if o.Stderr != nil {
		cmd.Stderr = o.Stderr
	}

	if err := cmd.Start(); err != nil {
		return Result{Err: fmt.Errorf("start scenario: %w", err)}
	}

	res := Result{}
	var mu sync.Mutex
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		var params map[string]any
		if len(msg.Params) > 0 {
			_ = json.Unmarshal(msg.Params, &params)
		}
		mu.Lock()
		handle(&res, msg.Method, params)
		mu.Unlock()
	}

	waitErr := cmd.Wait()
	if waitErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			res.Err = fmt.Errorf("scenario cancelled/timeout: %w", ctxErr)
		} else {
			res.Err = fmt.Errorf("scenario exited: %w", waitErr)
		}
	}
	return res
}

func handle(r *Result, method string, p map[string]any) {
	now := time.Now()
	label, _ := p["label"].(string)
	switch method {
	case "lumos.markStart":
		r.Markers = append(r.Markers, metrics.Marker{T: now, Label: label, Kind: "start"})
	case "lumos.markEnd":
		r.Markers = append(r.Markers, metrics.Marker{T: now, Label: label, Kind: "end"})
	case "lumos.mark":
		r.Markers = append(r.Markers, metrics.Marker{T: now, Label: label, Kind: "point"})
	case "lumos.iterStart":
		// In-process iteration boundary. Label encodes the iteration
		// number (post-warmup, 1-based) and a "warmup:1" suffix when
		// the iteration is a warmup pass. The runner uses these to
		// slice samples into per-iteration reports.
		r.Markers = append(r.Markers, metrics.Marker{T: now, Label: label, Kind: "iter_start"})
	case "lumos.iterEnd":
		r.Markers = append(r.Markers, metrics.Marker{T: now, Label: label, Kind: "iter_end"})
	case "lumos.log":
		lvl, _ := p["level"].(string)
		msg, _ := p["msg"].(string)
		r.Logs = append(r.Logs, LogLine{T: now, Level: lvl, Msg: msg})
	case "lumos.attach":
		if path, ok := p["path"].(string); ok && path != "" {
			r.Files = append(r.Files, path)
		}
	}
}

func buildEnv(o ScenarioOpts) []string {
	// Inherit parent environment so the scenario subprocess can find tools
	// like `adb` on the user's PATH. Override with Lumos-specific vars last.
	env := append([]string(nil), os.Environ()...)
	env = append(env,
		"LUMOS_DEVICE_ID="+o.DeviceID,
		"LUMOS_PLATFORM="+o.Platform,
		"LUMOS_APP_ID="+o.AppID,
		fmt.Sprintf("LUMOS_ITERATION=%d", o.Iteration),
	)
	for k, v := range o.Env {
		env = append(env, k+"="+v)
	}
	if !hasEnv(env, "PATH") {
		env = append(env, "PATH=/usr/local/bin:/usr/bin:/bin")
	}
	return env
}

func hasEnv(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
