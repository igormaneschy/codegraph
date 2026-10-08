//go:build linux

package scip

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// peakChildRSS polls the whole process subtree rooted at pid until until is
// closed and returns the maximum summed VmRSS observed (bytes). npx spawns the real
// Node process as a child, so sampling only pid would understate the resolver's
// peak; walking descendants reports what the tree actually used (P7).
func peakChildRSS(pid int, until <-chan struct{}) uint64 {
	var peak uint64
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-until:
			return peak
		case <-tick.C:
			if rss := treeRSS(pid); rss > peak {
				peak = rss
			}
		}
	}
}

// treeRSS sums VmRSS over pid and all descendants.
func treeRSS(pid int) uint64 {
	seen := map[int]bool{}
	var sum uint64
	var visit func(int)
	visit = func(p int) {
		if p <= 0 || seen[p] {
			return
		}
		seen[p] = true
		sum += procRSS(p)
		for _, child := range childPIDs(p) {
			visit(child)
		}
	}
	visit(pid)
	return sum
}

// childPIDs lists the direct children of every thread of pid. Reading only the
// main thread's children misses children spawned by other threads.
func childPIDs(pid int) []int {
	paths, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
	var pids []int
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			if n, err := strconv.Atoi(field); err == nil {
				pids = append(pids, n)
			}
		}
	}
	return pids
}

func procRSS(pid int) uint64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
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
