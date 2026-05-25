package android

import (
	"context"
	"strconv"
	"strings"
)

// BatteryStats is a snapshot of `dumpsys battery`.
//
// Android exposes battery level as 0–100 and temperature in tenths of a degree
// Celsius (323 → 32.3 °C). ChargeCounterUAh is the cumulative coulomb counter
// in microamp-hours; the delta between two samples is the actual charge drawn
// in that window (more precise than the integer level%, which only updates
// every 1%).
type BatteryStats struct {
	LevelPct         float64 // 0–100, NaN if unavailable
	TemperatureC     float64 // °C
	VoltageMV        float64 // millivolts
	ChargeCounterUAh float64 // microamp-hours (cumulative; diff for drain)
	OnPower          bool    // AC/USB/Wireless any plugged in
}

// Battery runs `dumpsys battery` and parses the result.
func (a *ADB) Battery(ctx context.Context, serial string) (BatteryStats, error) {
	out, err := a.Shell(ctx, serial, "dumpsys", "battery")
	if err != nil {
		return BatteryStats{}, err
	}
	return parseDumpsysBattery(out), nil
}

func parseDumpsysBattery(s string) BatteryStats {
	var b BatteryStats
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "level":
			if v, err := strconv.ParseFloat(val, 64); err == nil {
				b.LevelPct = v
			}
		case "temperature":
			// tenths of a degree C → divide by 10.
			if v, err := strconv.ParseFloat(val, 64); err == nil {
				b.TemperatureC = v / 10.0
			}
		case "voltage":
			if v, err := strconv.ParseFloat(val, 64); err == nil {
				b.VoltageMV = v
			}
		case "Charge counter":
			if v, err := strconv.ParseFloat(val, 64); err == nil {
				b.ChargeCounterUAh = v
			}
		case "AC powered", "USB powered", "Wireless powered", "Dock powered":
			if val == "true" {
				b.OnPower = true
			}
		}
	}
	return b
}
