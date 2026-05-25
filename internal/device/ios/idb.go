// Package ios wraps `idb`, `xcrun`, and `xctrace` for iOS device control
// and metric collection.
//
// Design notes:
//   - Mirrors the Android package layout. The Execer interface lets tests
//     stub every external binary so we can build and verify the whole
//     pipeline without a real iOS device.
//   - Real metric collection on iOS goes through `xctrace`: we record a
//     trace bundle for the duration of an iteration and parse the XML
//     export afterwards into metrics.Sample values. There is no per-frame
//     real-time stream like Android's dumpsys; the runner sees a closed
//     channel that has emitted post-hoc samples once the recording stops.
//   - iOS tooling only ships on macOS. The package compiles on all hosts
//     (so tests run anywhere) but real binaries will be `ErrUnsupportedHost`
//     on non-darwin via runtime.GOOS check inside Sample/Devices/etc.
package ios

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Execer runs an external command and returns its stdout, stderr, and error.
// Injectable so tests can simulate `idb` / `xcrun` / `xctrace` without a
// real device.
type Execer interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

type realExec struct{}

func (realExec) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return out.Bytes(), errBuf.Bytes(), err
}

// Tools is a thin wrapper around the iOS CLI toolchain.
type Tools struct {
	idbBin     string // default "idb"
	xcrunBin   string // default "xcrun"
	xctraceBin string // default "xctrace" (resolved via xcrun on real hosts)
	exec       Execer
	timeout    time.Duration
}

// Option configures a Tools instance.
type Option func(*Tools)

// WithIDB overrides the idb binary path.
func WithIDB(path string) Option { return func(t *Tools) { t.idbBin = path } }

// WithXcrun overrides the xcrun binary path.
func WithXcrun(path string) Option { return func(t *Tools) { t.xcrunBin = path } }

// WithXctrace overrides the xctrace binary path (default: invoke via xcrun).
func WithXctrace(path string) Option { return func(t *Tools) { t.xctraceBin = path } }

// WithExecer injects a custom Execer (used in tests).
func WithExecer(e Execer) Option { return func(t *Tools) { t.exec = e } }

// WithTimeout sets the default per-command timeout (default 60s — iOS tools
// are slower than adb).
func WithTimeout(d time.Duration) Option { return func(t *Tools) { t.timeout = d } }

// New constructs a Tools wrapper with sensible defaults.
func New(opts ...Option) *Tools {
	t := &Tools{
		idbBin:   "idb",
		xcrunBin: "xcrun",
		exec:     realExec{},
		timeout:  60 * time.Second,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// ErrIDBNotFound is returned when the idb binary cannot be located.
var ErrIDBNotFound = errors.New("idb not found on PATH; install via `brew tap facebook/fb && brew install idb-companion` and `pipx install fb-idb`")

// ErrXcrunNotFound is returned when xcrun cannot be located.
var ErrXcrunNotFound = errors.New("xcrun not found on PATH; install Xcode Command Line Tools (`xcode-select --install`)")

// ErrUnsupportedHost is returned when an iOS operation is attempted on a
// host OS that cannot drive iOS devices (anything other than macOS).
var ErrUnsupportedHost = errors.New("iOS tooling requires macOS; this host cannot drive iOS devices")

// CmdError carries the binary name, args, exit error and captured stderr.
type CmdError struct {
	Bin    string
	Args   []string
	Stderr string
	Err    error
}

func (e *CmdError) Error() string {
	msg := fmt.Sprintf("%s %s: %v", e.Bin, strings.Join(e.Args, " "), e.Err)
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += " (" + s + ")"
	}
	return msg
}

func (e *CmdError) Unwrap() error { return e.Err }

// SupportedHost reports whether the current OS can run real iOS tooling.
func SupportedHost() bool { return runtime.GOOS == "darwin" }

// run executes the given binary with args, applying the default timeout if
// the context does not already have one.
func (t *Tools) run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	if t == nil {
		return nil, errors.New("nil *Tools")
	}
	if _, set := ctx.Deadline(); !set && t.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.timeout)
		defer cancel()
	}
	out, errBuf, err := t.exec.Run(ctx, bin, args...)
	if err != nil {
		var notFound *exec.Error
		if errors.As(err, &notFound) && errors.Is(notFound.Err, exec.ErrNotFound) {
			switch bin {
			case t.idbBin:
				return nil, ErrIDBNotFound
			case t.xcrunBin:
				return nil, ErrXcrunNotFound
			}
		}
		return out, &CmdError{Bin: bin, Args: args, Stderr: string(errBuf), Err: err}
	}
	return out, nil
}

// IDB runs `idb <args...>` and returns trimmed stdout.
func (t *Tools) IDB(ctx context.Context, args ...string) (string, error) {
	out, err := t.run(ctx, t.idbBin, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Xcrun runs `xcrun <args...>` and returns trimmed stdout.
func (t *Tools) Xcrun(ctx context.Context, args ...string) (string, error) {
	out, err := t.run(ctx, t.xcrunBin, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Xctrace invokes `xctrace` (via the configured binary, defaulting to
// `xcrun xctrace`) with args.
func (t *Tools) Xctrace(ctx context.Context, args ...string) (string, error) {
	if t.xctraceBin != "" {
		out, err := t.run(ctx, t.xctraceBin, args...)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	full := append([]string{"xctrace"}, args...)
	return t.Xcrun(ctx, full...)
}
