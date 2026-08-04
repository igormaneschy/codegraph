//go:build !windows && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package scip

import (
	"context"
	"fmt"
	"os/exec"
)

// Unsupported targets fail closed rather than silently falling back to killing
// only cmd.Process, which could leave a descendant holding captured pipes.
type processTree struct{}

func newProcessTree(*exec.Cmd) (processTreeController, error) {
	return nil, fmt.Errorf("process-tree cancellation is unsupported on this platform")
}

func (*processTree) start(context.Context) error {
	return fmt.Errorf("process-tree cancellation is unsupported on this platform")
}
func (*processTree) wait() error {
	return fmt.Errorf("process-tree cancellation is unsupported on this platform")
}
func (*processTree) pid() int { return 0 }

func (*processTree) attach() error {
	return fmt.Errorf("process-tree cancellation is unsupported on this platform")
}
func (*processTree) terminate() error {
	return fmt.Errorf("process-tree cancellation is unsupported on this platform")
}
func (*processTree) close() error { return nil }
