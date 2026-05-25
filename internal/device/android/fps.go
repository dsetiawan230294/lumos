package android

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// FrameStats are derived from `dumpsys gfxinfo <pkg> framestats`.
//
// Android's GfxInfo prints a CSV-like "PROFILEDATA" block where each row is
// one frame and columns are nanosecond timestamps of pipeline stages. We use:
//   - Flags        (col 0)  — skip flagged frames (window animations, etc.)
//   - IntendedVsync(col 1)  — frame's start
//   - FrameCompleted(col 13) — when the buffer was handed to SurfaceFlinger
//
// A frame is "janky" if FrameCompleted - IntendedVsync exceeds the deadline
// (typically 16.67 ms at 60 Hz; 8.33 ms at 120 Hz). Caller supplies the
// expected frame budget in nanoseconds.
type FrameStats struct {
	Frames      int
	JankyFrames int
	AvgFrameMS  float64
	P50FrameMS  float64
	P90FrameMS  float64
	P99FrameMS  float64
	JankPercent float64
	FPS         float64 // 1000 / avgFrameMs (best-effort)
}

// GfxFrameStats runs `dumpsys gfxinfo <appID> framestats` and parses one
// snapshot. The caller should also `dumpsys gfxinfo <appID> reset` between
// samples to avoid accumulating data.
//
// budgetNs is the per-frame deadline in nanoseconds (e.g. 16_666_667 for 60 Hz).
func (a *ADB) GfxFrameStats(ctx context.Context, serial, appID string, budgetNs uint64) (FrameStats, error) {
	out, err := a.Shell(ctx, serial, "dumpsys", "gfxinfo", appID, "framestats")
	if err != nil {
		return FrameStats{}, err
	}
	return parseGfxFrameStats(out, budgetNs)
}

// GfxReset resets accumulated gfxinfo counters for the given package.
func (a *ADB) GfxReset(ctx context.Context, serial, appID string) error {
	_, err := a.Shell(ctx, serial, "dumpsys", "gfxinfo", appID, "reset")
	return err
}

func parseGfxFrameStats(s string, budgetNs uint64) (FrameStats, error) {
	return parseGfxFrameStatsFast(s, budgetNs)
}

// parseGfxFrameStatsSlow is the original implementation, kept only so the
// benchmark suite can compare implementations. Not called in production.
func parseGfxFrameStatsSlow(s string, budgetNs uint64) (FrameStats, error) {
	if budgetNs == 0 {
		budgetNs = 16_666_667 // default 60 Hz
	}
	var frameMS []float64
	var janky int

	inBlock := false
	// Column indexes are resolved from the header row each PROFILEDATA block so we
	// adapt to Android version changes (e.g. API 31+ added FrameTimelineVsyncId,
	// shifting Intended Vsync from col 1→2 and FrameCompleted from col 13→16).
	idxFlags, idxIntended, idxCompleted := -1, -1, -1
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "---PROFILEDATA---":
			inBlock = !inBlock
			if !inBlock {
				idxFlags, idxIntended, idxCompleted = -1, -1, -1
			}
			continue
		case !inBlock:
			continue
		case line == "":
			continue
		case strings.HasPrefix(line, "Flags"):
			idxFlags, idxIntended, idxCompleted = resolveGfxColumns(line)
			continue
		}
		if idxIntended < 0 || idxCompleted < 0 {
			// Header missing — fall back to legacy fixed layout (pre-API-31).
			idxFlags, idxIntended, idxCompleted = 0, 1, 13
		}
		fields := strings.Split(line, ",")
		need := idxCompleted
		if idxIntended > need {
			need = idxIntended
		}
		if idxFlags > need {
			need = idxFlags
		}
		if len(fields) <= need {
			continue
		}
		flags, _ := strconv.ParseUint(strings.TrimSpace(fields[idxFlags]), 10, 64)
		if flags != 0 {
			continue
		}
		intendedVsync, err1 := strconv.ParseUint(strings.TrimSpace(fields[idxIntended]), 10, 64)
		frameCompleted, err2 := strconv.ParseUint(strings.TrimSpace(fields[idxCompleted]), 10, 64)
		if err1 != nil || err2 != nil || frameCompleted <= intendedVsync {
			continue
		}
		dur := frameCompleted - intendedVsync
		frameMS = append(frameMS, float64(dur)/1e6)
		if dur > budgetNs {
			janky++
		}
	}

	if len(frameMS) == 0 {
		return FrameStats{}, fmt.Errorf("no frame samples in gfxinfo output")
	}

	fs := FrameStats{
		Frames:      len(frameMS),
		JankyFrames: janky,
		AvgFrameMS:  mean(frameMS),
		P50FrameMS:  percentile(frameMS, 50),
		P90FrameMS:  percentile(frameMS, 90),
		P99FrameMS:  percentile(frameMS, 99),
		JankPercent: 100.0 * float64(janky) / float64(len(frameMS)),
	}
	if fs.AvgFrameMS > 0 {
		fs.FPS = 1000.0 / fs.AvgFrameMS
	}
	return fs, nil
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// resolveGfxColumns parses a gfxinfo framestats header line (comma-separated
// column names) and returns the 0-based indexes of Flags, IntendedVsync, and
// FrameCompleted. Returns -1 for any missing column so the caller can fall
// back to the legacy fixed layout.
func resolveGfxColumns(header string) (flags, intended, completed int) {
	flags, intended, completed = -1, -1, -1
	for i, name := range strings.Split(header, ",") {
		switch strings.TrimSpace(name) {
		case "Flags":
			flags = i
		case "IntendedVsync":
			intended = i
		case "FrameCompleted":
			completed = i
		}
	}
	return
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	// Copy to avoid mutating caller's slice.
	sorted := make([]float64, len(xs))
	copy(sorted, xs)
	sortFloat64s(sorted)
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := int(float64(len(sorted)-1) * p / 100.0)
	return sorted[idx]
}

// sortFloat64s sorts in place ascending. Tiny insertion sort to avoid pulling
// the sort package's overhead for short frame slices; for large slices Go's
// sort would be faster, but framestats blocks are bounded.
func sortFloat64s(xs []float64) {
	for i := 1; i < len(xs); i++ {
		v := xs[i]
		j := i - 1
		for j >= 0 && xs[j] > v {
			xs[j+1] = xs[j]
			j--
		}
		xs[j+1] = v
	}
}
