package ios

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// IsInstalled returns true if the bundle is installed on the device.
func (t *Tools) IsInstalled(ctx context.Context, udid, bundleID string) (bool, error) {
	if !SupportedHost() {
		return false, ErrUnsupportedHost
	}
	out, err := t.IDB(ctx, "list-apps", "--udid", udid)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, bundleID) {
			return true, nil
		}
	}
	return false, nil
}

// Install installs a .ipa or .app bundle.
func (t *Tools) Install(ctx context.Context, udid, path string) error {
	if !SupportedHost() {
		return ErrUnsupportedHost
	}
	if path == "" {
		return errors.New("Install: empty path")
	}
	_, err := t.IDB(ctx, "install", path, "--udid", udid)
	return err
}

// Uninstall removes an app by bundle id.
func (t *Tools) Uninstall(ctx context.Context, udid, bundleID string) error {
	if !SupportedHost() {
		return ErrUnsupportedHost
	}
	_, err := t.IDB(ctx, "uninstall", bundleID, "--udid", udid)
	return err
}

// Terminate stops a running app.
func (t *Tools) Terminate(ctx context.Context, udid, bundleID string) error {
	if !SupportedHost() {
		return ErrUnsupportedHost
	}
	_, err := t.IDB(ctx, "terminate", bundleID, "--udid", udid)
	return err
}

// Launch starts an app and returns its pid. Uses `idb launch --json` so we
// can parse the pid out of the response.
func (t *Tools) Launch(ctx context.Context, udid, bundleID string) (int, error) {
	if !SupportedHost() {
		return 0, ErrUnsupportedHost
	}
	// `idb launch --json` prints something like {"pid": 1234, ...} on success.
	out, err := t.IDB(ctx, "launch", "--json", bundleID, "--udid", udid)
	if err != nil {
		return 0, err
	}
	return parseLaunchPID(out)
}

// parseLaunchPID extracts the pid field from `idb launch --json` output.
// We avoid pulling in a JSON dep here so the function tolerates minor format
// drift (`pid`, `process_id`, plain integer line).
func parseLaunchPID(s string) (int, error) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		for _, key := range []string{`"pid":`, `"process_id":`} {
			if i := strings.Index(line, key); i >= 0 {
				rest := strings.TrimSpace(line[i+len(key):])
				rest = strings.TrimLeft(rest, " \t")
				end := strings.IndexAny(rest, ",} \t\n")
				if end < 0 {
					end = len(rest)
				}
				if n, err := strconv.Atoi(strings.TrimSpace(rest[:end])); err == nil && n > 0 {
					return n, nil
				}
			}
		}
		// Bare integer (some idb versions just print the pid).
		if n, err := strconv.Atoi(line); err == nil && n > 0 {
			return n, nil
		}
	}
	return 0, fmt.Errorf("launch: pid not found in output: %q", strings.TrimSpace(s))
}
