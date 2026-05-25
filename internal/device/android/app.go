package android

import (
	"context"
	"fmt"
	"strings"
)

// Install installs (or reinstalls with -r) the given APK on the device.
// Path must be a local file readable by the host.
func (a *ADB) Install(ctx context.Context, serial, apkPath string) error {
	args := []string{}
	if serial != "" {
		args = append(args, "-s", serial)
	}
	args = append(args, "install", "-r", "-g", apkPath)
	out, err := a.run(ctx, args...)
	if err != nil {
		return err
	}
	// adb install prints "Success" on the last non-empty line on success.
	if !strings.Contains(string(out), "Success") {
		return fmt.Errorf("adb install: unexpected output: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall removes the package by id. If the package is absent, the underlying
// adb call returns an error; we surface it as-is so callers can decide.
func (a *ADB) Uninstall(ctx context.Context, serial, appID string) error {
	args := []string{}
	if serial != "" {
		args = append(args, "-s", serial)
	}
	args = append(args, "uninstall", appID)
	out, err := a.run(ctx, args...)
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "Success") {
		return fmt.Errorf("adb uninstall %s: %s", appID, strings.TrimSpace(string(out)))
	}
	return nil
}

// ClearData resets app private storage (equivalent to "Clear data" in settings).
func (a *ADB) ClearData(ctx context.Context, serial, appID string) error {
	out, err := a.Shell(ctx, serial, "pm", "clear", appID)
	if err != nil {
		return err
	}
	if !strings.Contains(out, "Success") {
		return fmt.Errorf("pm clear %s: %s", appID, strings.TrimSpace(out))
	}
	return nil
}

// ForceStop force-stops the app process.
func (a *ADB) ForceStop(ctx context.Context, serial, appID string) error {
	_, err := a.Shell(ctx, serial, "am", "force-stop", appID)
	return err
}

// IsInstalled returns true if the package is present on the device.
func (a *ADB) IsInstalled(ctx context.Context, serial, appID string) (bool, error) {
	out, err := a.Shell(ctx, serial, "pm", "list", "packages", appID)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "package:"+appID {
			return true, nil
		}
	}
	return false, nil
}
