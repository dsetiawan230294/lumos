package android

import "testing"

func TestParseDumpsysBattery(t *testing.T) {
	in := `Current Battery Service state:
  AC powered: false
  USB powered: true
  Wireless powered: false
  Dock powered: false
  Max charging current: 500000
  Max charging voltage: 5000000
  Charge counter: 4899000
  status: 5
  health: 2
  present: true
  level: 87
  scale: 100
  voltage: 4388
  temperature: 323
  technology: Li-poly
`
	b := parseDumpsysBattery(in)
	if b.LevelPct != 87 {
		t.Errorf("LevelPct = %v, want 87", b.LevelPct)
	}
	if b.TemperatureC != 32.3 {
		t.Errorf("TemperatureC = %v, want 32.3", b.TemperatureC)
	}
	if b.VoltageMV != 4388 {
		t.Errorf("VoltageMV = %v, want 4388", b.VoltageMV)
	}
	if b.ChargeCounterUAh != 4_899_000 {
		t.Errorf("ChargeCounterUAh = %v, want 4899000", b.ChargeCounterUAh)
	}
	if !b.OnPower {
		t.Errorf("OnPower = false, want true (USB powered)")
	}
}

func TestParseDumpsysBattery_Unplugged(t *testing.T) {
	in := `  AC powered: false
  USB powered: false
  Wireless powered: false
  level: 42
  temperature: 280
`
	b := parseDumpsysBattery(in)
	if b.OnPower {
		t.Errorf("OnPower = true, want false")
	}
	if b.LevelPct != 42 || b.TemperatureC != 28.0 {
		t.Errorf("got %+v", b)
	}
}

func TestParseDumpsysBattery_Empty(t *testing.T) {
	b := parseDumpsysBattery("")
	if b.LevelPct != 0 || b.OnPower {
		t.Errorf("empty input should yield zero value, got %+v", b)
	}
}
