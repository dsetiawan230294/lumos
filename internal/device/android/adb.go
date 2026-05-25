package android

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Execer runs an external command and returns its stdout, stderr, and error.
// It is injectable so tests can stub `adb` without needing a real device.
type Execer interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// realExec invokes the OS process. It is the default Execer.
type realExec struct{}

// Run executes name with args and returns captured stdout/stderr.
func (realExec) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return out.Bytes(), errBuf.Bytes(), err
}

// ADB is a thin wrapper around the `adb` binary.
//
// Zero value is not usable; construct with NewADB.
type ADB struct {
	bin     string        // path or name of the adb executable
	exec    Execer        // injectable
	timeout time.Duration // default per-command timeout
}

// Option configures an ADB instance.
type Option func(*ADB)

// WithBinary overrides the adb binary path (default "adb").
func WithBinary(path string) Option { return func(a *ADB) { a.bin = path } }

// WithExecer injects a custom Execer (used in tests).
func WithExecer(e Execer) Option { return func(a *ADB) { a.exec = e } }

// WithTimeout sets the default per-command timeout (default 30s).
func WithTimeout(d time.Duration) Option { return func(a *ADB) { a.timeout = d } }

// NewADB constructs an ADB wrapper with sensible defaults.
func NewADB(opts ...Option) *ADB {
	a := &ADB{bin: "adb", exec: realExec{}, timeout: 30 * time.Second}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// ErrADBNotFound is returned when the adb binary cannot be located on PATH.
var ErrADBNotFound = errors.New("adb not found on PATH; install Android Platform Tools")

// CmdError carries the exit error plus captured stderr for richer messages.
type CmdError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *CmdError) Error() string {
	msg := fmt.Sprintf("adb %s: %v", strings.Join(e.Args, " "), e.Err)
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += " (" + s + ")"
	}
	return msg
}

func (e *CmdError) Unwrap() error { return e.Err }

// run invokes adb with the given args using the configured timeout. If the
// context already has a deadline that is shorter, it is preserved.
func (a *ADB) run(ctx context.Context, args ...string) ([]byte, error) {
	if a == nil {
		return nil, errors.New("nil *ADB")
	}
	if _, deadlineSet := ctx.Deadline(); !deadlineSet && a.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	out, errBuf, err := a.exec.Run(ctx, a.bin, args...)
	if err != nil {
		// Distinguish "binary not found" from execution failures.
		var notFound *exec.Error
		if errors.As(err, &notFound) && errors.Is(notFound.Err, exec.ErrNotFound) {
			return nil, ErrADBNotFound
		}
		return out, &CmdError{Args: args, Stderr: string(errBuf), Err: err}
	}
	return out, nil
}

// Version returns the output of `adb version`.
func (a *ADB) Version(ctx context.Context) (string, error) {
	out, err := a.run(ctx, "version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Shell runs `adb -s <serial> shell <cmd...>` and returns trimmed stdout.
func (a *ADB) Shell(ctx context.Context, serial string, shellArgs ...string) (string, error) {
	args := make([]string, 0, len(shellArgs)+3)
	if serial != "" {
		args = append(args, "-s", serial)
	}
	args = append(args, "shell")
	args = append(args, shellArgs...)
	out, err := a.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Pull copies a file off the device to local path. Returns adb's stdout.
func (a *ADB) Pull(ctx context.Context, serial, remote, local string) (string, error) {
	args := make([]string, 0, 5)
	if serial != "" {
		args = append(args, "-s", serial)
	}
	args = append(args, "pull", remote, local)
	out, err := a.run(ctx, args...)
	return string(out), err
}
