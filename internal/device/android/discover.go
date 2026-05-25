package android

import (
	"context"
	"strconv"
	"strings"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

// DeviceInfo is the parsed identity of one attached Android device.
type DeviceInfo struct {
	Serial   string            // device serial / id
	State    string            // "device" | "offline" | "unauthorized" | ...
	Model    string            // marketing model name
	Product  string            // product code
	DeviceID string            // device codename
	APILevel int               // Android API level, 0 if unknown
	Extra    map[string]string // anything else in the line (transport_id, …)
}

// Platform reports android. Implements the device discovery contract.
func (DeviceInfo) Platform() metrics.Platform { return metrics.Android }

// Display returns a human-friendly one-line summary.
func (d DeviceInfo) Display() string {
	var b strings.Builder
	b.WriteString(d.Serial)
	if d.Model != "" {
		b.WriteString("  ")
		b.WriteString(d.Model)
	}
	if d.APILevel > 0 {
		b.WriteString("  API ")
		b.WriteString(strconv.Itoa(d.APILevel))
	}
	if d.State != "" && d.State != "device" {
		b.WriteString("  [")
		b.WriteString(d.State)
		b.WriteString("]")
	}
	return b.String()
}

// Devices lists all attached devices via `adb devices -l`. For each device in
// the "device" state it additionally probes the API level via `getprop`.
//
// Devices in non-ready states (offline/unauthorized) are returned but their
// APILevel/Model may be zero.
//
// Multiple adb transports can point at the same physical phone (e.g. a USB
// serial, an IP:port wireless transport, and an mDNS pairing channel all
// active simultaneously). Devices() deduplicates by hardware serial
// (`getprop ro.serialno`) and keeps the most benchmark-friendly transport:
// USB serial > IP:port > mDNS. Without this two samplers would race to
// force-stop and relaunch the app on the same device.
func (a *ADB) Devices(ctx context.Context) ([]DeviceInfo, error) {
	out, err := a.run(ctx, "devices", "-l")
	if err != nil {
		return nil, err
	}
	infos := parseDevicesL(string(out))

	for i := range infos {
		if infos[i].State != "device" {
			continue
		}
		// Best-effort enrichment; failures don't abort the listing.
		if infos[i].APILevel == 0 {
			if v, err := a.Shell(ctx, infos[i].Serial, "getprop", "ro.build.version.sdk"); err == nil {
				if n, perr := strconv.Atoi(strings.TrimSpace(v)); perr == nil {
					infos[i].APILevel = n
				}
			}
		}
		if infos[i].Model == "" {
			if v, err := a.Shell(ctx, infos[i].Serial, "getprop", "ro.product.model"); err == nil {
				infos[i].Model = strings.TrimSpace(v)
			}
		}
		// Probe hardware serial for dedup. Stored in Extra so callers that
		// look at it (e.g. logs) can see why two transports collapsed.
		if v, err := a.Shell(ctx, infos[i].Serial, "getprop", "ro.serialno"); err == nil {
			if hw := strings.TrimSpace(v); hw != "" {
				infos[i].Extra["hw_serial"] = hw
			}
		}
	}
	return dedupByHardwareSerial(infos), nil
}

// transportKind ranks adb serials by how useful they are for sampling.
// Lower number = preferred. USB-style serials (alphanumeric, no separators)
// are stable and have the lowest RTT. IP:port is fine but adds network
// latency. mDNS (`adb-<serial>-<token>._adb-tls-connect._tcp.`) shares the
// same transport as the IP form on modern Android but the long name is
// awkward in logs and reports, so we deprioritize it.
func transportKind(serial string) int {
	switch {
	case strings.Contains(serial, "_adb-tls-"), strings.Contains(serial, "._tcp"):
		return 2 // mDNS pairing channel
	case strings.Contains(serial, ":"):
		return 1 // IP:port wireless transport
	default:
		return 0 // USB serial
	}
}

// dedupByHardwareSerial collapses entries that share the same ro.serialno,
// keeping the highest-priority transport (see transportKind). Devices
// without a probed hw_serial (offline/unauthorized) are passed through
// unchanged so the caller can still report them.
func dedupByHardwareSerial(in []DeviceInfo) []DeviceInfo {
	best := map[string]int{} // hw_serial -> index in out
	var out []DeviceInfo
	for _, d := range in {
		hw := d.Extra["hw_serial"]
		if hw == "" {
			out = append(out, d)
			continue
		}
		if idx, ok := best[hw]; ok {
			if transportKind(d.Serial) < transportKind(out[idx].Serial) {
				out[idx] = d
			}
			continue
		}
		best[hw] = len(out)
		out = append(out, d)
	}
	return out
}

// parseDevicesL parses the output of `adb devices -l`. Format:
//
//	List of devices attached
//	emulator-5554  device product:sdk_gphone_x86 model:Android_SDK_built_for_x86 device:generic_x86 transport_id:1
//	R3CN30XXXX     unauthorized usb:1-1
func parseDevicesL(s string) []DeviceInfo {
	var out []DeviceInfo
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "*") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		info := DeviceInfo{
			Serial: fields[0],
			State:  fields[1],
			Extra:  map[string]string{},
		}
		for _, kv := range fields[2:] {
			k, v, ok := strings.Cut(kv, ":")
			if !ok {
				continue
			}
			switch k {
			case "model":
				// adb returns underscores for spaces.
				info.Model = strings.ReplaceAll(v, "_", " ")
			case "product":
				info.Product = v
			case "device":
				info.DeviceID = v
			default:
				info.Extra[k] = v
			}
		}
		out = append(out, info)
	}
	return out
}
