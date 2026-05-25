// Package interactive implements the `lumos watch` TUI for manual,
// hotkey-driven benchmarking sessions.
//
// Design notes:
//   - The model layer (this file) is a pure state machine: events go in,
//     a snapshot comes out. No I/O or rendering. Fully unit-testable.
//   - The watch orchestrator (watch.go) wires real samplers and a raw
//     terminal renderer around this model.
//   - Each device has its own pane and its own segment timeline. Hotkeys
//     act on the focused pane; Tab cycles focus.
package interactive

import (
	"sync"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

// DefaultRingCapacity is how many recent samples each device keeps in its
// in-memory ring for sparkline rendering. Older samples are still recorded
// to the session timeline and JSON output; only the live display is bounded.
const DefaultRingCapacity = 64

// DevicePane is the per-device slice of state shown in one TUI pane.
type DevicePane struct {
	ID       string
	Platform metrics.Platform
	Label    string // human label (model, name, …)
	AppID    string

	// Status one-liner: "sampling", "starting", "error: …", "stopped".
	Status string

	// Samples is the full history (unbounded) — used for the JSON report.
	Samples []metrics.Sample
	// Ring is the last DefaultRingCapacity samples — used for sparklines.
	Ring []metrics.Sample
	// Latest is the most recently appended sample, for headline values.
	Latest metrics.Sample
	// SampleCount counts every sample ever observed.
	SampleCount int

	// All markers recorded in this session for this device.
	Markers []metrics.Marker
	// ActiveSegment is the label of the currently-open segment (empty if
	// none). Set by Hotkey 's', cleared by 'e'.
	ActiveSegment string
	// SegmentStart is when the active segment was opened.
	SegmentStart time.Time

	// Sessions started/ended timestamps for the JSON report.
	StartedAt time.Time
	EndedAt   time.Time
}

// Model is the full TUI state. It is safe for concurrent use; readers can
// call Snapshot to get a stable copy.
type Model struct {
	mu sync.RWMutex

	panes     []*DevicePane
	focus     int // index into panes
	startedAt time.Time

	ringCap int
}

// NewModel constructs a Model with one pane per supplied device.
func NewModel(panes []*DevicePane) *Model {
	now := time.Now()
	for _, p := range panes {
		if p.StartedAt.IsZero() {
			p.StartedAt = now
		}
		if p.Status == "" {
			p.Status = "starting"
		}
	}
	return &Model{
		panes:     panes,
		focus:     0,
		startedAt: now,
		ringCap:   DefaultRingCapacity,
	}
}

// Snapshot returns a deep-ish copy of the model suitable for rendering
// without holding the lock. Inner slices are shared; the renderer must
// treat them as read-only.
type Snapshot struct {
	Panes     []DevicePane
	Focus     int
	StartedAt time.Time
}

// Snapshot returns a stable view of the model.
func (m *Model) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := Snapshot{Focus: m.focus, StartedAt: m.startedAt}
	out.Panes = make([]DevicePane, len(m.panes))
	for i, p := range m.panes {
		out.Panes[i] = *p
		// shallow-copy slices so renderer's iteration is safe even if a
		// concurrent AddSample mutates m.panes[i].Ring after we return.
		out.Panes[i].Ring = append([]metrics.Sample(nil), p.Ring...)
		out.Panes[i].Markers = append([]metrics.Marker(nil), p.Markers...)
	}
	return out
}

// AddSample appends one sample to the named device's history and ring.
func (m *Model) AddSample(deviceID string, s metrics.Sample) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.findLocked(deviceID)
	if p == nil {
		return
	}
	p.Samples = append(p.Samples, s)
	p.Ring = append(p.Ring, s)
	if len(p.Ring) > m.ringCap {
		p.Ring = p.Ring[len(p.Ring)-m.ringCap:]
	}
	p.Latest = s
	p.SampleCount++
	if p.Status == "starting" || p.Status == "" {
		p.Status = "sampling"
	}
}

// SetStatus updates a device's status line (e.g. "error: pid not found").
func (m *Model) SetStatus(deviceID, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.findLocked(deviceID); p != nil {
		p.Status = status
	}
}

// FocusNext cycles to the next pane.
func (m *Model) FocusNext() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.panes) == 0 {
		return
	}
	m.focus = (m.focus + 1) % len(m.panes)
}

// FocusPrev cycles to the previous pane.
func (m *Model) FocusPrev() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.panes) == 0 {
		return
	}
	m.focus = (m.focus - 1 + len(m.panes)) % len(m.panes)
}

// MarkPoint records an ad-hoc marker ("m" key) on the focused device.
func (m *Model) MarkPoint(label string) metrics.Marker {
	return m.addMarker(label, "point")
}

// StartSegment opens a segment on the focused device.
func (m *Model) StartSegment(label string) metrics.Marker {
	mk := m.addMarker(label, "start")
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.focusedLocked(); p != nil {
		p.ActiveSegment = label
		p.SegmentStart = mk.T
	}
	return mk
}

// EndSegment closes the active segment on the focused device. If no
// segment is open, this is a no-op and returns a zero Marker.
func (m *Model) EndSegment() metrics.Marker {
	m.mu.Lock()
	p := m.focusedLocked()
	if p == nil || p.ActiveSegment == "" {
		m.mu.Unlock()
		return metrics.Marker{}
	}
	label := p.ActiveSegment
	p.ActiveSegment = ""
	p.SegmentStart = time.Time{}
	m.mu.Unlock()
	return m.addMarker(label, "end")
}

// ResetFocused clears samples + markers on the focused pane (keeps the
// session running).
func (m *Model) ResetFocused() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.focusedLocked(); p != nil {
		p.Samples = nil
		p.Ring = p.Ring[:0]
		p.Markers = nil
		p.SampleCount = 0
		p.ActiveSegment = ""
		p.SegmentStart = time.Time{}
		p.StartedAt = time.Now()
	}
}

// CloseAll stamps EndedAt on every pane and returns a copy of the final
// state for writing to disk.
func (m *Model) CloseAll() []DevicePane {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := make([]DevicePane, len(m.panes))
	for i, p := range m.panes {
		if p.EndedAt.IsZero() {
			p.EndedAt = now
		}
		out[i] = *p
		out[i].Samples = append([]metrics.Sample(nil), p.Samples...)
		out[i].Ring = append([]metrics.Sample(nil), p.Ring...)
		out[i].Markers = append([]metrics.Marker(nil), p.Markers...)
	}
	return out
}

func (m *Model) addMarker(label, kind string) metrics.Marker {
	if label == "" {
		label = "marker"
	}
	mk := metrics.Marker{T: time.Now(), Label: label, Kind: kind}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.focusedLocked(); p != nil {
		p.Markers = append(p.Markers, mk)
	}
	return mk
}

func (m *Model) focusedLocked() *DevicePane {
	if len(m.panes) == 0 {
		return nil
	}
	if m.focus < 0 || m.focus >= len(m.panes) {
		m.focus = 0
	}
	return m.panes[m.focus]
}

func (m *Model) findLocked(id string) *DevicePane {
	for _, p := range m.panes {
		if p.ID == id {
			return p
		}
	}
	return nil
}
