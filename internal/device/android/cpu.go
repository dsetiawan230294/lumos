package android

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Pidof returns the pid of the named process on the device, or 0 if not running.
// Tries `pidof` first (Android 7+), falls back to parsing `ps -A`.
func (a *ADB) Pidof(ctx context.Context, serial, appID string) (int, error) {
	if out, err := a.Shell(ctx, serial, "pidof", appID); err == nil {
		if pid, ok := firstInt(out); ok {
			return pid, nil
		}
	}
	// Fallback: ps -A | grep
	out, err := a.Shell(ctx, serial, "ps", "-A")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasSuffix(strings.TrimSpace(line), " "+appID) && !strings.HasSuffix(strings.TrimSpace(line), "\t"+appID) {
			// Defensive: also accept lines whose last whitespace-separated token equals appID.
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[len(fields)-1] != appID {
				continue
			}
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(fields[1]); err == nil {
			return pid, nil
		}
	}
	return 0, nil
}

func firstInt(s string) (int, bool) {
	for _, tok := range strings.Fields(s) {
		if n, err := strconv.Atoi(tok); err == nil {
			return n, true
		}
	}
	return 0, false
}

// ReadProcStat reads /proc/<pid>/stat and returns (utime+stime, totalCPUTime)
// jiffies. Both values are needed to compute CPU% across two samples.
func (a *ADB) ReadProcStat(ctx context.Context, serial string, pid int) (procJiffies, totalJiffies uint64, err error) {
	procOut, err := a.Shell(ctx, serial, "cat", fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	pj, perr := parseProcPidStat(procOut)
	if perr != nil {
		return 0, 0, perr
	}
	cpuOut, err := a.Shell(ctx, serial, "cat", "/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	tj, terr := parseProcStatTotal(cpuOut)
	if terr != nil {
		return 0, 0, terr
	}
	return pj, tj, nil
}

// parseProcPidStat parses /proc/<pid>/stat and returns utime+stime.
// The comm field (field 2) may contain spaces and is wrapped in parens.
func parseProcPidStat(s string) (uint64, error) {
	open := strings.IndexByte(s, '(')
	close := strings.LastIndexByte(s, ')')
	if open < 0 || close < 0 || close < open {
		return 0, fmt.Errorf("malformed /proc/pid/stat: %q", s)
	}
	rest := strings.TrimSpace(s[close+1:])
	fields := strings.Fields(rest)
	// After ')' the next field is state (index 0 of `fields`), so:
	//   fields[0] = state         (proc field 3)
	//   fields[11] = utime        (proc field 14)
	//   fields[12] = stime        (proc field 15)
	if len(fields) < 13 {
		return 0, fmt.Errorf("too few fields in /proc/pid/stat: %d", len(fields))
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("utime: %w", err)
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("stime: %w", err)
	}
	return utime + stime, nil
}

// parseProcStatTotal parses the first "cpu " line of /proc/stat and returns
// the sum of all jiffies counters.
func parseProcStatTotal(s string) (uint64, error) {
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		var total uint64
		for _, f := range fields {
			n, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse /proc/stat field %q: %w", f, err)
			}
			total += n
		}
		return total, nil
	}
	return 0, fmt.Errorf("no 'cpu' line in /proc/stat")
}

// CPUPercent returns the process CPU % between two ReadProcStat samples,
// scaled to the number of CPU cores on the device (matches `top`'s convention
// where a single fully-loaded core is 100% × ncpu / ncpu = 100%).
//
// Returns 0 if the deltas are non-positive (clock skew, sample reuse).
func CPUPercent(procDelta, totalDelta uint64, ncpu int) float64 {
	if totalDelta == 0 || ncpu <= 0 {
		return 0
	}
	pct := float64(procDelta) / float64(totalDelta) * 100.0 * float64(ncpu)
	if pct < 0 {
		return 0
	}
	return pct
}

// ThreadStat is one thread's snapshot from /proc/<pid>/task/<tid>/stat.
type ThreadStat struct {
	TID     int
	Comm    string // typically <= 15 chars; e.g. "RenderThread", "mqt_js"
	Jiffies uint64 // utime+stime
}

// ReadThreadStats walks /proc/<pid>/task/*/stat in a single adb shell call
// and returns one ThreadStat per thread, plus the /proc/stat total jiffies
// for ratio math.
//
// Performance note: a heavy mobile process can spawn 100–300 threads;
// shelling out 100 times per tick would dominate the sampling budget, so we
// rely on the device's shell glob expansion to emit all thread stats in one
// round trip (~5–10 ms even on slow devices).
func (a *ADB) ReadThreadStats(ctx context.Context, serial string, pid int) (threads []ThreadStat, totalJiffies uint64, err error) {
	// adb shell concatenates all args into a single command line that the
	// device shell parses, so globbing and redirection work naturally.
	// 2>/dev/null swallows the inevitable "no such file" race for threads
	// that die mid-walk.
	out, err := a.Shell(ctx, serial,
		fmt.Sprintf("cat /proc/%d/task/*/stat 2>/dev/null", pid))
	if err != nil {
		return nil, 0, err
	}
	threads = parseThreadStats(out)

	cpuOut, err := a.Shell(ctx, serial, "cat", "/proc/stat")
	if err != nil {
		return threads, 0, err
	}
	totalJiffies, err = parseProcStatTotal(cpuOut)
	if err != nil {
		return threads, 0, err
	}
	return threads, totalJiffies, nil
}

// parseThreadStats parses one or more concatenated /proc/<pid>/task/<tid>/stat
// records. Records are line-delimited. The comm field may contain spaces or
// parens, so we anchor on the LAST ')' in each line (same heuristic as
// parseProcPidStat).
func parseThreadStats(s string) []ThreadStat {
	var out []ThreadStat
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		open := strings.IndexByte(line, '(')
		close := strings.LastIndexByte(line, ')')
		if open < 0 || close < 0 || close <= open {
			continue
		}
		tidStr := strings.TrimSpace(line[:open])
		tid, err := strconv.Atoi(tidStr)
		if err != nil {
			continue
		}
		comm := line[open+1 : close]
		fields := strings.Fields(strings.TrimSpace(line[close+1:]))
		// utime = fields[11], stime = fields[12] (0-indexed after ')').
		if len(fields) < 13 {
			continue
		}
		utime, e1 := strconv.ParseUint(fields[11], 10, 64)
		stime, e2 := strconv.ParseUint(fields[12], 10, 64)
		if e1 != nil || e2 != nil {
			continue
		}
		out = append(out, ThreadStat{
			TID:     tid,
			Comm:    comm,
			Jiffies: utime + stime,
		})
	}
	return out
}
