//go:build linux || darwin

package bench

import "syscall"

// RUSAGE_SELF excludes SCIP/go/compiler children. This high-water mark includes
// worker startup; the orchestration must use a fresh worker for every sample.
func selfPeakRSSBytes() (uint64, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	return rssBytes(usage.Maxrss), nil
}
