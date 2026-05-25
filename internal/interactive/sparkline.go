package interactive

import (
	"math"
	"strings"
)

// sparkBars is the 8-level Unicode block ramp used for sparklines. Empty
// values render as a single space so missing data shows as a gap rather
// than ▁ (which would imply "low but present").
var sparkBars = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// Sparkline renders the given values as a width-character unicode bar
// chart, scaled to max(values, scaleMin). NaN / zero values produce a
// blank cell.
//
// If len(values) > width, the most recent `width` values are kept.
func Sparkline(values []float64, width int, scaleMin float64) string {
	if width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	max := scaleMin
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		if v > max {
			max = v
		}
	}
	if max <= 0 {
		max = 1
	}
	var b strings.Builder
	// Left-pad so the bar is right-aligned (newest sample touches the right edge).
	for i := 0; i < width-len(values); i++ {
		b.WriteByte(' ')
	}
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			b.WriteByte(' ')
			continue
		}
		idx := int(v / max * float64(len(sparkBars)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkBars) {
			idx = len(sparkBars) - 1
		}
		b.WriteRune(sparkBars[idx])
	}
	return b.String()
}
