package android

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// MemInfo holds parsed memory totals for an app process (units: KB on device,
// converted to MB at parse time for convenience).
type MemInfo struct {
	TotalPSSMB float64
	JavaHeapMB float64
	NativeMB   float64
	GraphicsMB float64
	TotalRSSMB float64
}

// MemInfo runs `dumpsys meminfo <appID>` and parses the TOTAL line.
//
// Output shape we rely on (Android 11+):
//
//	** MEMINFO in pid 1234 [com.example.app] **
//	                   Pss  Private  Private  SwapPss      Rss     Heap     Heap     Heap
//	                 Total    Dirty    Clean    Dirty    Total     Size    Alloc     Free
//	                ------   ------   ------   ------   ------   ------   ------   ------
//	  Native Heap     1234      ...
//	  ...
//	         TOTAL    9876      ...
func (a *ADB) MemInfo(ctx context.Context, serial, appID string) (MemInfo, error) {
	out, err := a.Shell(ctx, serial, "dumpsys", "meminfo", appID)
	if err != nil {
		return MemInfo{}, err
	}
	return parseMemInfo(out)
}

func parseMemInfo(s string) (MemInfo, error) {
	var mi MemInfo
	var sawTotal bool
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "TOTAL PSS:"):
			// Older format: "TOTAL PSS: 12345  TOTAL RSS: 23456  ..."
			mi.TotalPSSMB = kbToMB(grabIntAfter(trim, "TOTAL PSS:"))
			mi.TotalRSSMB = kbToMB(grabIntAfter(trim, "TOTAL RSS:"))
			sawTotal = true
		case strings.HasPrefix(trim, "TOTAL "):
			// Table row: TOTAL <pss> <pd> <pc> <swap> <rss> ...
			fields := strings.Fields(trim)
			if len(fields) >= 2 {
				if n, err := strconv.Atoi(fields[1]); err == nil {
					mi.TotalPSSMB = kbToMB(uint64(n))
					sawTotal = true
				}
			}
			if len(fields) >= 6 {
				if n, err := strconv.Atoi(fields[5]); err == nil {
					mi.TotalRSSMB = kbToMB(uint64(n))
				}
			}
		case strings.HasPrefix(trim, "Native Heap "):
			mi.NativeMB = kbToMB(firstUintField(trim, 0))
		case strings.HasPrefix(trim, "Java Heap "):
			mi.JavaHeapMB = kbToMB(firstUintField(trim, 0))
		case strings.HasPrefix(trim, "Graphics "):
			mi.GraphicsMB = kbToMB(firstUintField(trim, 0))
		}
	}
	if !sawTotal {
		return MemInfo{}, fmt.Errorf("dumpsys meminfo: TOTAL row not found")
	}
	return mi, nil
}

func kbToMB(kb uint64) float64 { return float64(kb) / 1024.0 }

func grabIntAfter(line, marker string) uint64 {
	i := strings.Index(line, marker)
	if i < 0 {
		return 0
	}
	rest := strings.TrimSpace(line[i+len(marker):])
	for _, tok := range strings.Fields(rest) {
		if n, err := strconv.ParseUint(tok, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// firstUintField returns the nth numeric token in line (0-indexed among
// numeric tokens only), or 0 if absent.
func firstUintField(line string, nth int) uint64 {
	seen := 0
	for _, tok := range strings.Fields(line) {
		if n, err := strconv.ParseUint(tok, 10, 64); err == nil {
			if seen == nth {
				return n
			}
			seen++
		}
	}
	return 0
}
