package ios

import (
	"context"
	"strings"
	"testing"
	"time"
)

type stubExec struct {
	// keyed on first arg (the subcommand) → stdout
	out map[string]string
	err map[string]error
}

func (s *stubExec) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	key := name
	if len(args) > 0 {
		key = name + " " + args[0]
	}
	if e, ok := s.err[key]; ok {
		return nil, nil, e
	}
	return []byte(s.out[key]), nil, nil
}

func TestParseIDBList(t *testing.T) {
	in := `{"udid":"00008110-001A4D281A78801E","name":"Pintu iPhone","model":"iPhone14,5","os_version":"17.4.1","state":"booted","target_type":"device","architecture":"arm64"}
{"udid":"ABCDEF12-3456-7890-ABCD-EF1234567890","name":"iPhone 15 Pro","model":"iPhone16,1","os_version":"17.5","state":"Shutdown","target_type":"simulator","architecture":"arm64"}
`
	devs, err := parseIDBList(in)
	if err != nil {
		t.Fatalf("parseIDBList: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("want 2 devices, got %d", len(devs))
	}
	if devs[0].UDID != "00008110-001A4D281A78801E" {
		t.Errorf("UDID = %q", devs[0].UDID)
	}
	if devs[0].State != "device" {
		t.Errorf("state normalisation: got %q want 'device'", devs[0].State)
	}
	if devs[0].Simulator {
		t.Errorf("real device flagged as simulator")
	}
	if !devs[1].Simulator {
		t.Errorf("simulator not flagged")
	}
	if devs[1].State != "offline" {
		t.Errorf("Shutdown→%q, want 'offline'", devs[1].State)
	}
}

func TestParseXctraceList(t *testing.T) {
	in := `== Devices ==
my-mac (15.4.1) (00000000-0000-0000-0000-000000000000)
Pintu's iPhone (17.4.1) (00008110-001A4D281A78801E)

== Simulators ==
iPhone 15 Pro (17.5) (ABCDEF12-3456-7890-ABCD-EF1234567890)
iPad Air (17.5) (11111111-2222-3333-4444-555555555555)
`
	devs := parseXctraceList(in)
	if len(devs) != 4 {
		t.Fatalf("want 4 devices, got %d", len(devs))
	}
	// real iPhone
	if devs[1].UDID != "00008110-001A4D281A78801E" || devs[1].OSVersion != "17.4.1" {
		t.Errorf("real iphone parsed wrong: %+v", devs[1])
	}
	if devs[1].Simulator {
		t.Errorf("real iphone flagged as simulator")
	}
	if !devs[2].Simulator {
		t.Errorf("simulator section not flagged")
	}
}

func TestParseLaunchPID(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{`{"pid": 1234, "bundle_id":"x"}`, 1234},
		{`{"process_id":987}`, 987},
		{"5678\n", 5678},
		{`{"pid":42}`, 42},
	}
	for _, c := range cases {
		got, err := parseLaunchPID(c.in)
		if err != nil {
			t.Errorf("parseLaunchPID(%q) err=%v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseLaunchPID(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	if _, err := parseLaunchPID(`{"foo":"bar"}`); err == nil {
		t.Errorf("expected error on missing pid")
	}
}

func TestUnsupportedHostGuards(t *testing.T) {
	if !SupportedHost() {
		t.Skip("non-darwin host: guards are returning ErrUnsupportedHost already")
	}
	// On darwin the guards should not short-circuit; they delegate to exec
	// which we stub out to nothing. We just exercise the wiring here.
	tools := New(WithExecer(&stubExec{out: map[string]string{"idb list-targets": ""}}))
	if _, err := tools.Devices(context.Background()); err != nil {
		t.Errorf("Devices on darwin with stub exec: %v", err)
	}
}

func TestParseXctraceExport_BucketsByInterval(t *testing.T) {
	// Synthetic xctrace export: one CPU table, one memory table, one frames table.
	// Times are in ns. Interval=1s, so events at 100ms and 800ms fall into bucket 0,
	// events at 1.2s fall into bucket 1.
	in := `<trace-query-result>
  <node>
    <schema name="time-profile">
      <col name="start"/><col name="weight"/>
    </schema>
    <row><start>100000000</start><weight>20</weight></row>
    <row><start>800000000</start><weight>40</weight></row>
    <row><start>1200000000</start><weight>60</weight></row>
  </node>
  <node>
    <schema name="vm-statistics">
      <col name="start"/><col name="resident-size"/>
    </schema>
    <row><start>500000000</start><resident-size>104857600</resident-size></row>
    <row><start>1500000000</start><resident-size>209715200</resident-size></row>
  </node>
  <node>
    <schema name="core-animation-fps">
      <col name="start"/><col name="duration"/>
    </schema>
    <!-- bucket 0: 10ms (good) and 25ms (jank @ 60Hz) -->
    <row><start>100000000</start><duration>10000000</duration></row>
    <row><start>200000000</start><duration>25000000</duration></row>
    <!-- bucket 1: 16ms (good) -->
    <row><start>1100000000</start><duration>16000000</duration></row>
  </node>
</trace-query-result>`

	start := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	cfg := SamplerConfig{Interval: time.Second, BudgetMs: 1000.0 / 60.0}
	tm, err := parseXctraceExport(strings.NewReader(in), start, cfg)
	if err != nil {
		t.Fatalf("parseXctraceExport: %v", err)
	}
	if len(tm.Samples) != 2 {
		t.Fatalf("want 2 buckets, got %d (%+v)", len(tm.Samples), tm.Samples)
	}
	// Bucket 0
	b0 := tm.Samples[0]
	if b0.CPUPct == 0 {
		t.Errorf("bucket0 CPU = 0")
	}
	if b0.RAMMB < 99 || b0.RAMMB > 101 {
		t.Errorf("bucket0 RAM = %v, want ~100MB", b0.RAMMB)
	}
	if b0.JankPct != 50 {
		t.Errorf("bucket0 jank = %v, want 50%%", b0.JankPct)
	}
	if b0.FPS == 0 {
		t.Errorf("bucket0 fps = 0")
	}
	// Bucket 1
	b1 := tm.Samples[1]
	if b1.RAMMB < 199 || b1.RAMMB > 201 {
		t.Errorf("bucket1 RAM = %v, want ~200MB", b1.RAMMB)
	}
	if b1.JankPct != 0 {
		t.Errorf("bucket1 jank = %v, want 0", b1.JankPct)
	}
	if b1.T.Sub(b0.T) != time.Second {
		t.Errorf("bucket spacing = %v", b1.T.Sub(b0.T))
	}
}
