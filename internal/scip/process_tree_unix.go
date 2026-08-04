//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package scip

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// processTree isolates the analyzer in its own Unix process group. The group
// includes the shell/script and every ordinary descendant it starts, so
// cancellation cannot leave a descendant holding os/exec's captured pipes.
type processTree struct {
	cmd        *exec.Cmd
	mu         sync.Mutex
	attached   bool
	terminated bool
}

func newProcessTree(cmd *exec.Cmd) (processTreeController, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &processTree{cmd: cmd}, nil
}

func (p *processTree) start(_ context.Context) error {
	if err := p.cmd.Start(); err != nil {
		return err
	}
	if err := p.attach(); err != nil {
		cleanupErr := errors.Join(p.terminate(), p.cmd.Wait())
		return errors.Join(fmt.Errorf("attach scip-typescript process tree: %w", err), cleanupErr)
	}
	return nil
}

func (p *processTree) wait() error {
	return p.cmd.Wait()
}

func (p *processTree) pid() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *processTree) attach() error {
	p.mu.Lock()
	p.attached = true
	terminated := p.terminated
	p.mu.Unlock()
	if terminated {
		return p.killGroup()
	}
	return nil
}

func (p *processTree) terminate() error {
	p.mu.Lock()
	p.terminated = true
	attached := p.attached
	p.mu.Unlock()
	if !attached {
		// attach will perform the group kill after Start if cancellation won
		// the startup race.
		return nil
	}
	return p.killGroup()
}

func (p *processTree) killGroup() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func (p *processTree) close() error { return nil }
