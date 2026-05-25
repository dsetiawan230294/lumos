package interactive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
	"github.com/dsetiawan230294/lumos/internal/report"

	"golang.org/x/term"
)

// DeviceSpec describes one device the watch session will display.
type DeviceSpec struct {
	ID       string
	Platform metrics.Platform
	Label    string // model / display name
	AppID    string
}

// SamplerFunc is the contract for kicking off a per-device sampler. It is
// the same shape as runner.SamplerFunc; we redefine it here so this package
// does not pull in internal/runner (which would be a cycle).
type SamplerFunc func(ctx context.Context, dev DeviceSpec) (<-chan metrics.Sample, error)

// Config configures a watch session.
type Config struct {
	Devices []DeviceSpec
	Sampler SamplerFunc
	OutDir  string
	Tool    string
	Version string

	// FrameInterval is how often the screen is repainted. Default 200ms.
	FrameInterval time.Duration
	// Input / Output streams. Default os.Stdin / os.Stdout.
	Input  *os.File
	Output io.Writer
	// NoRaw disables raw-mode terminal handling (used by tests and dumb
	// terminals). When set, key input is ignored and the renderer paints
	// without screen-clear/cursor-hide.
	NoRaw bool
}

// Run starts a watch session. It blocks until the user quits ('q' or
// ctrl-c) or ctx is cancelled. On exit it writes one JSON report per
// device into OutDir, matching the schema used by `lumos run`.
func Run(ctx context.Context, cfg Config) error {
	if len(cfg.Devices) == 0 {
		return errors.New("interactive: no devices supplied")
	}
	if cfg.Sampler == nil {
		return errors.New("interactive: Sampler required")
	}
	if cfg.FrameInterval <= 0 {
		cfg.FrameInterval = 200 * time.Millisecond
	}
	if cfg.Input == nil {
		cfg.Input = os.Stdin
	}
	if cfg.Output == nil {
		cfg.Output = os.Stdout
	}

	panes := make([]*DevicePane, 0, len(cfg.Devices))
	for _, d := range cfg.Devices {
		panes = append(panes, &DevicePane{
			ID: d.ID, Platform: d.Platform, Label: d.Label, AppID: d.AppID,
		})
	}
	model := NewModel(panes)

	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Spin up one sampler per device.
	var wg sync.WaitGroup
	for _, d := range cfg.Devices {
		d := d
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, err := cfg.Sampler(sessCtx, d)
			if err != nil {
				model.SetStatus(d.ID, "error: "+err.Error())
				return
			}
			for s := range ch {
				model.AddSample(d.ID, s)
			}
		}()
	}

	// Raw mode (skip in tests / no-raw mode).
	var restore func()
	if !cfg.NoRaw {
		if state, err := term.MakeRaw(int(cfg.Input.Fd())); err == nil {
			fd := int(cfg.Input.Fd())
			restore = func() { _ = term.Restore(fd, state) }
		}
		fmt.Fprint(cfg.Output, ansiHideCursor+ansiClearScrn+ansiHome)
	}
	defer func() {
		if restore != nil {
			restore()
		}
		fmt.Fprint(cfg.Output, ansiShowCursor+ansiReset+"\r\n")
	}()

	// Key reader.
	keys := make(chan KeyEvent, 16)
	if !cfg.NoRaw {
		go ReadKeys(sessCtx, cfg.Input, keys)
	}

	// Render loop.
	frame := time.NewTicker(cfg.FrameInterval)
	defer frame.Stop()

	width := terminalWidth(cfg.Input)
	rcfg := RenderConfig{Width: width, NoColour: cfg.NoRaw}

	Render(cfg.Output, model.Snapshot(), rcfg)
	pendingLabel := ""
	for {
		select {
		case <-sessCtx.Done():
			goto done
		case <-frame.C:
			Render(cfg.Output, model.Snapshot(), rcfg)
		case ev := <-keys:
			if handleKey(model, ev, &pendingLabel) {
				goto done
			}
			Render(cfg.Output, model.Snapshot(), rcfg)
		}
	}
done:
	cancel()
	wg.Wait()

	final := model.CloseAll()
	return writeReports(final, cfg.OutDir, cfg.Tool, cfg.Version)
}

// handleKey applies one key event to the model. Returns true if the session
// should exit.
func handleKey(m *Model, ev KeyEvent, pendingLabel *string) bool {
	switch ev.Key {
	case "ctrl-c":
		return true
	case "tab":
		m.FocusNext()
	case "shift-tab":
		m.FocusPrev()
	case "rune":
		switch ev.Rune {
		case 'q', 'Q':
			return true
		case 's', 'S':
			m.StartSegment("segment")
		case 'e', 'E':
			m.EndSegment()
		case 'm', 'M':
			m.MarkPoint("marker")
		case 'r', 'R':
			m.ResetFocused()
		}
	}
	_ = pendingLabel // reserved for future label-entry prompt
	return false
}

func writeReports(panes []DevicePane, outDir, tool, version string) error {
	if outDir == "" {
		return nil
	}
	for _, p := range panes {
		run := metrics.Run{
			Scenario:  "watch",
			Iteration: 1,
			DeviceID:  p.ID,
			Platform:  p.Platform,
			StartedAt: p.StartedAt,
			EndedAt:   p.EndedAt,
			Samples:   p.Samples,
			Markers:   p.Markers,
		}
		if _, err := report.WriteRun(outDir, tool, version, run); err != nil {
			return fmt.Errorf("write report for %s: %w", p.ID, err)
		}
	}
	return nil
}

func terminalWidth(in *os.File) int {
	if in == nil {
		return 100
	}
	if w, _, err := term.GetSize(int(in.Fd())); err == nil && w > 0 {
		return w
	}
	return 100
}
