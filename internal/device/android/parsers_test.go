package android

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestParseProcPidStat(t *testing.T) {
	// pid (comm with spaces) state ppid pgrp session tty_nr tpgid flags minflt cminflt majflt cmajflt utime stime ...
	in := `1234 (com.example app) S 1 1234 1234 0 -1 4194304 100 0 0 0 250 50 0 0 20 0 1 0 ...`
	got, err := parseProcPidStat(in)
	if err != nil {
		t.Fatalf("parseProcPidStat: %v", err)
	}
	if got != 300 {
		t.Errorf("utime+stime = %d, want 300", got)
	}
}

func TestParseProcStatTotal(t *testing.T) {
	in := "cpu  1000 200 300 4000 50 0 10 0 0 0\ncpu0 ...\n"
	got, err := parseProcStatTotal(in)
	if err != nil {
		t.Fatalf("parseProcStatTotal: %v", err)
	}
	if got != 5560 {
		t.Errorf("total = %d, want 5560", got)
	}
}

func TestCPUPercent(t *testing.T) {
	// Process consumed 100 jiffies of 1000 total, 8 cores → 100/1000 * 100 * 8 = 80%.
	got := CPUPercent(100, 1000, 8)
	if math.Abs(got-80.0) > 1e-9 {
		t.Errorf("CPUPercent = %v, want 80", got)
	}
	if CPUPercent(10, 0, 8) != 0 {
		t.Errorf("zero totalDelta should yield 0")
	}
}

func TestParseMemInfo_TableFormat(t *testing.T) {
	in := `** MEMINFO in pid 1234 [com.example.app] **
                   Pss  Private  Private  SwapPss      Rss     Heap     Heap     Heap
                 Total    Dirty    Clean    Dirty    Total     Size    Alloc     Free
                ------   ------   ------   ------   ------   ------   ------   ------
  Native Heap     2048     2048        0        0     2200     8192     4096     2048
  Java Heap       4096     4096        0        0     5000    16384     8192     4096
  Graphics        1024        0        0        0
         TOTAL   10240     5120      512      256    12000
`
	mi, err := parseMemInfo(in)
	if err != nil {
		t.Fatalf("parseMemInfo: %v", err)
	}
	if mi.TotalPSSMB != 10.0 {
		t.Errorf("TotalPSSMB = %v, want 10", mi.TotalPSSMB)
	}
	if mi.TotalRSSMB == 0 {
		t.Errorf("TotalRSSMB should be parsed from row")
	}
	if mi.NativeMB != 2.0 {
		t.Errorf("NativeMB = %v, want 2", mi.NativeMB)
	}
	if mi.JavaHeapMB != 4.0 {
		t.Errorf("JavaHeapMB = %v, want 4", mi.JavaHeapMB)
	}
	if mi.GraphicsMB != 1.0 {
		t.Errorf("GraphicsMB = %v, want 1", mi.GraphicsMB)
	}
}

func TestParseGfxFrameStats(t *testing.T) {
	// Legacy (pre-API-31) layout: 14 columns, IntendedVsync at col 1,
	// FrameCompleted at col 13.
	in := strings.Join([]string{
		"---PROFILEDATA---",
		"Flags,IntendedVsync,Vsync,OldestInputEvent,NewestInputEvent,HandleInputStart,AnimationStart,PerformTraversalsStart,DrawStart,SyncQueued,SyncStart,IssueDrawCommandsStart,SwapBuffers,FrameCompleted",
		// frame 1: 10ms (good)
		"0,1000000000,1000000000,0,0,0,0,0,0,0,0,0,0,1010000000",
		// frame 2: 20ms (jank @ 60Hz)
		"0,2000000000,2000000000,0,0,0,0,0,0,0,0,0,0,2020000000",
		// frame 3: flagged → skipped
		"1,3000000000,3000000000,0,0,0,0,0,0,0,0,0,0,3010000000",
		"---PROFILEDATA---",
	}, "\n")
	fs, err := parseGfxFrameStats(in, 16_666_667)
	if err != nil {
		t.Fatalf("parseGfxFrameStats: %v", err)
	}
	if fs.Frames != 2 {
		t.Errorf("Frames = %d, want 2", fs.Frames)
	}
	if fs.JankyFrames != 1 {
		t.Errorf("JankyFrames = %d, want 1", fs.JankyFrames)
	}
	if math.Abs(fs.AvgFrameMS-15.0) > 0.01 {
		t.Errorf("AvgFrameMS = %v, want 15", fs.AvgFrameMS)
	}
	if math.Abs(fs.JankPercent-50.0) > 1e-9 {
		t.Errorf("JankPercent = %v, want 50", fs.JankPercent)
	}
	if fs.FPS <= 0 {
		t.Errorf("FPS should be positive, got %v", fs.FPS)
	}
}

// TestParseGfxFrameStats_Modern covers the API 31+ layout that adds
// FrameTimelineVsyncId (column 1) and several pipeline columns, shifting
// IntendedVsync to col 2 and FrameCompleted to col 16. Regression test for
// the real-device bug where FPS came back as ~0.0001.
func TestParseGfxFrameStats_Modern(t *testing.T) {
	header := "Flags,FrameTimelineVsyncId,IntendedVsync,Vsync,InputEventId,HandleInputStart,AnimationStart,PerformTraversalsStart,DrawStart,FrameDeadline,FrameInterval,FrameStartTime,SyncQueued,SyncStart,IssueDrawCommandsStart,SwapBuffers,FrameCompleted,DequeueBufferDuration,QueueBufferDuration,GpuCompleted,SwapBuffersCompleted,DisplayPresentTime,CommandSubmissionCompleted"
	row := func(intended, completed uint64) string {
		// 23 columns matching the header above.
		parts := make([]string, 23)
		for i := range parts {
			parts[i] = "0"
		}
		parts[2] = strconv.FormatUint(intended, 10)
		parts[16] = strconv.FormatUint(completed, 10)
		return strings.Join(parts, ",")
	}
	in := strings.Join([]string{
		"---PROFILEDATA---",
		header,
		row(1_000_000_000, 1_010_000_000), // 10ms (good)
		row(2_000_000_000, 2_020_000_000), // 20ms (jank)
		"---PROFILEDATA---",
	}, "\n")
	fs, err := parseGfxFrameStats(in, 16_666_667)
	if err != nil {
		t.Fatalf("parseGfxFrameStats: %v", err)
	}
	if fs.Frames != 2 {
		t.Fatalf("Frames = %d, want 2", fs.Frames)
	}
	if fs.JankyFrames != 1 {
		t.Errorf("JankyFrames = %d, want 1", fs.JankyFrames)
	}
	if math.Abs(fs.AvgFrameMS-15.0) > 0.01 {
		t.Errorf("AvgFrameMS = %v, want 15 (regression — modern column layout)", fs.AvgFrameMS)
	}
	if fs.FPS < 50 || fs.FPS > 100 {
		t.Errorf("FPS = %v, want ~66.7 (15ms avg)", fs.FPS)
	}
}

func TestParseAmStartW(t *testing.T) {
	in := `Starting: Intent { cmp=com.example/.MainActivity }
Status: ok
Activity: com.example/.MainActivity
ThisTime: 412
TotalTime: 412
WaitTime: 433
Complete
`
	ls, err := parseAmStartW(in)
	if err != nil {
		t.Fatalf("parseAmStartW: %v", err)
	}
	if ls.TotalTimeMS != 412 || ls.WaitTimeMS != 433 || ls.Activity != "com.example/.MainActivity" {
		t.Errorf("unexpected: %#v", ls)
	}
}
