package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/index"
	"github.com/Lordymine/codegraph/internal/query"
)

func TestCachePath_DigestSeparatesCollidingProjectSlugs(t *testing.T) {
	base := t.TempDir()
	rootA := filepath.Join(base, "left", "right")
	rootB := filepath.Join(base, "left-right")
	if err := os.MkdirAll(rootA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rootB, 0o755); err != nil {
		t.Fatal(err)
	}
	if index.ProjectName(rootA) != index.ProjectName(rootB) {
		t.Fatalf("test roots do not collide under the human-readable slug: %q vs %q", index.ProjectName(rootA), index.ProjectName(rootB))
	}

	cacheRoot := t.TempDir()
	pathA, err := cachePath(cacheRoot, rootA)
	if err != nil {
		t.Fatal(err)
	}
	pathB, err := cachePath(cacheRoot, rootB)
	if err != nil {
		t.Fatal(err)
	}
	if pathA == pathB {
		t.Fatalf("colliding project slugs share cache path %q", pathA)
	}
	pathAAgain, err := cachePath(cacheRoot, rootA)
	if err != nil {
		t.Fatal(err)
	}
	if pathAAgain != pathA {
		t.Fatalf("cache identity is not stable: first=%q again=%q", pathA, pathAAgain)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(cacheRoot, "codegraph"))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("cache directory mode=%o, want 700", got)
		}
	}

	storeA, err := graph.Open(pathA)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	projectA := index.ProjectName(rootA)
	if err := storeA.InsertNodes([]graph.Node{{
		Project: projectA, Label: graph.LabelFunction, Name: "A", QualifiedName: projectA + ":a.go.A",
	}}); err != nil {
		t.Fatal(err)
	}
	storeB, err := graph.Open(pathB)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()
	projectB := index.ProjectName(rootB)
	if err := storeB.InsertNodes([]graph.Node{{
		Project: projectB, Label: graph.LabelFunction, Name: "B", QualifiedName: projectB + ":b.go.B",
	}}); err != nil {
		t.Fatal(err)
	}
	nA, _, err := storeA.Stats(projectA)
	if err != nil {
		t.Fatal(err)
	}
	nB, _, err := storeB.Stats(projectB)
	if err != nil {
		t.Fatal(err)
	}
	if nA != 1 || nB != 1 {
		t.Fatalf("independent cache stores have counts A=%d B=%d, want 1/1", nA, nB)
	}
}

func TestCachePath_SymlinkAliasSharesCanonicalIdentity(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "physical-repo")
	aliasRoot := filepath.Join(base, "repo-alias")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	cacheRoot := t.TempDir()
	physicalPath, err := cachePath(cacheRoot, realRoot)
	if err != nil {
		t.Fatal(err)
	}
	aliasPath, err := cachePath(cacheRoot, aliasRoot)
	if err != nil {
		t.Fatal(err)
	}
	if physicalPath != aliasPath {
		t.Fatalf("symlink alias received a distinct cache path: physical=%q alias=%q", physicalPath, aliasPath)
	}
	if filepath.Clean(realRoot) == filepath.Clean(aliasRoot) {
		t.Fatal("test paths unexpectedly share their lexical spelling")
	}
	physicalCanonical, err := absoluteRepoRoot(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := absoluteRepoRoot(aliasRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := index.ProjectName(canonical), index.ProjectName(physicalCanonical); got != want {
		t.Fatalf("canonical project name=%q, physical project name=%q", got, want)
	}
}

func TestCachePath_CaseAliasSharesIdentityOnCaseInsensitiveFilesystem(t *testing.T) {
	base := t.TempDir()
	actualRoot := filepath.Join(base, "CaseRepository")
	aliasRoot := filepath.Join(base, "caserepository")
	if err := os.Mkdir(actualRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	actualInfo, err := os.Stat(actualRoot)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(aliasRoot)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("filesystem is case-sensitive; no case alias exists")
		}
		t.Fatal(err)
	}
	if !os.SameFile(actualInfo, aliasInfo) {
		t.Fatalf("case alias is not proven to name the same directory: actual=%q alias=%q", actualRoot, aliasRoot)
	}

	cacheRoot := t.TempDir()
	actualPath, err := cachePath(cacheRoot, actualRoot)
	if err != nil {
		t.Fatal(err)
	}
	aliasPath, err := cachePath(cacheRoot, aliasRoot)
	if err != nil {
		t.Fatal(err)
	}
	if actualPath != aliasPath {
		t.Fatalf("case alias received a distinct cache path: actual=%q alias=%q", actualPath, aliasPath)
	}
}

func TestCachePath_DifferentCaseDirectoriesRemainDistinctOnCaseSensitiveFilesystem(t *testing.T) {
	base := t.TempDir()
	upperRoot := filepath.Join(base, "CaseRepository")
	lowerRoot := filepath.Join(base, "caserepository")
	if err := os.Mkdir(upperRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lowerRoot, 0o755); err != nil {
		if os.IsExist(err) {
			t.Skip("filesystem is case-insensitive; differently cased entries cannot be distinct")
		}
		t.Fatal(err)
	}
	upperInfo, err := os.Stat(upperRoot)
	if err != nil {
		t.Fatal(err)
	}
	lowerInfo, err := os.Stat(lowerRoot)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(upperInfo, lowerInfo) {
		t.Fatalf("differently cased directories unexpectedly name one physical directory: %q and %q", upperRoot, lowerRoot)
	}

	cacheRoot := t.TempDir()
	upperPath, err := cachePath(cacheRoot, upperRoot)
	if err != nil {
		t.Fatal(err)
	}
	lowerPath, err := cachePath(cacheRoot, lowerRoot)
	if err != nil {
		t.Fatal(err)
	}
	if upperPath == lowerPath {
		t.Fatalf("distinct case-sensitive repositories share cache path %q", upperPath)
	}
}

func TestOpenForWithRetry_WaitsForHeldWriterLock(t *testing.T) {
	root := t.TempDir()
	dbPath, err := cachePath(mustUserCacheDir(t), root)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := index.AcquireExclusiveLock(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	var lockedOnce, releaseOnce sync.Once
	mcpStartupRetryHook = func() {
		lockedOnce.Do(func() { close(locked) })
		<-release
	}
	t.Cleanup(func() {
		mcpStartupRetryHook = nil
		releaseOnce.Do(func() { close(release) })
		_ = holder.Release()
	})

	type result struct {
		store *graph.Store
		lock  *index.Lock
		err   error
	}
	done := make(chan result, 1)
	go func() {
		store, _, lock, openErr := openForWithRetry(root)
		done <- result{store: store, lock: lock, err: openErr}
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("startup retry did not observe the held writer lock")
	}
	releaseOnce.Do(func() { close(release) })
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("startup retry failed after writer release: %v", got.err)
		}
		if got.store == nil || got.lock == nil {
			t.Fatal("startup retry returned without a readable store and lock")
		}
		if err := got.lock.Release(); err != nil {
			t.Fatal(err)
		}
		if err := got.store.Close(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup retry did not become ready after writer release")
	}
}

func TestServeMCP_StdinCloseCancelsBlockedIndexing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte("package x\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, inputWriter := io.Pipe()
	reached := make(chan struct{})
	var reachedOnce sync.Once
	hooks := mcpIndexHooks{
		beforeRunContext: func(ctx context.Context) error {
			reachedOnce.Do(func() { close(reached) })
			<-ctx.Done()
			return ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- serveMCPWithHooks(root, input, io.Discard, hooks)
	}()
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		_ = inputWriter.Close()
		t.Fatal("MCP indexing did not reach the blocked hook")
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MCP shutdown after stdin close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("MCP shutdown remained blocked after stdin close")
	}
}

func mustUserCacheDir(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func TestMCPBackgroundIndex_ReopensAfterTransientWriterContention(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte("package x\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	readerLock, err := index.AcquireReaderLock(dbPath)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	eng := query.NewEngine(store, index.ProjectName(root), root)

	ctx, cancel := context.WithCancel(context.Background())
	var stopOnce sync.Once
	done := make(chan mcpIndexOutcome, 1)
	writerHeld := make(chan struct{})
	writerErr := make(chan error, 1)
	reopenAttempted := make(chan struct{})
	var reopenOnce sync.Once
	var writerMu sync.Mutex
	var writer *index.Lock

	releaseWriter := func() error {
		writerMu.Lock()
		lock := writer
		writer = nil
		writerMu.Unlock()
		if lock == nil {
			return nil
		}
		return lock.Release()
	}
	var outcome mcpIndexOutcome
	var outcomeReady bool
	t.Cleanup(func() {
		stopOnce.Do(cancel)
		_ = releaseWriter()
		if !outcomeReady {
			select {
			case outcome = <-done:
				outcomeReady = true
			case <-time.After(5 * time.Second):
			}
		}
		if outcomeReady && outcome.readerLock != nil {
			_ = outcome.readerLock.Release()
		}
		_ = eng.Close()
	})

	hooks := mcpIndexHooks{
		beforeRun: func() {
			lock, lockErr := index.AcquireExclusiveLock(dbPath)
			if lockErr != nil {
				writerErr <- lockErr
				return
			}
			writerMu.Lock()
			writer = lock
			writerMu.Unlock()
			close(writerHeld)
		},
		reopenAttempt: func() {
			reopenOnce.Do(func() { close(reopenAttempted) })
		},
	}
	go func() {
		done <- runMCPBackgroundIndex(eng, readerLock, dbPath, root, index.ProjectName(root), "building", ctx, hooks)
	}()

	select {
	case <-writerHeld:
	case lockErr := <-writerErr:
		t.Fatalf("background test writer lock: %v", lockErr)
	case <-time.After(5 * time.Second):
		t.Fatal("background index did not reach the deterministic writer-lock barrier")
	}
	select {
	case <-reopenAttempted:
	case <-time.After(5 * time.Second):
		t.Fatal("background index did not attempt recovery after ErrIndexLocked")
	}
	if err := releaseWriter(); err != nil {
		t.Fatal(err)
	}

	select {
	case outcome = <-done:
		outcomeReady = true
	case <-time.After(5 * time.Second):
		t.Fatal("background index did not become ready after writer release")
	}
	if !outcome.ready || outcome.readerLock == nil {
		t.Fatalf("background index outcome ready=%v readerLock=%v status=%q", outcome.ready, outcome.readerLock != nil, outcome.status)
	}
	if !strings.Contains(outcome.status, "failed: index already in progress") {
		t.Fatalf("lock contention status was not preserved: %q", outcome.status)
	}
	if _, err := eng.Search("x", "", 1); err != nil {
		t.Fatalf("reopened MCP engine is not queryable: %v", err)
	}
}
