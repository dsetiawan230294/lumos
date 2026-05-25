package android

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// LaunchStats are returned by `am start -W`.
type LaunchStats struct {
	ThisTimeMS  int
	TotalTimeMS int
	WaitTimeMS  int
	Activity    string
}

// LaunchApp performs a cold launch via `am start -W` and returns timings.
// Caller should `force-stop` the app first if a cold-start measurement is
// desired.
func (a *ADB) LaunchApp(ctx context.Context, serial, appID, activity string) (LaunchStats, error) {
	component := appID
	if activity != "" {
		component = appID + "/" + activity
	}
	args := []string{"am", "start", "-W", "-n", component}
	out, err := a.Shell(ctx, serial, args...)
	if err != nil {
		return LaunchStats{}, err
	}
	return parseAmStartW(out)
}

func parseAmStartW(s string) (LaunchStats, error) {
	var ls LaunchStats
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "ThisTime:"):
			ls.ThisTimeMS = atoiAfter(line, "ThisTime:")
		case strings.HasPrefix(line, "TotalTime:"):
			ls.TotalTimeMS = atoiAfter(line, "TotalTime:")
		case strings.HasPrefix(line, "WaitTime:"):
			ls.WaitTimeMS = atoiAfter(line, "WaitTime:")
		case strings.HasPrefix(line, "Activity:"):
			ls.Activity = strings.TrimSpace(strings.TrimPrefix(line, "Activity:"))
		case strings.HasPrefix(line, "Error:"):
			return LaunchStats{}, fmt.Errorf("am start: %s", line)
		}
	}
	if ls.TotalTimeMS == 0 && ls.ThisTimeMS == 0 {
		return LaunchStats{}, fmt.Errorf("am start: no timing in output")
	}
	return ls, nil
}

func atoiAfter(line, marker string) int {
	rest := strings.TrimSpace(strings.TrimPrefix(line, marker))
	for _, tok := range strings.Fields(rest) {
		if n, err := strconv.Atoi(tok); err == nil {
			return n
		}
	}
	return 0
}
