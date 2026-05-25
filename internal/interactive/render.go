package interactive

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// ANSI control sequences. Kept as raw strings (no fmt) so we can write them
// straight to the terminal without per-frame allocation hot spots.
const (
	ansiHideCursor = "\x1b[?25l"
	ansiShowCursor = "\x1b[?25h"
	ansiClearScrn  = "\x1b[2J"
	ansiHome       = "\x1b[H"
	ansiClearLine  = "\x1b[2K"
	ansiReset      = "\x1b[0m"
	ansiBold       = "\x1b[1m"
	ansiDim        = "\x1b[2m"
	ansiInverse    = "\x1b[7m"
	ansiCyan       = "\x1b[36m"
	ansiGreen      = "\x1b[32m"
	ansiYellow     = "\x1b[33m"
	ansiRed        = "\x1b[31m"
	ansiBlue       = "\x1b[34m"
)

// RenderConfig configures the renderer. Width is the target terminal width;
// values <40 are clamped to 40.
type RenderConfig struct {
	Width    int
	NoColour bool // for tests / dumb terminals
}

// Render writes the full TUI frame for the snapshot to w. The frame begins
// with cursor-home + clear-to-end-of-line per row so successive frames do
// not flash. Caller is responsible for clearing the screen on first draw.
func Render(w io.Writer, snap Snapshot, cfg RenderConfig) {
	if cfg.Width < 40 {
		cfg.Width = 40
	}
	dim := func(s string) string {
		if cfg.NoColour {
			return s
		}
		return ansiDim + s + ansiReset
	}
	bold := func(s string) string {
		if cfg.NoColour {
			return s
		}
		return ansiBold + s + ansiReset
	}
	colour := func(c, s string) string {
		if cfg.NoColour {
			return s
		}
		return c + s + ansiReset
	}

	fmt.Fprint(w, ansiHome)

	// Header.
	elapsed := time.Since(snap.StartedAt).Round(time.Second)
	header := fmt.Sprintf(" Lumos watch — %d device(s) — %s elapsed ",
		len(snap.Panes), elapsed)
	help := "  [Tab] focus  [s] segment start  [e] end  [m] mark  [r] reset  [q] quit "
	writeLine(w, padRight(bold(header), cfg.Width))
	writeLine(w, padRight(dim(help), cfg.Width))
	writeLine(w, padRight(strings.Repeat("─", cfg.Width), cfg.Width))

	if len(snap.Panes) == 0 {
		writeLine(w, padRight("  (no devices)", cfg.Width))
		fmt.Fprint(w, "\x1b[J") // clear from cursor down
		return
	}

	sparkW := cfg.Width - 24
	if sparkW < 10 {
		sparkW = 10
	}

	for i, p := range snap.Panes {
		focused := i == snap.Focus
		marker := "  "
		if focused {
			marker = colour(ansiCyan, "▶ ")
		}
		title := fmt.Sprintf("%s%s  %s  %s",
			marker, p.Platform, p.ID, p.Label)
		if focused {
			title = bold(title)
		}
		writeLine(w, padRight(title, cfg.Width))

		statusColour := ansiGreen
		switch {
		case strings.HasPrefix(p.Status, "error"):
			statusColour = ansiRed
		case p.Status == "starting":
			statusColour = ansiYellow
		case p.Status == "stopped":
			statusColour = ansiDim
		}
		appLine := fmt.Sprintf("    app=%s  samples=%d  status=%s",
			p.AppID, p.SampleCount, colour(statusColour, p.Status))
		writeLine(w, padRight(appLine, cfg.Width))

		// Per-metric rows. Headline value left, sparkline right.
		fpsVals := make([]float64, 0, len(p.Ring))
		cpuVals := make([]float64, 0, len(p.Ring))
		ramVals := make([]float64, 0, len(p.Ring))
		jnkVals := make([]float64, 0, len(p.Ring))
		for _, s := range p.Ring {
			fpsVals = append(fpsVals, s.FPS)
			cpuVals = append(cpuVals, s.CPUPct)
			ramVals = append(ramVals, s.RAMMB)
			jnkVals = append(jnkVals, s.JankPct)
		}
		writeLine(w, padRight(metricLine("FPS", fmt.Sprintf("%6.1f", p.Latest.FPS), Sparkline(fpsVals, sparkW, 60), ansiCyan, cfg.NoColour), cfg.Width))
		writeLine(w, padRight(metricLine("CPU", fmt.Sprintf("%5.1f%%", p.Latest.CPUPct), Sparkline(cpuVals, sparkW, 100), ansiGreen, cfg.NoColour), cfg.Width))
		writeLine(w, padRight(metricLine("RAM", fmt.Sprintf("%5.0f MB", p.Latest.RAMMB), Sparkline(ramVals, sparkW, 50), ansiBlue, cfg.NoColour), cfg.Width))
		writeLine(w, padRight(metricLine("JNK", fmt.Sprintf("%5.1f%%", p.Latest.JankPct), Sparkline(jnkVals, sparkW, 10), ansiYellow, cfg.NoColour), cfg.Width))

		// Segment + markers summary.
		seg := "    "
		if p.ActiveSegment != "" {
			seg = fmt.Sprintf("    %s segment %q open for %s",
				colour(ansiYellow, "●"), p.ActiveSegment,
				time.Since(p.SegmentStart).Round(time.Second))
		}
		mk := fmt.Sprintf("markers=%d", len(p.Markers))
		writeLine(w, padRight(seg+"   "+dim(mk), cfg.Width))
		writeLine(w, padRight("", cfg.Width))
	}
	// Clear anything left over from a previous, taller frame.
	fmt.Fprint(w, "\x1b[J")
}

func metricLine(label, value, spark, colour string, noColour bool) string {
	lbl := label
	if !noColour {
		lbl = colour + label + ansiReset
	}
	return fmt.Sprintf("    %s  %s  %s", lbl, value, spark)
}

func writeLine(w io.Writer, s string) {
	fmt.Fprint(w, ansiClearLine)
	fmt.Fprint(w, s)
	fmt.Fprint(w, "\r\n")
}

// padRight pads s with spaces (or truncates) so its visible width is
// approximately n. This is a best-effort fast path: it counts runes, not
// terminal cells, so wide East Asian glyphs may end up slightly off.
func padRight(s string, n int) string {
	visible := visibleWidth(s)
	if visible >= n {
		return s
	}
	return s + strings.Repeat(" ", n-visible)
}

// visibleWidth counts runes outside ANSI escape sequences.
func visibleWidth(s string) int {
	count := 0
	inEsc := false
	for _, r := range s {
		if r == 0x1b {
			inEsc = true
			continue
		}
		if inEsc {
			// Sequence ends at the first letter (m, K, J, H, …).
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		count++
	}
	return count
}
