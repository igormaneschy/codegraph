package scip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	scippb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"

	"github.com/Lordymine/codegraph/internal/memory"
)

// scipTypescriptVersion is pinned so index output stays reproducible across npx runs.
const scipTypescriptVersion = "0.4.0"

// resolverVersion is part of the graph freshness identity. Bump it when the
// SCIP bridge changes output semantics even if the external package version is
// unchanged.
const resolverVersion = "scip-typescript-bridge-v1@" + scipTypescriptVersion

// ResolverVersion returns the version recorded in an index manifest.
func ResolverVersion() string { return resolverVersion }

// scipWaitDelay bounds both a child that does not exit after cancellation and
// descendants that keep the captured stdout/stderr pipes open. The process-tree
// controller normally makes this a fast path; WaitDelay is the final bounded
// cleanup guard, never an excuse to return while an untracked child remains.
const scipWaitDelay = 2 * time.Second

// RunStats reports resource use of one scip-typescript invocation. PeakRSSBytes is
// sampled from the child process on Linux/WSL (/proc); zero on platforms where RSS
// is not tracked (Windows native).
type RunStats struct {
	PeakRSSBytes uint64
	NodeHeapMB   int
	Elapsed      time.Duration
}

// Read loads a SCIP index from a .scip protobuf file.
func Read(path string) (*scippb.Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	idx := &scippb.Index{}
	if err := proto.Unmarshal(data, idx); err != nil {
		return nil, fmt.Errorf("unmarshal scip: %w", err)
	}
	data = nil
	return idx, nil
}

// RunAndRead runs scip-typescript in dir (which must hold a tsconfig.json and
// installed node_modules), writes the index to outPath, and reads it back. It is
// A non-zero process exit, unreadable output, or invalid protobuf is a resolver
// failure; callers decide whether a first run may commit structural-only data.
func RunAndRead(dir, outPath string) (*scippb.Index, RunStats, error) {
	return RunAndReadContext(context.Background(), dir, outPath)
}

// RunAndReadContext is the cancellable form of RunAndRead. The child process
// receives the context so shutdown can terminate an in-flight SCIP invocation
// before the caller closes its graph engine.
func RunAndReadContext(ctx context.Context, dir, outPath string) (*scippb.Index, RunStats, error) {
	ctx = nonNilContext(ctx)
	if err := requireFreshSCIPOutput(outPath); err != nil {
		return nil, RunStats{}, err
	}
	st, err := runScipContext(ctx, dir, outPath)
	if err != nil {
		return nil, st, err
	}
	if err := ctx.Err(); err != nil {
		return nil, st, err
	}
	if err := validateSCIPOutput(outPath); err != nil {
		return nil, st, err
	}
	idx, err := Read(outPath)
	if err != nil {
		return nil, st, err
	}
	if err := ctx.Err(); err != nil {
		return nil, st, err
	}
	memory.Gate() // drop ReadFile+unmarshal buffers before CallEdges walks the index
	return idx, st, nil
}

// requireFreshSCIPOutput prevents a caller from parsing bytes left by another
// invocation. The indexer normally supplies a newly-created private directory;
// rejecting an existing path keeps the lower-level API honest as well.
func requireFreshSCIPOutput(path string) error {
	if path == "" {
		return errors.New("SCIP output path is empty")
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("SCIP output path already exists before invocation: %q", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect SCIP output path %q: %w", path, err)
	}
	return nil
}

func validateSCIPOutput(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("SCIP invocation produced no output %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("SCIP invocation output %q is not a regular file", path)
	}
	return nil
}

func runScip(dir, outPath string) (RunStats, error) {
	return runScipContext(context.Background(), dir, outPath)
}

func runScipContext(ctx context.Context, dir, outPath string) (st RunStats, retErr error) {
	ctx = nonNilContext(ctx)
	st = RunStats{NodeHeapMB: memory.NodeHeapMB()}
	name, args := npx("@sourcegraph/scip-typescript@"+scipTypescriptVersion, "index", "--output", outPath)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = nodeEnv(st.NodeHeapMB)
	tree, err := newProcessTree(cmd)
	if err != nil {
		return st, fmt.Errorf("prepare scip-typescript process tree: %w", err)
	}
	// CommandContext's default Cancel only kills cmd.Process. Replace it before
	// Start so a cancellation racing process startup still records a pending
	// tree termination in the platform controller.
	cmd.Cancel = tree.terminate
	cmd.WaitDelay = scipWaitDelay
	defer func() {
		if err := tree.close(); err != nil {
			cleanupErr := fmt.Errorf("close scip-typescript process tree: %w", err)
			if retErr == nil {
				retErr = cleanupErr
			} else {
				retErr = errors.Join(retErr, cleanupErr)
			}
		}
	}()

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	t0 := time.Now()
	if err := tree.start(ctx); err != nil {
		return st, fmt.Errorf("start scip-typescript: %w", err)
	}
	done := make(chan struct{})
	peakDone := make(chan struct{})
	var peak atomic.Uint64
	go func() {
		defer close(peakDone)
		peak.Store(peakChildRSS(tree.pid(), done))
	}()
	waitErr := tree.wait()
	close(done)
	<-peakDone
	st.PeakRSSBytes = peak.Load()
	st.Elapsed = time.Since(t0)

	if waitErr != nil {
		if err := ctx.Err(); err != nil {
			return st, fmt.Errorf("scip-typescript canceled: %w", err)
		}
		return st, fmt.Errorf("scip-typescript in %s: %w: %s", dir, waitErr, tail(out.Bytes(), 500))
	}
	return st, nil
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// nodeEnv returns os.Environ with NODE_OPTIONS augmented by --max-old-space-size so
// the scip-typescript child cannot grow past the auto-tuned budget.
func nodeEnv(heapMB int) []string {
	limit := fmt.Sprintf("--max-old-space-size=%d", heapMB)
	env := os.Environ()
	for i, e := range env {
		if strings.HasPrefix(e, "NODE_OPTIONS=") {
			env[i] = e + " " + limit
			return env
		}
	}
	return append(env, "NODE_OPTIONS="+limit)
}

// npx builds the platform-correct invocation. On Windows npx is a .cmd shim, so
// it must be run through cmd.exe to resolve on PATH.
func npx(pkgAndArgs ...string) (string, []string) {
	args := append([]string{"--yes"}, pkgAndArgs...)
	if runtime.GOOS == "windows" {
		return "cmd", append([]string{"/c", "npx"}, args...)
	}
	return "npx", args
}

func tail(b []byte, n int) string {
	if len(b) > n {
		return string(b[len(b)-n:])
	}
	return string(b)
}
