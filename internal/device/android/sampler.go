package android

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

// SamplerConfig controls how the per-device sampling loop runs.
type SamplerConfig struct {
	Serial   string
	AppID    string
	Pid      int
	Interval time.Duration // sample period, default 1s
	NCPU     int           // number of CPU cores, default 1
	BudgetNs uint64        // frame deadline in ns, default 16_666_667 (60Hz)
	// Threads enables per-thread CPU% sampling. When true, each emitted
	// Sample.Threads is populated from /proc/<pid>/task/*/stat. Adds one
	// extra adb roundtrip per tick.
	Threads bool
}

// NCPU returns the number of online CPU cores on the device. Returns 1 on
// failure so CPU math still works.
func (a *ADB) NCPU(ctx context.Context, serial string) int {
	out, err := a.Shell(ctx, serial, "cat", "/sys/devices/system/cpu/online")
	if err != nil {
		return 1
	}
	// Format: "0-7" or "0,2-7".
	count := 0
	for _, span := range strings.Split(strings.TrimSpace(out), ",") {
		span = strings.TrimSpace(span)
		if span == "" {
			continue
		}
		lo, hi, ok := strings.Cut(span, "-")
		if !ok {
			count++
			continue
		}
		l, e1 := strconv.Atoi(lo)
		h, e2 := strconv.Atoi(hi)
		if e1 == nil && e2 == nil && h >= l {
			count += h - l + 1
		}
	}
	if count < 1 {
		return 1
	}
	return count
}

// Sample streams metrics.Sample values until ctx is cancelled. Each tick:
//
//  1. Snapshot CPU jiffies (proc + total) and compute % vs. previous tick.
//  2. Run dumpsys meminfo and extract Total PSS in MB.
//  3. Run dumpsys gfxinfo framestats, then reset, to derive FPS/jank for the
//     window we just observed.
//
// Failures of any single collector are logged into the sample's error-free
// fields as zeros — the loop never crashes the run.
//
// The returned channel is closed when ctx is cancelled.
func (a *ADB) Sample(ctx context.Context, cfg SamplerConfig) (<-chan metrics.Sample, error) {
	if cfg.Pid <= 0 {
		return nil, fmt.Errorf("Sample: pid must be > 0")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	if cfg.NCPU <= 0 {
		cfg.NCPU = 1
	}
	if cfg.BudgetNs == 0 {
		cfg.BudgetNs = 16_666_667
	}

	out := make(chan metrics.Sample, 8)
	// Prime the CPU baseline so the first emitted sample is meaningful.
	prevProc, prevTotal, _ := a.ReadProcStat(ctx, cfg.Serial, cfg.Pid)
	// Prime gfxinfo so the first window starts clean.
	_ = a.GfxReset(ctx, cfg.Serial, cfg.AppID)
	// Prime per-thread baseline if enabled.
	prevThreads := map[int]uint64{}
	prevThreadTotal := uint64(0)
	if cfg.Threads {
		if ts, tj, err := a.ReadThreadStats(ctx, cfg.Serial, cfg.Pid); err == nil {
			for _, t := range ts {
				prevThreads[t.TID] = t.Jiffies
			}
			prevThreadTotal = tj
		}
	}

	go func() {
		defer close(out)
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				s := metrics.Sample{T: t}

				if pj, tj, err := a.ReadProcStat(ctx, cfg.Serial, cfg.Pid); err == nil {
					if tj > prevTotal && pj >= prevProc {
						s.CPUPct = CPUPercent(pj-prevProc, tj-prevTotal, cfg.NCPU)
					}
					prevProc, prevTotal = pj, tj
				}

				if mi, err := a.MemInfo(ctx, cfg.Serial, cfg.AppID); err == nil {
					s.RAMMB = mi.TotalPSSMB
				}

				if fs, err := a.GfxFrameStats(ctx, cfg.Serial, cfg.AppID, cfg.BudgetNs); err == nil {
					s.FPS = fs.FPS
					s.FrameMS = fs.AvgFrameMS
					s.JankPct = fs.JankPercent
					_ = a.GfxReset(ctx, cfg.Serial, cfg.AppID)
				}

				if b, err := a.Battery(ctx, cfg.Serial); err == nil {
					s.BatteryPct = b.LevelPct
					s.BatteryTempC = b.TemperatureC
				}

				if cfg.Threads {
					if ts, tj, err := a.ReadThreadStats(ctx, cfg.Serial, cfg.Pid); err == nil {
						if tj > prevThreadTotal {
							totalDelta := tj - prevThreadTotal
							breakdown := map[string]float64{}
							for _, t := range ts {
								prev, ok := prevThreads[t.TID]
								if !ok || t.Jiffies < prev {
									continue
								}
								d := t.Jiffies - prev
								if d == 0 {
									continue
								}
								pct := CPUPercent(d, totalDelta, cfg.NCPU)
								if pct <= 0 {
									continue
								}
								breakdown[t.Comm] += pct
							}
							if len(breakdown) > 0 {
								s.Threads = breakdown
							}
						}
						// Refresh baseline. Track only TIDs that still exist
						// so the map doesn't grow unboundedly across long runs.
						next := make(map[int]uint64, len(ts))
						for _, t := range ts {
							next[t.TID] = t.Jiffies
						}
						prevThreads = next
						prevThreadTotal = tj
					}
				}

				select {
				case out <- s:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}
