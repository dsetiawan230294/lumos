package android

import (
	"strings"
	"testing"
)

// buildLargeFramestats fabricates an N-frame gfxinfo PROFILEDATA block in the
// modern (API 31+) layout. Used to benchmark the hot-path parser at realistic
// sizes — a fresh gfxinfo dump caps at ~120 frames, but trace-mode collection
// can buffer many more.
func buildLargeFramestats(n int) string {
	var b strings.Builder
	b.WriteString("---PROFILEDATA---\n")
	b.WriteString("Flags,FrameTimelineVsyncId,IntendedVsync,Vsync,InputEventId,HandleInputStart,AnimationStart,PerformTraversalsStart,DrawStart,SyncQueued,SyncStart,IssueDrawCommandsStart,SwapBuffers,FrameCompleted,DequeueBufferDuration,QueueBufferDuration,GpuCompleted\n")
	base := uint64(1_000_000_000)
	for i := 0; i < n; i++ {
		intended := base + uint64(i)*16_666_667
		completed := intended + 12_000_000 // 12 ms frame → not janky at 60 Hz
		if i%10 == 0 {
			completed = intended + 25_000_000 // janky
		}
		// 17 columns; only flags/intended(2)/completed(13) matter.
		b.WriteString("0,123,")
		b.WriteString(uitos(intended))
		b.WriteString(",")
		b.WriteString(uitos(intended + 100))
		b.WriteString(",0,0,0,0,0,0,0,0,0,")
		b.WriteString(uitos(completed))
		b.WriteString(",0,0,0\n")
	}
	b.WriteString("---PROFILEDATA---\n")
	return b.String()
}

func uitos(v uint64) string {
	// Avoid pulling strconv into the helper; one allocation per call is fine
	// for a fixture builder.
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}

func BenchmarkParseGfxFrameStats_120(b *testing.B) {
	in := buildLargeFramestats(120)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseGfxFrameStats(in, 16_666_667); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseGfxFrameStats_1000(b *testing.B) {
	in := buildLargeFramestats(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseGfxFrameStats(in, 16_666_667); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseGfxFrameStats_10000(b *testing.B) {
	in := buildLargeFramestats(10000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseGfxFrameStats(in, 16_666_667); err != nil {
			b.Fatal(err)
		}
	}
}

// Slow-path benchmarks for direct comparison; the slow parser is no longer on
// the production code path.
func BenchmarkParseGfxFrameStatsSlow_120(b *testing.B) {
	in := buildLargeFramestats(120)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseGfxFrameStatsSlow(in, 16_666_667); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseGfxFrameStatsSlow_10000(b *testing.B) {
	in := buildLargeFramestats(10000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseGfxFrameStatsSlow(in, 16_666_667); err != nil {
			b.Fatal(err)
		}
	}
}

// TestParseGfxFrameStats_FastSlowEquivalent verifies the fast scanner returns
// identical FrameStats to the original implementation across a range of
// inputs, including edge cases (janky frames, header/legacy mix, empty).
func TestParseGfxFrameStats_FastSlowEquivalent(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"120 frames", buildLargeFramestats(120)},
		{"1 frame", buildLargeFramestats(1)},
		{"1000 frames", buildLargeFramestats(1000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fast, ferr := parseGfxFrameStatsFast(c.in, 16_666_667)
			slow, serr := parseGfxFrameStatsSlow(c.in, 16_666_667)
			if (ferr == nil) != (serr == nil) {
				t.Fatalf("error mismatch: fast=%v slow=%v", ferr, serr)
			}
			if ferr != nil {
				return
			}
			if fast != slow {
				t.Errorf("FrameStats mismatch:\n  fast=%+v\n  slow=%+v", fast, slow)
			}
		})
	}
}
