//go:build !linux && !darwin

package bench

import "fmt"

func selfPeakRSSBytes() (uint64, error) {
	return 0, fmt.Errorf("process-self RSS measurement requires Linux or macOS")
}
