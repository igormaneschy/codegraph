package index

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Lordymine/codegraph/internal/graph"
)

func TestRunAtomic_PreservesGraphOnFailure(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "main.go")
	if err := os.WriteFile(p, []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "g.db")

	if _, err := RunAtomic(dbPath, dir); err != nil {
		t.Fatalf("first index: %v", err)
	}
	project := ProjectName(dir)
	st, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	nBefore, eBefore, err := st.Stats(project)
	st.Close()
	if err != nil || nBefore == 0 {
		t.Fatalf("expected indexed graph, nodes=%d err=%v", nBefore, err)
	}

	if err := os.WriteFile(p, []byte("package main\nfunc main() { panic(1) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipelinePreflightErr = errors.New("injected pipeline failure")
	defer func() { pipelinePreflightErr = nil }()

	if _, err := RunAtomic(dbPath, dir); err == nil {
		t.Fatal("expected injected failure")
	}
	if _, err := os.Stat(dbPath + BuildingSuffix); !os.IsNotExist(err) {
		t.Fatalf("building file should be removed on failure, stat err=%v", err)
	}

	st, err = graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	nAfter, eAfter, err := st.Stats(project)
	if err != nil {
		t.Fatal(err)
	}
	if nAfter != nBefore || eAfter != eBefore {
		t.Fatalf("live graph changed on failed re-index: before=%d/%d after=%d/%d", nBefore, eBefore, nAfter, eAfter)
	}
}

func TestRunAtomic_CancellationImmediatelyBeforeReplacementKeepsLiveGraph(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "x.go")
	if err := os.WriteFile(source, []byte("package x\nfunc oldSymbol() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "graph.db")
	if _, err := RunAtomic(dbPath, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("package x\nfunc newSymbol() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	oldHook := beforeBuiltIndexReplaceHook
	t.Cleanup(func() {
		beforeBuiltIndexReplaceHook = oldHook
		cancel()
	})
	beforeBuiltIndexReplaceHook = func() { cancel() }
	if _, err := RunAtomicContext(ctx, dbPath, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation before replacement error=%v, want context.Canceled", err)
	}

	for _, artifact := range []string{dbPath + BuildingSuffix, manifestBuildingPath(dbPath), manifestBuildingPath(dbPath) + ".tmp"} {
		if _, err := os.Stat(artifact); !os.IsNotExist(err) {
			t.Fatalf("canceled replacement artifact %q remains: %v", artifact, err)
		}
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(dir)
	oldHits, oldErr := store.Search(project, "oldSymbol", "", 5)
	newHits, newErr := store.Search(project, "newSymbol", "", 5)
	if oldErr != nil || newErr != nil {
		t.Fatalf("live graph after pre-linearization cancellation: old=%v new=%v", oldErr, newErr)
	}
	if len(oldHits) != 1 || len(newHits) != 0 {
		t.Fatalf("pre-linearization cancellation changed live graph: old=%d new=%d", len(oldHits), len(newHits))
	}
}

func TestRunAtomic_CommitsOnSuccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\nfunc F() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "g.db")
	for _, artifact := range []string{
		dbPath + BuildingSuffix,
		manifestBuildingPath(dbPath),
		manifestBuildingPath(dbPath) + ".tmp",
	} {
		if err := os.WriteFile(artifact, []byte("stale build artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RunAtomic(dbPath, dir); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range []string{
		dbPath + BuildingSuffix,
		manifestBuildingPath(dbPath),
		manifestBuildingPath(dbPath) + ".tmp",
	} {
		if _, err := os.Stat(artifact); !os.IsNotExist(err) {
			t.Fatalf("build artifact %q should not remain after success: %v", artifact, err)
		}
	}
}

func TestRunAtomic_RejectsInvalidRepositoryBeforeCreatingGraph(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "source.go")
	if err := os.WriteFile(regular, []byte("package source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "missing-repository")
	for i, invalidRoot := range []string{missing, regular} {
		dbPath := filepath.Join(root, fmt.Sprintf("invalid-%d.db", i))
		if _, err := RunAtomic(dbPath, invalidRoot); err == nil {
			t.Fatalf("RunAtomic(%q) unexpectedly succeeded", invalidRoot)
		}
		for _, suffix := range []string{"", BuildingSuffix, ".lock"} {
			if _, err := os.Stat(dbPath + suffix); !os.IsNotExist(err) {
				t.Fatalf("invalid root created graph artifact %q: stat err=%v", dbPath+suffix, err)
			}
		}
	}
}

func TestRunAtomicContext_CancelsBlockedPipelineAndReleasesLock(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte("package x\nfunc F() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "canceled.db")
	ctx, cancel := context.WithCancel(context.Background())
	reached := make(chan struct{})
	var reachedOnce sync.Once
	pipelineContextHook = func(ctx context.Context) error {
		reachedOnce.Do(func() { close(reached) })
		<-ctx.Done()
		return ctx.Err()
	}
	defer func() {
		pipelineContextHook = nil
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := RunAtomicContext(ctx, dbPath, root)
		done <- err
	}()
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("index did not reach the cancellable pipeline barrier")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled index error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled index did not terminate")
	}

	if _, err := os.Stat(dbPath + BuildingSuffix); !os.IsNotExist(err) {
		t.Fatalf("canceled build artifact remains: %v", err)
	}
	lock, err := AcquireExclusiveLock(dbPath)
	if err != nil {
		t.Fatalf("index lock remained held after cancellation: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverIndex_AcquiresExclusiveLockBeforeCheckingRecoveryState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	holder, err := AcquireExclusiveLock(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecoverIndex(dbPath); !errors.Is(err, ErrIndexLocked) {
		_ = holder.Release()
		t.Fatalf("recovery while writer lock is held = %v, want ErrIndexLocked", err)
	}
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}
	if err := RecoverIndex(dbPath); err != nil {
		t.Fatalf("recovery after writer release: %v", err)
	}
}

func TestRunAndRunAtomic_CanonicalizeSymlinkRootIdentity(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "physical-repo")
	aliasRoot := filepath.Join(base, "repo-alias")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "x.go"), []byte("package x\nfunc F() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	canonicalRoot, err := CanonicalPath(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonicalProject := ProjectName(canonicalRoot)
	aliasProject := ProjectName(filepath.Clean(aliasRoot))
	if filepath.Clean(realRoot) == filepath.Clean(aliasRoot) {
		t.Fatal("test roots unexpectedly share their lexical spelling")
	}

	atomicDB := filepath.Join(base, "atomic.db")
	if first, err := RunAtomic(atomicDB, realRoot); err != nil {
		t.Fatalf("initial RunAtomic: %v", err)
	} else if first.Project != canonicalProject {
		t.Fatalf("initial RunAtomic project=%q, want canonical %q", first.Project, canonicalProject)
	}
	second, err := RunAtomic(atomicDB, aliasRoot)
	if err != nil {
		t.Fatalf("symlink-root RunAtomic: %v", err)
	}
	if !second.Reused || second.Project != canonicalProject {
		t.Fatalf("symlink-root RunAtomic result=%+v, want reused canonical project %q", second, canonicalProject)
	}
	atomicStore, err := graph.Open(atomicDB)
	if err != nil {
		t.Fatal(err)
	}
	atomicNodes, _, err := atomicStore.Stats(canonicalProject)
	if err != nil {
		_ = atomicStore.Close()
		t.Fatal(err)
	}
	aliasNodes, _, err := atomicStore.Stats(aliasProject)
	_ = atomicStore.Close()
	if err != nil {
		t.Fatal(err)
	}
	if atomicNodes == 0 || aliasNodes != atomicNodes {
		t.Fatalf("RunAtomic project rows canonical=%d alias=%d, want one shared canonical project", atomicNodes, aliasNodes)
	}

	runDB := filepath.Join(base, "run.db")
	runStore, err := graph.Open(runDB)
	if err != nil {
		t.Fatal(err)
	}
	defer runStore.Close()
	if first, err := Run(runStore, realRoot); err != nil {
		t.Fatalf("initial Run: %v", err)
	} else if first.Project != canonicalProject {
		t.Fatalf("initial Run project=%q, want canonical %q", first.Project, canonicalProject)
	}
	secondRun, err := Run(runStore, aliasRoot)
	if err != nil {
		t.Fatalf("symlink-root Run: %v", err)
	}
	if !secondRun.Reused || secondRun.Project != canonicalProject {
		t.Fatalf("symlink-root Run result=%+v, want reused canonical project %q", secondRun, canonicalProject)
	}
	runNodes, _, err := runStore.Stats(canonicalProject)
	if err != nil {
		t.Fatal(err)
	}
	runAliasNodes, _, err := runStore.Stats(aliasProject)
	if err != nil {
		t.Fatal(err)
	}
	if runNodes == 0 || runAliasNodes != runNodes {
		t.Fatalf("Run project rows canonical=%d alias=%d, want one shared canonical project", runNodes, runAliasNodes)
	}
}

func TestRunAtomic_PreservesGraphOnReplacementFailure(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "x.go")
	if err := os.WriteFile(source, []byte("package x\nfunc oldSymbol() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "g.db")
	if _, err := RunAtomic(dbPath, dir); err != nil {
		t.Fatalf("first index: %v", err)
	}
	project := ProjectName(dir)

	if err := os.WriteFile(source, []byte("package x\nfunc newSymbol() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	replaceBuiltIndexErr = errors.New("injected replacement failure")
	defer func() { replaceBuiltIndexErr = nil }()
	if _, err := RunAtomic(dbPath, dir); err == nil {
		t.Fatal("expected replacement failure")
	}

	for _, suffix := range []string{BuildingSuffix, BuildingSuffix + "-wal", BuildingSuffix + "-shm"} {
		if _, err := os.Stat(dbPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("failed build artifact %q remains, stat err=%v", suffix, err)
		}
	}
	st, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n, _, err := st.Stats(project)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("replacement failure left no readable old graph")
	}
	oldHits, err := st.Search(project, "oldSymbol", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldHits) != 1 {
		t.Fatalf("old graph is not queryable after replacement failure: hits=%d", len(oldHits))
	}
	newHits, err := st.Search(project, "newSymbol", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(newHits) != 0 {
		t.Fatalf("failed replacement leaked new graph data: hits=%d", len(newHits))
	}
}

func TestRunAtomic_RejectsConcurrentSameDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\nfunc F() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "g.db")
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBuild := func() { releaseOnce.Do(func() { close(release) }) }
	pipelinePreflightHook = func() {
		close(started)
		<-release
	}
	defer func() {
		releaseBuild()
		pipelinePreflightHook = nil
	}()

	type outcome struct {
		result Result
		err    error
	}
	firstDone := make(chan outcome, 1)
	go func() {
		res, err := RunAtomic(dbPath, dir)
		firstDone <- outcome{result: res, err: err}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		releaseBuild()
		t.Fatal("first index did not reach the deterministic build barrier")
	}

	if _, err := RunAtomic(dbPath, dir); !errors.Is(err, ErrIndexLocked) {
		t.Fatalf("second index error = %v, want ErrIndexLocked", err)
	}
	releaseBuild()
	select {
	case first := <-firstDone:
		if first.err != nil {
			t.Fatalf("first index failed: %v", first.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("first index did not finish after lock release")
	}
}

func TestRunAtomic_RefusesReplacementWhileReaderHoldsWAL(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "x.go")
	if err := os.WriteFile(source, []byte("package x\nfunc oldSymbol() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "g.db")
	if _, err := RunAtomic(dbPath, dir); err != nil {
		t.Fatalf("first index: %v", err)
	}
	project := ProjectName(dir)

	reader, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := reader.BeginReadSnapshot(); err != nil {
		t.Fatal(err)
	}

	writer, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.InsertNodes([]graph.Node{{
		Project: project, Label: graph.LabelFunction, Name: "WALMarker",
		QualifiedName: project + ":x.go.WALMarker", FilePath: "x.go",
	}}); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath + "-wal"); err != nil {
		t.Fatalf("reader-held WAL fixture missing: %v", err)
	}

	if err := os.WriteFile(source, []byte("package x\nfunc newSymbol() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunAtomic(dbPath, dir); err == nil {
		t.Fatal("replacement unexpectedly succeeded with an active reader-held WAL")
	}

	check, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	oldHits, oldErr := check.Search(project, "oldSymbol", "", 5)
	newHits, newErr := check.Search(project, "newSymbol", "", 5)
	_ = check.Close()
	if oldErr != nil || newErr != nil {
		t.Fatalf("old graph query after refused replacement: old=%v new=%v", oldErr, newErr)
	}
	if len(oldHits) != 1 || len(newHits) != 0 {
		t.Fatalf("refused replacement changed graph: old hits=%d new hits=%d", len(oldHits), len(newHits))
	}

	if err := reader.EndReadSnapshot(); err != nil {
		t.Fatal(err)
	}
	if _, err := RunAtomic(dbPath, dir); err != nil {
		t.Fatalf("replacement after reader release: %v", err)
	}
}

func TestIndexLock_CrossProcess(t *testing.T) {
	if os.Getenv("CODEGRAPH_LOCK_CHILD") == "1" {
		dbPath := os.Getenv("CODEGRAPH_LOCK_DB")
		var lock *Lock
		var err error
		if os.Getenv("CODEGRAPH_LOCK_MODE") == "shared" {
			lock, err = AcquireSharedLock(dbPath)
		} else {
			lock, err = AcquireExclusiveLock(dbPath)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, "locked")
		_, _ = io.ReadAll(os.Stdin)
		// Deliberately exit without Release: the OS must release the file lock
		// when the helper process exits.
		_ = lock
		os.Exit(0)
	}

	cases := []struct {
		name         string
		childShared  bool
		parentShared bool
	}{
		{name: "shared_reader_blocks_writer", childShared: true},
		{name: "writer_blocks_shared_reader", parentShared: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "cross-process.db")
			// #nosec G204,G702 -- test-only self-exec: re-runs this test binary with a
			// fixed -test.run filter (os.Args[0] is the test binary itself), no
			// user input, no shell.
			cmd := exec.Command(os.Args[0], "-test.run=^TestIndexLock_CrossProcess$")
			mode := "exclusive"
			if tc.childShared {
				mode = "shared"
			}
			cmd.Env = append(os.Environ(),
				"CODEGRAPH_LOCK_CHILD=1",
				"CODEGRAPH_LOCK_DB="+dbPath,
				"CODEGRAPH_LOCK_MODE="+mode,
			)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			ready := make(chan error, 1)
			go func() {
				line, err := bufio.NewReader(stdout).ReadString('\n')
				if err == nil && line != "locked\n" {
					err = fmt.Errorf("unexpected child handshake %q", line)
				}
				ready <- err
			}()
			select {
			case err := <-ready:
				if err != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatalf("child lock handshake: %v", err)
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("child lock handshake timed out")
			}

			var parent *Lock
			if tc.parentShared {
				parent, err = AcquireSharedLock(dbPath)
			} else {
				parent, err = AcquireExclusiveLock(dbPath)
			}
			if !errors.Is(err, ErrIndexLocked) {
				t.Fatalf("conflicting parent lock error = %v, want ErrIndexLocked", err)
			}
			if parent != nil {
				_ = parent.Release()
			}

			if err := stdin.Close(); err != nil {
				t.Fatal(err)
			}
			wait := make(chan error, 1)
			go func() { wait <- cmd.Wait() }()
			select {
			case err := <-wait:
				if err != nil {
					t.Fatalf("child exit: %v", err)
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("child did not exit after stdin close")
			}

			if tc.parentShared {
				parent, err = AcquireSharedLock(dbPath)
			} else {
				parent, err = AcquireExclusiveLock(dbPath)
			}
			if err != nil {
				t.Fatalf("lock after child exit: %v", err)
			}
			if err := parent.Release(); err != nil {
				t.Fatalf("release parent lock: %v", err)
			}
		})
	}
}

func TestIndexLock_RelativeAndAbsoluteSpellingsShareIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "relative-absolute.db")
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativePath, err := filepath.Rel(workingDir, dbPath)
	if err != nil {
		t.Fatal(err)
	}

	shared, err := AcquireSharedLock(relativePath)
	if err != nil {
		t.Fatalf("shared lock from relative path: %v", err)
	}
	canonicalDB, err := normalizeIndexPath(dbPath)
	if err != nil {
		_ = shared.Release()
		t.Fatal(err)
	}
	if got, want := filepath.Clean(shared.file.Name()), filepath.Clean(canonicalDB+lockSuffix); got != want {
		_ = shared.Release()
		t.Fatalf("relative spelling opened lock %q, want canonical lock %q", got, want)
	}

	if _, err := AcquireExclusiveLock(dbPath); !errors.Is(err, ErrIndexLocked) {
		_ = shared.Release()
		t.Fatalf("absolute exclusive lock error = %v, want ErrIndexLocked", err)
	}
	if err := shared.Release(); err != nil {
		t.Fatal(err)
	}

	exclusive, err := AcquireExclusiveLock(relativePath)
	if err != nil {
		t.Fatalf("exclusive lock from relative path after release: %v", err)
	}
	if _, err := AcquireSharedLock(dbPath); !errors.Is(err, ErrIndexLocked) {
		_ = exclusive.Release()
		t.Fatalf("absolute shared lock error = %v, want ErrIndexLocked", err)
	}
	if err := exclusive.Release(); err != nil {
		t.Fatal(err)
	}

	if lock, err := AcquireSharedLock(dbPath); err != nil {
		t.Fatalf("lock was not released: %v", err)
	} else if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}
