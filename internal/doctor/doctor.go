// Package doctor implements `lumos doctor`: a single-shot environment
// health-check that diagnoses common setup issues (adb missing, no
// devices, missing Python, etc.) and prints actionable fixes.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/dsetiawan230294/lumos/internal/device/android"
	"github.com/dsetiawan230294/lumos/internal/device/ios"
)

// Status is one check's outcome.
type Status int

const (
	OK   Status = iota // green
	Warn               // amber — works but missing something optional
	Fail               // red — required dependency missing
	Skip               // not applicable on this host
)

func (s Status) String() string {
	switch s {
	case OK:
		return "OK"
	case Warn:
		return "WARN"
	case Fail:
		return "FAIL"
	case Skip:
		return "SKIP"
	}
	return "?"
}

// Check is the result of one diagnostic step.
type Check struct {
	Name   string // short identifier, e.g. "adb"
	Status Status
	Detail string // observed value or error
	Hint   string // actionable fix (empty for OK/Skip)
}

// Report bundles all checks plus an aggregate summary.
type Report struct {
	Checks    []Check
	OK        int
	Warn      int
	Fail      int
	Skip      int
	GoVersion string
	GOOS      string
	GOARCH    string
}

// Verdict returns the worst non-skip status across all checks.
func (r Report) Verdict() Status {
	switch {
	case r.Fail > 0:
		return Fail
	case r.Warn > 0:
		return Warn
	default:
		return OK
	}
}

// Run performs every diagnostic and returns a Report. It never returns an
// error — failures are recorded as Check entries with status Fail/Warn so
// the caller can pretty-print them all at once.
func Run(ctx context.Context) Report {
	r := Report{
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
	}
	r.add(checkBinary(ctx, "python3", "--version", "install Python 3.9+ from python.org or your package manager"))
	r.add(checkAndroid(ctx))
	r.add(checkAndroidDevices(ctx))
	r.add(checkAndroidPerfetto(ctx))
	r.add(checkIOS(ctx))
	r.add(checkIOSDevices(ctx))
	return r
}

func (r *Report) add(c Check) {
	r.Checks = append(r.Checks, c)
	switch c.Status {
	case OK:
		r.OK++
	case Warn:
		r.Warn++
	case Fail:
		r.Fail++
	case Skip:
		r.Skip++
	}
}

// Render prints the report to w in a compact, scannable layout.
func Render(w io.Writer, r Report) {
	fmt.Fprintf(w, "lumos doctor · %s/%s · %s\n\n", r.GOOS, r.GOARCH, r.GoVersion)
	for _, c := range r.Checks {
		fmt.Fprintf(w, "  [%-4s] %-22s %s\n", c.Status, c.Name, c.Detail)
		if c.Hint != "" && (c.Status == Warn || c.Status == Fail) {
			fmt.Fprintf(w, "         %s↳ %s\n", "", c.Hint)
		}
	}
	fmt.Fprintf(w, "\n%d ok · %d warn · %d fail · %d skip\n", r.OK, r.Warn, r.Fail, r.Skip)
	switch r.Verdict() {
	case OK:
		fmt.Fprintln(w, "ready to roll.")
	case Warn:
		fmt.Fprintln(w, "ready, but some optional features may be unavailable.")
	case Fail:
		fmt.Fprintln(w, "blocked: fix the FAIL items above before running lumos.")
	}
}

// checkBinary verifies that an executable is on PATH and produces a version.
// If versionArg is empty, just locates the binary.
func checkBinary(ctx context.Context, bin, versionArg, hint string) Check {
	path, err := exec.LookPath(bin)
	if err != nil {
		return Check{Name: bin, Status: Fail, Detail: "not found on PATH", Hint: hint}
	}
	if versionArg == "" {
		return Check{Name: bin, Status: OK, Detail: path}
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, versionArg).CombinedOutput()
	if err != nil {
		return Check{Name: bin, Status: Warn, Detail: fmt.Sprintf("%s (version probe failed: %v)", path, err), Hint: hint}
	}
	return Check{Name: bin, Status: OK, Detail: firstLine(string(out))}
}

func checkAndroid(ctx context.Context) Check {
	adb := android.NewADB()
	v, err := adb.Version(ctx)
	if errors.Is(err, android.ErrADBNotFound) {
		return Check{
			Name: "adb", Status: Fail,
			Detail: "not found on PATH",
			Hint:   "install Android Platform Tools (https://developer.android.com/tools/releases/platform-tools) — Android sampling won't work without it",
		}
	}
	if err != nil {
		return Check{Name: "adb", Status: Warn, Detail: fmt.Sprintf("present but errored: %v", err)}
	}
	return Check{Name: "adb", Status: OK, Detail: firstLine(v)}
}

func checkAndroidDevices(ctx context.Context) Check {
	adb := android.NewADB()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	devs, err := adb.Devices(cctx)
	if errors.Is(err, android.ErrADBNotFound) {
		return Check{Name: "android devices", Status: Skip, Detail: "adb missing"}
	}
	if err != nil {
		return Check{
			Name: "android devices", Status: Warn,
			Detail: fmt.Sprintf("adb errored: %v", err),
			Hint:   "try `adb kill-server && adb start-server`",
		}
	}
	var ready, unauthorized, offline int
	var serials []string
	for _, d := range devs {
		switch d.State {
		case "device":
			ready++
			serials = append(serials, d.Serial)
		case "unauthorized":
			unauthorized++
		case "offline":
			offline++
		}
	}
	switch {
	case ready == 0 && unauthorized > 0:
		return Check{
			Name: "android devices", Status: Warn,
			Detail: fmt.Sprintf("%d unauthorized", unauthorized),
			Hint:   "accept the USB-debugging prompt on the device, or revoke + reconnect via Developer Options → USB debugging",
		}
	case ready == 0 && offline > 0:
		return Check{
			Name: "android devices", Status: Warn,
			Detail: fmt.Sprintf("%d offline", offline),
			Hint:   "replug the cable, switch USB mode to File Transfer, or run `adb reconnect offline`",
		}
	case ready == 0:
		return Check{
			Name: "android devices", Status: Warn,
			Detail: "none connected",
			Hint:   "plug in a device with USB debugging enabled (Developer Options → USB debugging)",
		}
	}
	return Check{Name: "android devices", Status: OK, Detail: fmt.Sprintf("%d ready (%s)", ready, strings.Join(serials, ", "))}
}

// checkAndroidPerfetto: best-effort check that `perfetto` is on each ready
// device (Android 9+). Failure is a Warn, not Fail, because tracing is
// opt-in via --trace.
func checkAndroidPerfetto(ctx context.Context) Check {
	adb := android.NewADB()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	devs, err := adb.Devices(cctx)
	if err != nil {
		return Check{Name: "perfetto on device", Status: Skip, Detail: "no devices to probe"}
	}
	var ready []string
	for _, d := range devs {
		if d.State == "device" {
			ready = append(ready, d.Serial)
		}
	}
	if len(ready) == 0 {
		return Check{Name: "perfetto on device", Status: Skip, Detail: "no ready devices"}
	}
	var missing []string
	for _, s := range ready {
		out, err := adb.Shell(cctx, s, "which", "perfetto")
		if err != nil || !strings.Contains(out, "/perfetto") {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return Check{
			Name: "perfetto on device", Status: Warn,
			Detail: fmt.Sprintf("missing on %d/%d device(s): %s", len(missing), len(ready), strings.Join(missing, ", ")),
			Hint:   "Android 9+ ships `perfetto` in /system/bin; older devices can't use `lumos run --trace`",
		}
	}
	return Check{Name: "perfetto on device", Status: OK, Detail: fmt.Sprintf("present on %d device(s)", len(ready))}
}

func checkIOS(ctx context.Context) Check {
	if !ios.SupportedHost() {
		return Check{
			Name: "ios tooling", Status: Skip,
			Detail: "iOS sampling requires macOS",
		}
	}
	return checkBinary(ctx, "xcrun", "--version", "install Xcode Command Line Tools: `xcode-select --install`")
}

func checkIOSDevices(ctx context.Context) Check {
	if !ios.SupportedHost() {
		return Check{Name: "ios devices", Status: Skip, Detail: "non-darwin host"}
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tools := ios.New()
	devs, err := tools.Devices(cctx)
	if errors.Is(err, ios.ErrXcrunNotFound) {
		return Check{Name: "ios devices", Status: Skip, Detail: "xcrun missing"}
	}
	if errors.Is(err, ios.ErrIDBNotFound) {
		return Check{
			Name: "ios devices", Status: Warn,
			Detail: "idb missing (xcrun-only mode)",
			Hint:   "for richer iOS automation install idb: `brew tap facebook/fb && brew install idb-companion && pipx install fb-idb`",
		}
	}
	if err != nil {
		return Check{Name: "ios devices", Status: Warn, Detail: fmt.Sprintf("listing errored: %v", err)}
	}
	var physical, simulator int
	for _, d := range devs {
		if d.Simulator {
			simulator++
		} else if d.State == "device" {
			physical++
		}
	}
	switch {
	case physical == 0 && simulator == 0:
		return Check{
			Name: "ios devices", Status: Warn,
			Detail: "none connected",
			Hint:   "plug in an iPhone/iPad, trust the host, or run `xcrun simctl boot <udid>` for a simulator",
		}
	case physical == 0:
		return Check{
			Name: "ios devices", Status: Warn,
			Detail: fmt.Sprintf("%d simulator(s) only; xctrace sampling needs a physical device", simulator),
		}
	}
	return Check{Name: "ios devices", Status: OK, Detail: fmt.Sprintf("%d physical, %d simulator(s)", physical, simulator)}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
