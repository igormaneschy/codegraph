package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/index"
	"github.com/Lordymine/codegraph/internal/query"
)

// TestMain gates the two-real-processes helper: with CODEGRAPH_TEST_CHILD set
// the test binary acts as a bare writer attempt instead of running tests.
func TestMain(m *testing.M) {
	if os.Getenv("CODEGRAPH_TEST_CHILD") != "" {
		runP1Child()
		return
	}
	os.Exit(m.Run())
}

func runP1Child() {
	if os.Getenv("CODEGRAPH_TEST_CHILD") != "tryindex" {
		fmt.Fprintln(os.Stderr, "unknown child mode")
		os.Exit(2)
	}
	res, err := index.RunAtomic(os.Getenv("CODEGRAPH_TEST_DB"), os.Getenv("CODEGRAPH_TEST_ROOT"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
	fmt.Printf("indexed files=%d reused=%v\n", res.Files, res.Reused)
	os.Exit(0)
}

// writeP1Repo creates a tiny Go-only repo (no tsconfig, so no scip): fast to
// index, deterministic, and free of toolchain downloads.
func writeP1Repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module p1.test\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func openP1Session(t *testing.T, root string) (*mcpSession, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	eng := query.NewEngine(store, index.ProjectName(root), root)
	sess := newMCPSession(eng, dbPath, root, index.ProjectName(root), context.Background(), mcpIndexHooks{}, nil)
	t.Cleanup(func() { _ = sess.close() })
	return sess, dbPath
}

func sessionGeneration(t *testing.T, sess *mcpSession) string {
	t.Helper()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.generation
}

// TestSession_SecondEditRefreshWithoutRestart is the core P1 acceptance: edit
// and update with no restart. Initial round → ready gen1; edit; refresh →
// ready gen2 with the new symbol queryable through the guarded engine.
func TestSession_SecondEditRefreshWithoutRestart(t *testing.T) {
	root := writeP1Repo(t, map[string]string{"x.go": "package x\nfunc Before() int { return 1 }\n"})
	sess, _ := openP1Session(t, root)

	sess.startInitial()
	if !sess.waitRound(30 * time.Second) {
		t.Fatal("initial round did not finish")
	}
	if ok, msg := sess.gate(); !ok {
		t.Fatalf("initial gate ok=false: %q", msg)
	}
	gen1 := sessionGeneration(t, sess)
	if gen1 == "" {
		t.Fatal("initial round served an empty generation")
	}

	if err := os.WriteFile(filepath.Join(root, "y.go"), []byte("package x\nfunc After() int { return 2 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := sess.refreshAsync(); !strings.Contains(got, "refresh started") {
		t.Fatalf("refresh did not start: %q", got)
	}
	if !sess.waitRound(30 * time.Second) {
		t.Fatal("refresh round did not finish")
	}
	if ok, msg := sess.gate(); !ok {
		t.Fatalf("post-refresh gate ok=false: %q", msg)
	}
	gen2 := sessionGeneration(t, sess)
	if gen2 == "" || gen2 == gen1 {
		t.Fatalf("refresh did not publish a new generation: before=%q after=%q", gen1, gen2)
	}
	page, err := sess.engine().SearchPage("After", "", 5, "")
	if err != nil {
		t.Fatalf("query after refresh: %v", err)
	}
	found := false
	for _, r := range page.Refs {
		if strings.Contains(r.QualifiedName, "After") {
			found = true
		}
	}
	if !found {
		t.Fatalf("new symbol not queryable after refresh without restart: %+v", page.Refs)
	}
	// Similarity coverage rides the manifest: status shows it without
	// re-running the pass (here complete — a tiny fixture under budget).
	if status := sess.statusText(); !strings.Contains(status, "similar=complete") {
		t.Errorf("status must report similarity coverage, got:\n%s", status)
	}
}

// TestSession_TwoSessionsConverge proves two cooperating sessions observe the
// winner's commit without restarting: A refreshes after an edit, B's status
// reports the lag, B's refresh converges to A's generation.
func TestSession_TwoSessionsConverge(t *testing.T) {
	root := writeP1Repo(t, map[string]string{"x.go": "package x\nfunc Before() int { return 1 }\n"})
	sessA, dbPath := openP1Session(t, root)
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	engB := query.NewEngine(store, index.ProjectName(root), root)
	sessB := newMCPSession(engB, dbPath, root, index.ProjectName(root), context.Background(), mcpIndexHooks{}, nil)
	t.Cleanup(func() { _ = sessB.close() })

	for _, s := range []*mcpSession{sessA, sessB} {
		s.startInitial()
		if !s.waitRound(30 * time.Second) {
			t.Fatal("initial round did not finish")
		}
	}
	gen1 := sessionGeneration(t, sessA)
	if gen1 == "" || sessionGeneration(t, sessB) != gen1 {
		t.Fatalf("sessions did not start converged: A=%q B=%q", gen1, sessionGeneration(t, sessB))
	}

	if err := os.WriteFile(filepath.Join(root, "y.go"), []byte("package x\nfunc After() int { return 2 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessA.refreshAsync()
	if !sessA.waitRound(30 * time.Second) {
		t.Fatal("A refresh did not finish")
	}
	gen2 := sessionGeneration(t, sessA)
	if gen2 == gen1 {
		t.Fatal("A refresh did not publish a new generation")
	}
	statusB := sessB.statusText()
	if !strings.Contains(statusB, "lag=") {
		t.Fatalf("B status does not report the newer disk generation:\n%s", statusB)
	}
	oldPage, err := sessB.engine().SearchPage("Before", "", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if oldPage.Generation != shortDigest(gen1) {
		t.Fatalf("stale session labeled its old graph as generation %q, want %q", oldPage.Generation, shortDigest(gen1))
	}
	sessB.refreshAsync()
	if !sessB.waitRound(30 * time.Second) {
		t.Fatal("B refresh did not finish")
	}
	if got := sessionGeneration(t, sessB); got != gen2 {
		t.Fatalf("B did not converge without restart: B=%q A=%q", got, gen2)
	}
	newPage, err := sessB.engine().SearchPage("After", "", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if newPage.Generation != shortDigest(gen2) {
		t.Fatalf("reopened session page generation=%q, want %q", newPage.Generation, shortDigest(gen2))
	}
}

// TestSession_NeverTwoWriters holds the exclusive lock while a refresh runs:
// the round must fail fast with an actionable message (never block, never a
// second writer), keep serving the old graph, and succeed after release.
func TestSession_NeverTwoWriters(t *testing.T) {
	root := writeP1Repo(t, map[string]string{"x.go": "package x\nfunc Before() int { return 1 }\n"})
	sess, dbPath := openP1Session(t, root)
	sess.startInitial()
	if !sess.waitRound(30 * time.Second) {
		t.Fatal("initial round did not finish")
	}
	gen1 := sessionGeneration(t, sess)

	holder, err := index.AcquireExclusiveLock(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "y.go"), []byte("package x\nfunc After() int { return 2 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	sess.refreshAsync()
	if !sess.waitRound(30 * time.Second) {
		t.Fatal("contended refresh did not finish")
	}
	// Fail-fast: a lone contender must never stall the session.
	if elapsed := time.Since(start); elapsed > 25*time.Second {
		t.Fatalf("contended refresh blocked %v", elapsed)
	}
	ok, notice := sess.gate()
	if !ok {
		t.Fatalf("session stopped serving during writer contention: %q", notice)
	}
	if !strings.Contains(notice, "index already in progress") {
		t.Fatalf("contention is not actionable: %q", notice)
	}
	if got := sessionGeneration(t, sess); got != gen1 {
		t.Fatalf("failed round changed the served generation: %q -> %q", gen1, got)
	}
	if _, err := sess.engine().SearchPage("Before", "", 1, ""); err != nil {
		t.Fatalf("old graph not queryable during contention: %v", err)
	}
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}
	sess.refreshAsync()
	if !sess.waitRound(30 * time.Second) {
		t.Fatal("refresh after release did not finish")
	}
	if ok, msg := sess.gate(); !ok {
		t.Fatalf("gate ok=false after release: %q", msg)
	}
	if got := sessionGeneration(t, sess); got == gen1 {
		t.Fatal("refresh after release did not publish a new generation")
	}
}

// TestSession_RefreshDedup blocks the build at a deterministic barrier and
// fires two refreshes: the second reports in-progress and the build runs once.
func TestSession_RefreshDedup(t *testing.T) {
	root := writeP1Repo(t, map[string]string{"x.go": "package x\nfunc Before() int { return 1 }\n"})
	sess, _ := openP1Session(t, root)
	sess.startInitial()
	if !sess.waitRound(30 * time.Second) {
		t.Fatal("initial round did not finish")
	}

	release := make(chan struct{})
	entered := make(chan struct{})
	var enterOnce sync.Once
	var mu sync.Mutex
	builds := 0
	sess.hooks.beforeRunContext = func(ctx context.Context) error {
		mu.Lock()
		builds++
		mu.Unlock()
		enterOnce.Do(func() { close(entered) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := os.WriteFile(filepath.Join(root, "y.go"), []byte("package x\nfunc After() int { return 2 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := sess.refreshAsync()
	if !strings.Contains(first, "refresh started") {
		t.Fatalf("first refresh did not start: %q", first)
	}
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("refresh did not reach the build barrier")
	}
	second := sess.refreshAsync()
	if !strings.Contains(second, "already in progress") {
		t.Fatalf("second refresh was not deduped: %q", second)
	}
	close(release)
	if !sess.waitRound(30 * time.Second) {
		t.Fatal("refresh did not finish after barrier release")
	}
	mu.Lock()
	defer mu.Unlock()
	if builds != 1 {
		t.Fatalf("deduped refresh built %d times, want 1", builds)
	}
}

// TestSession_QueryNeverUsesClosedEngine closes the session and asserts every
// guarded method fails with an actionable error instead of panicking on a nil
// store.
func TestSession_QueryNeverUsesClosedEngine(t *testing.T) {
	root := writeP1Repo(t, map[string]string{"x.go": "package x\nfunc Before() int { return 1 }\n"})
	sess, _ := openP1Session(t, root)
	sess.startInitial()
	if !sess.waitRound(30 * time.Second) {
		t.Fatal("initial round did not finish")
	}
	if err := sess.close(); err != nil {
		t.Fatal(err)
	}
	eng := sess.engine()
	if _, err := eng.SearchPage("Before", "", 1, ""); err == nil || !strings.Contains(err.Error(), "not queryable") {
		t.Errorf("SearchPage on closed engine = %v, want actionable not-queryable error", err)
	}
	if _, err := eng.CallersPage("x", 1, ""); err == nil {
		t.Errorf("CallersPage on closed engine must fail")
	}
	if _, err := eng.SnippetPage("x.go", 1, 1, 0, ""); err == nil {
		t.Errorf("SnippetPage on closed engine must fail")
	}
	if _, err := eng.DetectChanges(); err == nil {
		t.Errorf("DetectChanges on closed engine must fail")
	}
}

// An admitted query must finish before Close can discard its store. The barrier
// pauses SearchPage after admission, making the refresh/close interleaving exact.
func TestSession_CloseWaitsForInFlightQuery(t *testing.T) {
	root := writeP1Repo(t, map[string]string{"x.go": "package x\nfunc Before() int { return 1 }\n"})
	sess, _ := openP1Session(t, root)
	sess.startInitial()
	if !sess.waitRound(30 * time.Second) {
		t.Fatal("initial round did not finish")
	}

	queryEntered := make(chan struct{})
	releaseQuery := make(chan struct{})
	closedEarly := make(chan struct{})
	guard := sessionEngine{s: sess, beforeSearchPage: func() error {
		close(queryEntered)
		<-releaseQuery
		select {
		case <-closedEarly:
			return fmt.Errorf("session closed before the admitted query ran")
		default:
			return nil
		}
	}}
	queryDone := make(chan error, 1)
	go func() {
		_, err := guard.SearchPage("Before", "", 1, "")
		queryDone <- err
	}()
	select {
	case <-queryEntered:
	case <-time.After(5 * time.Second):
		close(releaseQuery)
		t.Fatal("query did not reach the admission barrier")
	}

	closeStarted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeStarted)
		closeDone <- sess.close()
	}()
	<-closeStarted
	select {
	case err := <-closeDone:
		close(closedEarly)
		close(releaseQuery)
		<-queryDone
		t.Fatalf("Close returned while an admitted query was in flight: %v", err)
	case <-time.After(250 * time.Millisecond):
		close(releaseQuery)
	}
	if err := <-queryDone; err != nil {
		t.Fatalf("admitted query failed: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close after query: %v", err)
	}
}

// runP1ChildProcess spawns the current test binary as a real second process
// attempting a writer round on dbPath/root.
func runP1ChildProcess(t *testing.T, dbPath, root string) (string, string, int) {
	t.Helper()
	// #nosec G204 G702 -- the child executable is this test binary, never user input.
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"CODEGRAPH_TEST_CHILD=tryindex",
		"CODEGRAPH_TEST_DB="+dbPath,
		"CODEGRAPH_TEST_ROOT="+root,
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	rc := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			rc = exit.ExitCode()
		} else {
			t.Fatalf("spawn child: %v", err)
		}
	}
	return stdout.String(), stderr.String(), rc
}

// TestTwoProcesses_WriterVsLiveSession: a live session holds no lock while
// idle, so a real external writer proceeds; the session then reports the lag
// in status and converges via refresh — no restart on either side.
func TestTwoProcesses_WriterVsLiveSession(t *testing.T) {
	root := writeP1Repo(t, map[string]string{"x.go": "package x\nfunc Before() int { return 1 }\n"})
	sess, dbPath := openP1Session(t, root)
	sess.startInitial()
	if !sess.waitRound(60 * time.Second) {
		t.Fatal("initial round did not finish")
	}
	gen1 := sessionGeneration(t, sess)

	if err := os.WriteFile(filepath.Join(root, "y.go"), []byte("package x\nfunc After() int { return 2 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, rc := runP1ChildProcess(t, dbPath, root)
	if rc != 0 {
		t.Fatalf("external writer failed against a live session (rc=%d): stdout=%q stderr=%q", rc, stdout, stderr)
	}
	if status := sess.statusText(); !strings.Contains(status, "lag=") {
		t.Fatalf("live session does not report the external commit:\n%s", status)
	}
	sess.refreshAsync()
	if !sess.waitRound(60 * time.Second) {
		t.Fatal("refresh after external commit did not finish")
	}
	if got := sessionGeneration(t, sess); got == "" || got == gen1 {
		t.Fatalf("live session did not converge to the external commit: %q (was %q)", got, gen1)
	}
	if _, err := sess.engine().SearchPage("After", "", 1, ""); err != nil {
		t.Fatalf("externally committed symbol not queryable: %v", err)
	}
}

// TestTwoProcesses_WriterHoldsOverTwoSeconds reproduces sustained contention:
// while one process owns the exclusive lock for >2s, a second real writer
// attempt fails fast with an actionable message (never blocks, never a second
// writer); after release the writer succeeds and leaves no permanent block.
func TestTwoProcesses_WriterHoldsOverTwoSeconds(t *testing.T) {
	root := writeP1Repo(t, map[string]string{"x.go": "package x\nfunc Before() int { return 1 }\n"})
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := index.RunAtomic(dbPath, root); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	holder, err := index.AcquireExclusiveLock(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		_ = holder.Release()
	})

	contended := make(chan struct {
		stdout, stderr string
		rc             int
		elapsed        time.Duration
	}, 1)
	go func() {
		start := time.Now()
		stdout, stderr, rc := runP1ChildProcess(t, dbPath, root)
		contended <- struct {
			stdout, stderr string
			rc             int
			elapsed        time.Duration
		}{stdout, stderr, rc, time.Since(start)}
	}()

	// Hold the contention past two seconds while the child attempt is known to
	// be in flight (it fails fast, so bound the hold, then release and rerun).
	time.Sleep(2500 * time.Millisecond)
	releaseOnce.Do(func() { close(release) })
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}
	holder = nil

	var first struct {
		stdout, stderr string
		rc             int
		elapsed        time.Duration
	}
	select {
	case first = <-contended:
	case <-time.After(60 * time.Second):
		t.Fatal("contended child writer did not return")
	}
	if first.rc == 0 {
		t.Fatalf("second writer succeeded during held exclusive lock: %q", first.stdout)
	}
	if !strings.Contains(first.stderr, "index already in progress") {
		t.Fatalf("contention is not actionable: rc=%d stdout=%q stderr=%q", first.rc, first.stdout, first.stderr)
	}

	stdout, stderr, rc := runP1ChildProcess(t, dbPath, root)
	if rc != 0 {
		t.Fatalf("writer failed after release (permanent block): rc=%d stdout=%q stderr=%q", rc, stdout, stderr)
	}
}

// TestSession_PublishReportsServedGeneration pins R11: the generation and
// similarity coverage reported by status belong to the graph the engine serves,
// not to a disk manifest another writer installed between reopen and publish. The
// divergence is reported as lag, never as a status that disagrees with the page.
func TestSession_PublishReportsServedGeneration(t *testing.T) {
	root := writeP1Repo(t, map[string]string{"x.go": "package x\nfunc Before() int { return 1 }\n"})
	sess, dbPath := openP1Session(t, root)

	sess.startInitial()
	if !sess.waitRound(30 * time.Second) {
		t.Fatal("initial round did not finish")
	}

	if err := os.WriteFile(filepath.Join(root, "y.go"), []byte("package x\nfunc After() int { return 2 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Between reopen and publish of the refresh round, an external writer commits
	// a newer generation (with an extra file) — the exact race R11 covers.
	var once sync.Once
	sess.hooks.afterReopen = func() {
		once.Do(func() {
			if err := os.WriteFile(filepath.Join(root, "z.go"), []byte("package x\nfunc Later() int { return 3 }\n"), 0o600); err != nil {
				t.Error(err)
				return
			}
			if _, err := index.RunAtomic(dbPath, root); err != nil {
				t.Errorf("external writer: %v", err)
			}
		})
	}
	if got := sess.refreshAsync(); !strings.Contains(got, "refresh started") {
		t.Fatalf("refresh did not start: %q", got)
	}
	if !sess.waitRound(60 * time.Second) {
		t.Fatal("refresh round did not finish")
	}
	served := sessionGeneration(t, sess)
	if served == "" {
		t.Fatal("round published an empty generation")
	}

	page, err := sess.engine().SearchPage("After", "", 5, "")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if page.Generation != shortDigest(served) {
		t.Fatalf("page generation=%q, status served=%q", page.Generation, shortDigest(served))
	}
	status := sess.statusText()
	if !strings.Contains(status, "generation="+shortDigest(served)) {
		t.Fatalf("status generation disagrees with the served graph:\n%s", status)
	}
	if !strings.Contains(status, "lag=disk holds newer generation") {
		t.Fatalf("status must report the external writer's newer generation as lag:\n%s", status)
	}
}
