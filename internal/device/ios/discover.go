package ios

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

// DeviceInfo is the parsed identity of one attached iOS device.
type DeviceInfo struct {
	UDID      string // device unique identifier (40-char hex on real devices)
	Name      string // user-visible name ("Pintu's iPhone")
	Model     string // hardware identifier (iPhone14,5)
	OSVersion string // "17.4.1"
	State     string // "device" (paired+ready) | "unavailable" | "unauthorized" | "simulator"
	Simulator bool   // true if this is a simulator runtime
	DevModeOK bool   // best-effort: developer mode enabled (iOS 16+)
	Extra     map[string]string
}

// Platform reports ios.
func (DeviceInfo) Platform() metrics.Platform { return metrics.IOS }

// Display returns a one-line summary.
func (d DeviceInfo) Display() string {
	var b strings.Builder
	b.WriteString(d.UDID)
	if d.Name != "" {
		b.WriteString("  ")
		b.WriteString(d.Name)
	}
	if d.OSVersion != "" {
		b.WriteString("  iOS ")
		b.WriteString(d.OSVersion)
	}
	if d.Simulator {
		b.WriteString("  [simulator]")
	} else if d.State != "" && d.State != "device" {
		b.WriteString("  [")
		b.WriteString(d.State)
		b.WriteString("]")
	}
	return b.String()
}

// Devices lists attached iOS devices. Tries `idb list-targets --json` first,
// falling back to `xcrun xctrace list devices` if idb is not installed.
//
// Returns ErrUnsupportedHost on non-darwin hosts.
func (t *Tools) Devices(ctx context.Context) ([]DeviceInfo, error) {
	if !SupportedHost() {
		return nil, ErrUnsupportedHost
	}

	// Primary: idb (richer JSON, includes simulators).
	out, err := t.IDB(ctx, "list-targets", "--json")
	if err == nil {
		return parseIDBList(out)
	}
	if !errors.Is(err, ErrIDBNotFound) {
		// idb is installed but failed for some other reason — fall through
		// to xctrace, but remember the error for the caller in case both
		// fail.
		idbErr := err
		if devs, xerr := t.xctraceList(ctx); xerr == nil {
			return devs, nil
		}
		return nil, idbErr
	}
	return t.xctraceList(ctx)
}

// xctraceList parses `xcrun xctrace list devices`. The output is two
// sections: "== Devices ==" (real devices first, then host) and
// "== Simulators ==".
func (t *Tools) xctraceList(ctx context.Context) ([]DeviceInfo, error) {
	out, err := t.Xctrace(ctx, "list", "devices")
	if err != nil {
		return nil, err
	}
	return parseXctraceList(out), nil
}

// idbTarget mirrors the relevant fields of `idb list-targets --json` lines.
// Each line is a separate JSON object.
type idbTarget struct {
	UDID          string `json:"udid"`
	Name          string `json:"name"`
	Model         string `json:"model"`
	OSVersion     string `json:"os_version"`
	State         string `json:"state"`
	TargetType    string `json:"target_type"`
	Architecture  string `json:"architecture"`
	CompanionInfo any    `json:"companion_info,omitempty"`
}

func parseIDBList(s string) ([]DeviceInfo, error) {
	var out []DeviceInfo
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var t idbTarget
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			continue
		}
		di := DeviceInfo{
			UDID:      t.UDID,
			Name:      t.Name,
			Model:     t.Model,
			OSVersion: t.OSVersion,
			State:     normaliseIDBState(t.State),
			Simulator: strings.EqualFold(t.TargetType, "simulator"),
			Extra:     map[string]string{"architecture": t.Architecture},
		}
		out = append(out, di)
	}
	return out, nil
}

func normaliseIDBState(s string) string {
	switch strings.ToLower(s) {
	case "booted", "active", "ready", "online":
		return "device"
	case "shutdown", "offline":
		return "offline"
	case "unavailable", "creating":
		return "unavailable"
	default:
		if s == "" {
			return "device"
		}
		return strings.ToLower(s)
	}
}

// xctraceDeviceLine matches lines like:
//
//	Pintu's iPhone (17.4.1) (00008110-001A4D281A78801E)
//
// for both real devices and simulators.
var xctraceDeviceLine = regexp.MustCompile(`^(.+?) \(([0-9][0-9A-Za-z.]+)\) \(([0-9A-Fa-f-]{25,})\)\s*$`)

func parseXctraceList(s string) []DeviceInfo {
	var out []DeviceInfo
	section := "" // "devices" | "simulators"
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		switch {
		case strings.HasPrefix(low, "== devices"):
			section = "devices"
			continue
		case strings.HasPrefix(low, "== simulators"):
			section = "simulators"
			continue
		case strings.HasPrefix(line, "=="):
			section = ""
			continue
		}
		if section == "" {
			continue
		}
		m := xctraceDeviceLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		di := DeviceInfo{
			Name:      strings.TrimSpace(m[1]),
			OSVersion: strings.TrimSpace(m[2]),
			UDID:      strings.TrimSpace(m[3]),
			State:     "device",
			Simulator: section == "simulators",
		}
		out = append(out, di)
	}
	return out
}
