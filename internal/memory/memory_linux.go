//go:build linux

package memory

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

func systemRAMBytes() uint64 {
	total := memTotalBytes()
	if limit := cgroupMemoryLimitBytes(); limit > 0 && (total == 0 || limit < total) {
		// A container's effective budget is its cgroup limit, not the host's RAM:
		// a small container on a big host would otherwise get a budget far larger
		// than it can ever use (P7).
		return limit
	}
	return total
}

// cgroupMemoryLimitBytes reads the current cgroup's memory limit (v2 then v1).
// "max" and the v1 "unlimited" sentinel (near 2^63) are ignored. This does not walk
// parent cgroups: the leaf limit is what a container actually enforces.
func cgroupMemoryLimitBytes() uint64 {
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" || strings.EqualFold(text, "max") {
			continue
		}
		n, err := strconv.ParseUint(text, 10, 64)
		if err != nil || n == 0 || n >= 1<<62 {
			continue
		}
		return n
	}
	return 0
}

func memTotalBytes() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
