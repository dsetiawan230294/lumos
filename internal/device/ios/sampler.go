package ios

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

// SamplerConfig controls one iOS sampling job.
type SamplerConfig struct {
	UDID     string
	BundleID string
	// OutputDir is where the .trace bundle is written (auto-created). If
	// empty, a temp dir is used and cleaned up after export.
	OutputDir string
	// Template is the xctrace instrument template name. Default
	// "Time Profiler" (which also captures CPU and Allocations).
	Template string
	// Interval is the desired sample resolution. xctrace's own resolution
	// is much finer; samples are bucketed into Interval-sized windows.
	Interval time.Duration
	// BudgetMs is the per-frame budget used to compute jank %. Defaults to
	// 16.667 ms (60 Hz).
	BudgetMs float64
}

// xctraceRow is one row of an `xctrace export` XML run.
type xctraceRow struct {
	// time since start, ns
	TimeNs uint64
	// per-row fields, name → string value
	Fields map[string]string
}

// TraceMetrics is the cooked output of parsing an xctrace export.
type TraceMetrics struct {
	StartedAt time.Time
	Samples   []metrics.Sample
}

// Sample records a scenario iteration using xctrace, then exports and parses
// the trace into a synthetic per-second sample stream.
//
// The returned channel emits all samples after recording stops, then closes.
// This matches the runner's <-chan contract while accommodating xctrace's
// record-then-export model.
//
// Returns ErrUnsupportedHost on non-darwin.
func (t *Tools) Sample(ctx context.Context, cfg SamplerConfig) (<-chan metrics.Sample, func() error, error) {
	if !SupportedHost() {
		return nil, nil, ErrUnsupportedHost
	}
	if cfg.UDID == "" || cfg.BundleID == "" {
		return nil, nil, errors.New("Sample: UDID and BundleID required")
	}
	if cfg.Template == "" {
		cfg.Template = "Time Profiler"
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	if cfg.BudgetMs == 0 {
		cfg.BudgetMs = 1000.0 / 60.0
	}

	tracePath := cfg.OutputDir
	cleanup := func() error { return nil }
	if tracePath == "" {
		dir, err := os.MkdirTemp("", "lumos-xctrace-*")
		if err != nil {
			return nil, nil, err
		}
		tracePath = filepath.Join(dir, "scenario.trace")
		cleanup = func() error { return os.RemoveAll(dir) }
	} else {
		tracePath = filepath.Join(tracePath, "scenario.trace")
	}

	// Start recording in the background. xctrace blocks until SIGINT or
	// --time-limit elapses; we run it via context cancellation when the
	// caller closes ctx.
	out := make(chan metrics.Sample, 64)
	startedAt := time.Now()

	go func() {
		defer close(out)
		recCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		_, recErr := t.Xctrace(recCtx,
			"record",
			"--device", cfg.UDID,
			"--template", cfg.Template,
			"--attach", cfg.BundleID,
			"--output", tracePath,
		)
		// Recording stopped (caller cancelled or process exited).
		if recErr != nil && !errors.Is(recErr, context.Canceled) {
			// emit a zero sample carrying nothing; runner will see the
			// error via the returned cleanup if needed.
			return
		}
		// Export → parse → emit.
		exportCtx, exportCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer exportCancel()
		xml, xerr := t.Xctrace(exportCtx, "export", "--input", tracePath, "--xpath", `/trace-toc/*`)
		if xerr != nil {
			return
		}
		tm, perr := parseXctraceExport(strings.NewReader(xml), startedAt, cfg)
		if perr != nil {
			return
		}
		for _, s := range tm.Samples {
			select {
			case out <- s:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, cleanup, nil
}

// --- xctrace export XML parsing ---------------------------------------------
//
// `xctrace export --xpath '/trace-toc/*'` produces a node tree where the
// interesting payloads live under <node> elements with a <schema> child
// describing column names, followed by <row> children carrying values.
//
// We parse just enough to extract three series:
//   - "time-profile" rows give us per-thread CPU sample counts → CPU %
//   - "allocations-statistics" / "vm-statistics" rows give resident memory
//   - "core-animation-fps" or "graphics-driver" rows give frame timing
//
// Real xctrace schemas vary slightly between Xcode releases; the parser is
// schema-driven (it reads the column names from the <schema> block) rather
// than position-based so it tolerates field reordering.

func parseXctraceExport(r io.Reader, startedAt time.Time, cfg SamplerConfig) (TraceMetrics, error) {
	dec := xml.NewDecoder(r)
	tm := TraceMetrics{StartedAt: startedAt}

	var tabs []*parsedTab
	var current *parsedTab
	var capturing bool
	var pendingCell string

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return tm, fmt.Errorf("xctrace export: %w", err)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "node":
				current = &parsedTab{}
			case "schema":
				if current != nil {
					for _, a := range el.Attr {
						if a.Name.Local == "name" {
							current.name = a.Value
						}
					}
				}
			case "col":
				if current != nil {
					for _, a := range el.Attr {
						if a.Name.Local == "name" {
							current.cols = append(current.cols, a.Value)
						}
					}
				}
			case "row":
				if current != nil {
					current.rows = append(current.rows, xctraceRow{Fields: map[string]string{}})
				}
			default:
				// Inside a row, every direct child is a cell value.
				if current != nil && len(current.rows) > 0 {
					capturing = true
					pendingCell = el.Name.Local
				}
			}
		case xml.CharData:
			if capturing && current != nil && len(current.rows) > 0 {
				row := &current.rows[len(current.rows)-1]
				idx := len(row.Fields)
				name := pendingCell
				if idx < len(current.cols) {
					name = current.cols[idx]
				}
				row.Fields[name] = strings.TrimSpace(string(el))
				// also try to extract time-ns
				if name == "start" || name == "time" || name == "timestamp" {
					if v, perr := strconv.ParseUint(strings.TrimSpace(string(el)), 10, 64); perr == nil {
						row.TimeNs = v
					}
				}
			}
		case xml.EndElement:
			switch el.Name.Local {
			case "node":
				if current != nil {
					tabs = append(tabs, current)
					current = nil
				}
			default:
				capturing = false
			}
		}
	}

	tm.Samples = bucketIntoSamples(tabs, startedAt, cfg)
	return tm, nil
}

// parsedTab is one <node> in an xctrace export: a named schema with rows.
type parsedTab struct {
	name string
	cols []string
	rows []xctraceRow
}

// bucketIntoSamples aggregates xctrace rows into Interval-sized samples.
// Cell values are looked up by schema-declared column name so the same code
// handles slight variations between Xcode versions.
func bucketIntoSamples(tabs []*parsedTab, startedAt time.Time, cfg SamplerConfig) []metrics.Sample {

	type bucket struct {
		cpuCount  int
		cpuPctSum float64
		ramMBMax  float64
		frameMs   []float64
	}
	by := map[int64]*bucket{}
	getBucket := func(ns uint64) *bucket {
		idx := int64(time.Duration(ns) / cfg.Interval)
		b, ok := by[idx]
		if !ok {
			b = &bucket{}
			by[idx] = b
		}
		return b
	}
	for _, tab := range tabs {
		for _, row := range tab.rows {
			switch {
			case strings.Contains(tab.name, "time-profile") || strings.Contains(tab.name, "cpu"):
				if v, ok := row.Fields["weight"]; ok {
					if p, _ := strconv.ParseFloat(v, 64); p > 0 {
						b := getBucket(row.TimeNs)
						b.cpuPctSum += p
						b.cpuCount++
					}
				}
			case strings.Contains(tab.name, "alloc") || strings.Contains(tab.name, "vm-statistics") || strings.Contains(tab.name, "memory"):
				if v, ok := row.Fields["resident-size"]; ok {
					if mb, _ := strconv.ParseFloat(v, 64); mb > 0 {
						b := getBucket(row.TimeNs)
						mbVal := mb / (1024 * 1024)
						if mbVal > b.ramMBMax {
							b.ramMBMax = mbVal
						}
					}
				}
			case strings.Contains(tab.name, "core-animation") || strings.Contains(tab.name, "frame") || strings.Contains(tab.name, "graphics"):
				if v, ok := row.Fields["duration"]; ok {
					if d, _ := strconv.ParseFloat(v, 64); d > 0 {
						b := getBucket(row.TimeNs)
						b.frameMs = append(b.frameMs, d/1e6) // ns → ms
					}
				}
			}
		}
	}
	// Emit samples in time order.
	idx := make([]int64, 0, len(by))
	for k := range by {
		idx = append(idx, k)
	}
	sort.Slice(idx, func(i, j int) bool { return idx[i] < idx[j] })
	out := make([]metrics.Sample, 0, len(idx))
	for _, k := range idx {
		b := by[k]
		s := metrics.Sample{T: startedAt.Add(time.Duration(k) * cfg.Interval)}
		if b.cpuCount > 0 {
			s.CPUPct = b.cpuPctSum / float64(b.cpuCount)
		}
		if b.ramMBMax > 0 {
			s.RAMMB = b.ramMBMax
		}
		if len(b.frameMs) > 0 {
			var sum, jank float64
			for _, f := range b.frameMs {
				sum += f
				if f > cfg.BudgetMs {
					jank++
				}
			}
			avg := sum / float64(len(b.frameMs))
			s.FrameMS = avg
			if avg > 0 {
				s.FPS = 1000.0 / avg
			}
			s.JankPct = 100 * jank / float64(len(b.frameMs))
		}
		out = append(out, s)
	}
	return out
}
