package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lordymine/codegraph/internal/index"
	"github.com/Lordymine/codegraph/internal/mcp"
	"github.com/Lordymine/codegraph/internal/query"
	"github.com/Lordymine/codegraph/internal/similar"
)

// P1 refresh protocol: per-operation locks + generation revalidation.
// See docs/ARCHITECTURE.md ("MCP refresh protocol").
//
// A session owns one query engine and a state machine. Readers never hold the
// per-database lock while idle: the shared lock is taken only around reopen
// (the commit window a reader must not straddle). Writers take the exclusive
// lock for the whole RunAtomic. One mutex serializes queries against
// close/reopen, so a query can never touch a closed engine.

type mcpSessionState string

const (
	mcpStateUpdating    mcpSessionState = "updating"
	mcpStateReady       mcpSessionState = "ready"
	mcpStateDegraded    mcpSessionState = "degraded"
	mcpStateFailed      mcpSessionState = "failed"
	mcpStateUnavailable mcpSessionState = "unavailable"
)

type mcpSession struct {
	eng *query.Engine
	mu  sync.Mutex

	state        mcpSessionState
	notice       string // served with answers (stale context) or as the rejection reason
	generation   string // manifest digest served; "" = none yet
	similar      similar.Coverage
	servedStatus index.IndexStatus
	manifestErr  error
	serving      bool // engine currently queryable
	everServed   bool // a graph was served at least once (drives unavailable vs failed)

	inFlight bool
	done     chan struct{}

	dbPath  string
	root    string
	project string
	ctx     context.Context
	hooks   mcpIndexHooks
	indexFn func(ctx context.Context, dbPath, root string) (index.Result, error)

	lastResult index.Result
	hasResult  bool
}

func newMCPSession(eng *query.Engine, dbPath, root, project string, ctx context.Context, hooks mcpIndexHooks, indexFn func(ctx context.Context, dbPath, root string) (index.Result, error)) *mcpSession {
	if ctx == nil {
		ctx = context.Background()
	}
	if indexFn == nil {
		indexFn = func(ctx context.Context, dbPath, root string) (index.Result, error) {
			return index.RunAtomicContext(ctx, dbPath, root)
		}
	}
	return &mcpSession{
		eng:     eng,
		state:   mcpStateUpdating,
		notice:  "codegraph is building the index for " + project + " (first run can take a while); retry shortly",
		dbPath:  dbPath,
		root:    root,
		project: project,
		ctx:     ctx,
		hooks:   hooks,
		indexFn: indexFn,
	}
}

// engine returns the guarded query surface for the MCP server: every method
// holds the session mutex and refuses when the graph is not queryable, so a
// query can never run against a closed engine.
func (s *mcpSession) engine() mcp.QueryEngine { return sessionEngine{s: s} }

type sessionEngine struct {
	s                *mcpSession
	beforeSearchPage func() error // test barrier between admission and the query
}

func sessionQuery[T any](g sessionEngine, run func(*query.Engine) (T, error)) (T, error) {
	g.s.mu.Lock()
	defer g.s.mu.Unlock()
	if !g.s.serving || g.s.eng == nil {
		var zero T
		return zero, fmt.Errorf("codegraph: graph is not queryable (state=%s): %s", g.s.state, strings.TrimSpace(g.s.notice))
	}
	return run(g.s.eng)
}

func (g sessionEngine) SearchPage(q, label string, limit int, cursor string) (query.RefPage, error) {
	return sessionQuery(g, func(eng *query.Engine) (query.RefPage, error) {
		if g.beforeSearchPage != nil {
			if err := g.beforeSearchPage(); err != nil {
				return query.RefPage{}, err
			}
		}
		return eng.SearchPage(q, label, limit, cursor)
	})
}

func (g sessionEngine) CallersPage(qn string, limit int, cursor string) (query.RefPage, error) {
	return sessionQuery(g, func(eng *query.Engine) (query.RefPage, error) {
		return eng.CallersPage(qn, limit, cursor)
	})
}

func (g sessionEngine) CalleesPage(qn string, limit int, cursor string) (query.RefPage, error) {
	return sessionQuery(g, func(eng *query.Engine) (query.RefPage, error) {
		return eng.CalleesPage(qn, limit, cursor)
	})
}

func (g sessionEngine) NeighborsPage(qn string, limit int, cursor string) (query.RefPage, error) {
	return sessionQuery(g, func(eng *query.Engine) (query.RefPage, error) {
		return eng.NeighborsPage(qn, limit, cursor)
	})
}

func (g sessionEngine) SimilarPage(qn string, limit int, cursor string) (query.RefPage, error) {
	return sessionQuery(g, func(eng *query.Engine) (query.RefPage, error) {
		return eng.SimilarPage(qn, limit, cursor)
	})
}

func (g sessionEngine) DeadCodePage(limit int, cursor string) (query.RefPage, error) {
	return sessionQuery(g, func(eng *query.Engine) (query.RefPage, error) {
		return eng.DeadCodePage(limit, cursor)
	})
}

func (g sessionEngine) Architecture(topN int) (query.Architecture, error) {
	return sessionQuery(g, func(eng *query.Engine) (query.Architecture, error) {
		return eng.Architecture(topN)
	})
}

func (g sessionEngine) SnippetPage(file string, start, end, limit int, cursor string) (query.SnippetPage, error) {
	return sessionQuery(g, func(eng *query.Engine) (query.SnippetPage, error) {
		return eng.SnippetPage(file, start, end, limit, cursor)
	})
}

func (g sessionEngine) DetectChanges() (index.Changes, error) {
	return sessionQuery(g, func(eng *query.Engine) (index.Changes, error) {
		return eng.DetectChanges()
	})
}

// gate is the MCP readiness function: ready/degraded/failed-with-graph serve
// (failed carries its reason as stale context); updating/unavailable and a
// failed session without a graph reject with the reason.
func (s *mcpSession) gate() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case mcpStateReady:
		return true, ""
	case mcpStateDegraded, mcpStateFailed:
		if s.serving {
			return true, s.notice
		}
		return false, s.notice
	default:
		return false, s.notice
	}
}

// shortDigest renders a generation id: 12 hex chars, or "none"/"unknown".
func shortDigest(d string) string {
	if d == "" {
		return "none"
	}
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// diskGeneration reads the committed generation without touching the engine.
func diskGeneration(dbPath string) (string, error) {
	manifest, err := index.ReadManifest(dbPath)
	if err != nil {
		return "", err
	}
	return manifest.GraphContentDigest, nil
}

// statusText reports state, served generation, last round counts, and whether
// another process committed a newer generation (orientation to call refresh).
// It never mutates the session: convergence is refresh's job, not status's.
func (s *mcpSession) statusText() string {
	diskGen, diskErr := diskGeneration(s.dbPath)
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "state=%s\ngeneration=%s\n", s.state, shortDigest(s.generation))
	if s.hasResult {
		fmt.Fprintf(&b, "files=%d nodes=%d edges=%d reused=%v\n",
			s.lastResult.Files, s.lastResult.Nodes, s.lastResult.EdgesKept, s.lastResult.Reused)
		if summary := s.lastResult.MetricsSummary(); summary != "" {
			fmt.Fprintf(&b, "%s\n", summary)
		}
	} else {
		b.WriteString("files=0 nodes=0 edges=0 reused=false\n")
	}
	detail := s.notice
	if detail == "" {
		detail = "serving generation " + shortDigest(s.generation)
	}
	fmt.Fprintf(&b, "detail=%s\n", detail)
	// Similarity coverage rides the served manifest snapshot, so it is visible
	// here without re-running the pass — and, crucially, without reading a disk
	// manifest that another writer may have already replaced (R11).
	similarLine := "similar=not recorded"
	if s.generation != "" {
		cov := s.similar
		similarLine = "similar=" + cov.Summary() +
			" docs=" + strconv.Itoa(cov.Docs) +
			" pairs=" + strconv.Itoa(cov.PairsExamined) +
			" edges=" + strconv.Itoa(cov.EdgesEmitted)
	}
	fmt.Fprintf(&b, "%s\n", similarLine)
	if diskErr == nil && diskGen != "" && s.generation != "" && diskGen != s.generation && !s.inFlight {
		fmt.Fprintf(&b, "lag=disk holds newer generation %s; call refresh to converge\n", shortDigest(diskGen))
	}
	return b.String()
}

// refreshAsync starts an async reindex round, deduped per process: a second
// call while one is in flight reports in-progress instead of stacking work.
func (s *mcpSession) refreshAsync() string {
	s.mu.Lock()
	if s.inFlight {
		gen := shortDigest(s.generation)
		s.mu.Unlock()
		return "refresh already in progress (serving generation " + gen + "); poll status for completion"
	}
	s.inFlight = true
	s.done = make(chan struct{})
	s.state = mcpStateUpdating
	s.notice = "codegraph is refreshing the index for " + s.project + " (serving generation " + shortDigest(s.generation) + "); retry shortly"
	gen := shortDigest(s.generation)
	s.mu.Unlock()
	go s.round()
	return "refresh started (serving generation " + gen + "); poll status for completion"
}

// startInitial runs the first index round asynchronously, like a refresh but
// keeping the constructor's "first run" notice until the round finishes.
func (s *mcpSession) startInitial() {
	s.mu.Lock()
	s.inFlight = true
	s.done = make(chan struct{})
	s.mu.Unlock()
	go s.round()
}

// waitRound blocks until no round is in flight or the timeout elapses.
func (s *mcpSession) waitRound(timeout time.Duration) bool {
	s.mu.Lock()
	done := s.done
	inFlight := s.inFlight
	s.mu.Unlock()
	if !inFlight {
		return true
	}
	if done == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// close shuts the session engine down. Safe to call while a round is in
// flight: the round serializes on the same mutex.
func (s *mcpSession) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serving = false
	return s.eng.Close()
}

// reopenShared reacquires the committed graph under a brief shared lock (the
// commit window a reader must not straddle) with bounded, cancellable retries.
// It never holds the session mutex while sleeping.
func (s *mcpSession) reopenShared(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(mcpReopenRetryDeadline)
	backoff := mcpReopenRetryInitialBackoff
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if s.hooks.reopenAttempt != nil {
			s.hooks.reopenAttempt()
		}
		rl, err := index.AcquireReaderLock(s.dbPath)
		if err == nil {
			s.mu.Lock()
			rerr := s.eng.Reopen(s.dbPath)
			if rerr == nil {
				s.serving = true
				s.captureServedManifestLocked()
			}
			s.mu.Unlock()
			releaseErr := rl.Release()
			if rerr != nil {
				return rerr
			}
			return releaseErr
		}
		if !errors.Is(err, index.ErrIndexLocked) {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("reopen store lock contention exceeded %v: %w", mcpReopenRetryDeadline, err)
		}
		wait := backoff
		if wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < mcpReopenRetryMaxBackoff {
			backoff *= 2
			if backoff > mcpReopenRetryMaxBackoff {
				backoff = mcpReopenRetryMaxBackoff
			}
		}
	}
}

// finish closes the in-flight round under the mutex.
func (s *mcpSession) finishLocked() {
	s.inFlight = false
	if s.done != nil {
		close(s.done)
		s.done = nil
	}
}

// captureServedManifestLocked records the identity of the graph the engine just
// loaded. Reopen reads the manifest under the shared lock that pins the commit,
// so this snapshot — not a later disk read — is what status, coverage, and the
// degraded check must report. Caller holds s.mu.
func (s *mcpSession) captureServedManifestLocked() {
	manifest, err := s.eng.Manifest()
	s.manifestErr = err
	if err != nil {
		s.generation = ""
		s.servedStatus = index.StatusDegraded
		return
	}
	s.generation = manifest.GraphContentDigest
	s.similar = manifest.Similar
	s.servedStatus = manifest.Status
}

// round runs one index round: probe, close, build, reopen, publish. It always
// clears the in-flight flag, never serves mixed generations, and preserves
// cleanup/recovery semantics by delegating the build to the atomic core.
func (s *mcpSession) round() {
	// Probe the writer lock BEFORE closing the engine: when another indexer is
	// already active the round declines fast and the session keeps serving the
	// current graph. Without this the close-then-reopen sequence would leave a
	// contended session unqueryable until the other writer goes away.
	if probe, perr := index.AcquireExclusiveLock(s.dbPath); perr != nil {
		if errors.Is(perr, index.ErrIndexLocked) {
			s.mu.Lock()
			s.state = mcpStateFailed
			s.notice = "codegraph: refresh deferred: " + perr.Error() + "; serving generation " + shortDigest(s.generation) + " — retry refresh"
			s.finishLocked()
			s.mu.Unlock()
			return
		}
		s.fail("codegraph: refresh probing index lock: " + perr.Error())
		return
	} else if rerr := probe.Release(); rerr != nil {
		s.fail("codegraph: refresh releasing probe lock: " + rerr.Error())
		return
	}
	s.mu.Lock()
	s.state = mcpStateUpdating
	closeErr := s.eng.Close()
	s.serving = false
	s.mu.Unlock()
	if closeErr != nil {
		s.fail("codegraph: close store before index failed: " + closeErr.Error())
		return
	}
	if err := s.ctx.Err(); err != nil {
		s.fail("codegraph: indexing canceled: " + err.Error())
		return
	}
	if s.hooks.beforeRunContext != nil {
		if err := s.hooks.beforeRunContext(s.ctx); err != nil {
			s.fail("codegraph: indexing canceled: " + err.Error())
			return
		}
	}
	if s.hooks.beforeRun != nil {
		s.hooks.beforeRun()
	}
	if err := s.ctx.Err(); err != nil {
		s.fail("codegraph: indexing canceled: " + err.Error())
		return
	}

	res, ierr := s.indexFn(s.ctx, s.dbPath, s.root)
	// Last-round diagnostics belong to the attempted run, even when cancellation
	// or a failed reopen prevents publication. Served identity remains separate.
	s.mu.Lock()
	s.lastResult = res
	s.hasResult = true
	s.mu.Unlock()
	// Hand freed heap back to the OS after the resolver spike, as before.
	if s.ctx.Err() == nil {
		debug.FreeOSMemory()
	}
	if ierr != nil {
		if s.ctx.Err() != nil || errors.Is(ierr, context.Canceled) || errors.Is(ierr, context.DeadlineExceeded) {
			s.fail("codegraph: indexing canceled: " + ierr.Error())
			return
		}
		// The atomic core leaves the previous graph intact: reopen it and keep
		// serving stale with the failure as context.
		if rerr := s.reopenShared(s.ctx); rerr != nil {
			if s.ctx.Err() == nil {
				s.fail("codegraph: indexing " + s.project + " failed: " + ierr.Error() + "; reopen store failed: " + rerr.Error())
			} else {
				s.fail("codegraph: indexing canceled: " + s.ctx.Err().Error())
			}
			return
		}
		s.mu.Lock()
		s.state = mcpStateFailed
		s.notice = "codegraph: indexing " + s.project + " failed: " + ierr.Error() + "; serving previous generation " + shortDigest(s.generation)
		s.everServed = s.everServed || s.serving
		s.finishLocked()
		s.mu.Unlock()
		return
	}

	if err := s.ctx.Err(); err != nil {
		s.fail("codegraph: indexing canceled: " + err.Error())
		return
	}
	if rerr := s.reopenShared(s.ctx); rerr != nil {
		if s.ctx.Err() == nil {
			s.fail("codegraph: reopen store after index failed: " + rerr.Error())
		} else {
			s.fail("codegraph: indexing canceled: " + s.ctx.Err().Error())
		}
		return
	}
	if s.hooks.afterReopen != nil {
		s.hooks.afterReopen()
	}
	s.publish(res)
}

// fail records a round failure. tryReopen is handled by callers (round
// reopens the previous graph itself on build failure); fail only publishes.
func (s *mcpSession) fail(notice string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.serving && !s.everServed {
		s.state = mcpStateUnavailable
	} else {
		s.state = mcpStateFailed
	}
	s.notice = notice
	s.finishLocked()
}

// publish adopts a successful round. The served generation and manifest identity
// were captured with the engine in reopenShared, so status and pages report the
// same graph even if another writer committed a newer generation between reopen
// and this call; the divergence surfaces as `lag` in status, not as a mismatch.
func (s *mcpSession) publish(res index.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastResult = res
	s.hasResult = true
	s.everServed = s.everServed || s.serving
	if s.manifestErr != nil {
		s.state = mcpStateFailed
		s.notice = "codegraph: indexed but manifest unreadable: " + s.manifestErr.Error()
		s.finishLocked()
		return
	}
	if res.ScipScopes > 0 {
		msg := fmt.Sprintf("codegraph: scip-typescript %d scope(s), node heap cap %d MB",
			res.ScipScopes, res.ScipHeapCapMB)
		if res.ScipPeakRSS > 0 {
			msg += fmt.Sprintf(", peak RSS %d MB", res.ScipPeakRSS/(1024*1024))
		}
		fmt.Fprintln(os.Stderr, msg)
	}
	if res.Status == index.StatusDegraded || s.servedStatus == index.StatusDegraded {
		s.state = mcpStateDegraded
		s.notice = "codegraph: indexing degraded; resolver failed: " + res.Resolver.Summary()
	} else {
		s.state = mcpStateReady
		s.notice = ""
	}
	s.finishLocked()
}
