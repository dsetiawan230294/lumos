package android

import "testing"

func TestDedupByHardwareSerial(t *testing.T) {
	in := []DeviceInfo{
		{Serial: "192.168.100.22:43665", State: "device",
			Extra: map[string]string{"hw_serial": "PHONE_A"}},
		{Serial: "adb-PHONE_A-x._adb-tls-connect._tcp.", State: "device",
			Extra: map[string]string{"hw_serial": "PHONE_A"}},
		{Serial: "GUJ7EIW85P8DSGHI", State: "device",
			Extra: map[string]string{"hw_serial": "PHONE_B"}},
		// Unauthorized device with no hw_serial probe — must pass through.
		{Serial: "OFFLINE1", State: "unauthorized",
			Extra: map[string]string{}},
	}
	out := dedupByHardwareSerial(in)
	if len(out) != 3 {
		t.Fatalf("len(out)=%d want 3: %+v", len(out), out)
	}
	gotSerials := map[string]bool{}
	for _, d := range out {
		gotSerials[d.Serial] = true
	}
	// PHONE_A should keep the IP form, not the mDNS form.
	if !gotSerials["192.168.100.22:43665"] {
		t.Errorf("expected IP transport kept for PHONE_A, got: %+v", out)
	}
	if gotSerials["adb-PHONE_A-x._adb-tls-connect._tcp."] {
		t.Errorf("mDNS transport should have been dropped: %+v", out)
	}
	if !gotSerials["GUJ7EIW85P8DSGHI"] {
		t.Errorf("USB device missing from output: %+v", out)
	}
	if !gotSerials["OFFLINE1"] {
		t.Errorf("unauthorized device should pass through: %+v", out)
	}
}

func TestDedupByHardwareSerial_USBBeatsIP(t *testing.T) {
	// If a phone is both USB-connected (serial like ABC123) AND wireless
	// (192.168.x.y:port) at the same time, prefer USB (lower latency).
	in := []DeviceInfo{
		{Serial: "192.168.1.5:5555", State: "device",
			Extra: map[string]string{"hw_serial": "P"}},
		{Serial: "ABC123XYZ", State: "device",
			Extra: map[string]string{"hw_serial": "P"}},
	}
	out := dedupByHardwareSerial(in)
	if len(out) != 1 || out[0].Serial != "ABC123XYZ" {
		t.Fatalf("expected USB serial kept, got %+v", out)
	}
}

func TestTransportKind(t *testing.T) {
	cases := []struct {
		serial string
		want   int
	}{
		{"GUJ7EIW85P8DSGHI", 0},
		{"192.168.100.22:43665", 1},
		{"adb-S48DWCJ7I79DF6CI-QeFFOh._adb-tls-connect._tcp.", 2},
	}
	for _, c := range cases {
		if got := transportKind(c.serial); got != c.want {
			t.Errorf("transportKind(%q)=%d want %d", c.serial, got, c.want)
		}
	}
}
