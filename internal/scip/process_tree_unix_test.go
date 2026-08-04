//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package scip

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	scippb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// TestScipProcessTreeDescendant is launched by the fake npx shell below. It
// deliberately retains the inherited stdout/stderr descriptors forever; the
// production test proves that group cancellation kills this descendant as well
// as the shell that started it.
func TestScipProcessTreeDescendant(t *testing.T) {
	if os.Getenv("CODEGRAPH_SCIP_DESCENDANT") != "1" {
		return
	}
	if err := os.WriteFile(os.Getenv("CODEGRAPH_SCIP_CHILD_READY"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestRunScipContextTerminatesForkedDescendant(t *testing.T) {
	binDir := t.TempDir()
	markers := t.TempDir()
	parentPIDPath := filepath.Join(markers, "parent.pid")
	childPIDPath := filepath.Join(markers, "child.pid")
	childReadyPath := filepath.Join(markers, "child.ready")
	fakeNpx := filepath.Join(binDir, "npx")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$$" > "$CODEGRAPH_SCIP_PARENT_PID"
"$CODEGRAPH_SCIP_HELPER" -test.run='^TestScipProcessTreeDescendant$' -test.v &
child=$!
printf '%s\n' "$child" > "$CODEGRAPH_SCIP_CHILD_PID"
wait "$child"
`
	writeExecutable(t, fakeNpx, script)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEGRAPH_SCIP_HELPER", os.Args[0])
	t.Setenv("CODEGRAPH_SCIP_PARENT_PID", parentPIDPath)
	t.Setenv("CODEGRAPH_SCIP_CHILD_PID", childPIDPath)
	t.Setenv("CODEGRAPH_SCIP_CHILD_READY", childReadyPath)
	t.Setenv("CODEGRAPH_SCIP_DESCENDANT", "1")

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	done := make(chan struct{})
	var parentPID, childPID int
	go func() {
		_, _, err := RunAndReadContext(ctx, t.TempDir(), filepath.Join(t.TempDir(), "out.scip"))
		result <- err
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		if parentPID > 0 {
			// The PID is from this test's own fake analyzer process group;
			// this cleanup prevents a diagnostic failure from leaking it.
			_ = syscall.Kill(-parentPID, syscall.SIGKILL)
		}
		select {
		case <-done:
		case <-time.After(scipWaitDelay + time.Second):
		}
	})

	var err error
	parentPID, err = waitForUnixPIDFile(parentPIDPath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err = waitForUnixPIDFile(childPIDPath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitForUnixPIDFile(childReadyPath, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled SCIP error = %v, want context.Canceled", err)
		}
	case <-time.After(scipWaitDelay + time.Second):
		t.Fatalf("SCIP process tree did not terminate within %v", scipWaitDelay+time.Second)
	}
	if err := waitForUnixProcessGone(parentPID, scipWaitDelay); err != nil {
		t.Fatal(err)
	}
	if err := waitForUnixProcessGone(childPID, scipWaitDelay); err != nil {
		t.Fatal(err)
	}
}

func TestRunAndRead_SuccessAndCompatibility(t *testing.T) {
	binDir := t.TempDir()
	fixture := filepath.Join(t.TempDir(), "fixture.scip")
	data, err := proto.Marshal(&scippb.Index{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, data, 0o600); err != nil {
		t.Fatal(err)
	}
	fakeNpx := filepath.Join(binDir, "npx")
	script := `#!/bin/sh
set -eu
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output" ]; then
    output=$2
    shift 2
  else
    shift
  fi
done
cp "$CODEGRAPH_SCIP_FIXTURE" "$output"
`
	writeExecutable(t, fakeNpx, script)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEGRAPH_SCIP_FIXTURE", fixture)
	dir := t.TempDir()

	contextOut := filepath.Join(t.TempDir(), "context.scip")
	idx, stats, err := RunAndReadContext(context.Background(), dir, contextOut)
	if err != nil {
		t.Fatalf("RunAndReadContext: %v", err)
	}
	if idx == nil {
		t.Fatal("RunAndReadContext returned nil index")
	}
	if stats.NodeHeapMB <= 0 || stats.Elapsed <= 0 {
		t.Fatalf("resource stats = %+v, want heap and elapsed reporting", stats)
	}
	if _, err := os.Stat(contextOut); err != nil {
		t.Fatalf("SCIP output missing after successful run: %v", err)
	}

	compatOut := filepath.Join(t.TempDir(), "compat.scip")
	compat, _, err := RunAndRead(dir, compatOut)
	if err != nil {
		t.Fatalf("RunAndRead compatibility wrapper: %v", err)
	}
	if compat == nil {
		t.Fatal("RunAndRead compatibility wrapper returned nil index")
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func waitForUnixPIDFile(path string, timeout time.Duration) (int, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil || pid <= 0 {
				return 0, fmt.Errorf("invalid PID in %s: %q", path, data)
			}
			return pid, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		select {
		case <-deadline.C:
			return 0, fmt.Errorf("timed out waiting for PID file %s", path)
		case <-ticker.C:
		}
	}
}

func waitForUnixProcessGone(pid int, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for unixProcessExists(pid) {
		select {
		case <-deadline.C:
			return fmt.Errorf("process %d remains alive after process-tree cancellation", pid)
		case <-ticker.C:
		}
	}
	return nil
}

func unixProcessExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
