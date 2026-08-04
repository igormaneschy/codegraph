package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/memory"
	"github.com/Lordymine/codegraph/internal/scip"
)

// Result summarizes an indexing run.
type Result struct {
	Project       string
	Files         int
	Nodes         int
	EdgesKept     int
	EdgesDropped  int
	Reused        bool // nothing changed since the last index; the pipeline was skipped
	Status        IndexStatus
	Resolver      ResolverReport
	ScipScopes    int
	ScipPeakRSS   uint64
	ScipHeapCapMB int
}

// BuildingSuffix is the suffix RunAtomic uses for in-progress index files.
const BuildingSuffix = ".building"

// ErrIndexLocked reports that another indexer already owns the per-database
// replacement lock.
var ErrIndexLocked = errors.New("index already in progress")

// These hooks are deliberately package-local test seams. They let the atomic
// tests hold a build at a deterministic point and fail replacement without
// relying on sleeps or filesystem races.
var (
	pipelinePreflightHook       func()
	pipelineContextHook         func(context.Context) error
	replaceBuiltIndexErr        error
	replaceManifestErr          error
	manifestPostWriteHook       func()
	manifestPostReplaceHook     func()
	beforeBuiltIndexReplaceHook func()
	similarPassOverride         func(context.Context, string, string, []graph.FunctionSpan) ([]graph.Edge, error)
)

// Run indexes root into an already-open store (tests/temp dirs). Prefer RunAtomic
// for CLI/MCP so a failed re-index does not wipe the previous graph.
func Run(store *graph.Store, root string) (Result, error) {
	return RunContext(context.Background(), store, root)
}

// RunContext is the cancellable form of Run. Cancellation is checked before
// mutating the target store and between pipeline phases; work already inside a
// parser or resolver is allowed to return before the caller closes the store.
func RunContext(ctx context.Context, store *graph.Store, root string) (Result, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	dbPath := store.DBPath()
	if dbPath == "" {
		return Result{}, fmt.Errorf("run index: store has no database path")
	}
	if err := store.Close(); err != nil {
		return Result{}, fmt.Errorf("close store before atomic index: %w", err)
	}
	result, err := runAtomicContext(ctx, dbPath, root, false)
	if reopenErr := store.Reopen(dbPath); reopenErr != nil {
		if err != nil {
			return result, errors.Join(err, fmt.Errorf("reopen store after index: %w", reopenErr))
		}
		return result, fmt.Errorf("reopen store after index: %w", reopenErr)
	}
	return result, err
}

// RunAtomic builds into dbPath+BuildingSuffix and replaces dbPath only after a
// complete, checkpointed build. A non-blocking per-database lock serializes
// competing indexers, including CLI and MCP callers in the same process or in
// separate processes.
func RunAtomic(dbPath, root string) (res Result, err error) {
	return RunAtomicContext(context.Background(), dbPath, root)
}

// RunAtomicContext is the cancellable production indexing path. Cancellation
// observed before the final replacement linearization point prevents the live
// database rename. Once that point starts, the filesystem commit may complete
// even if cancellation is observed concurrently; the caller still waits for
// the in-process work to return before releasing the index lock.
func RunAtomicContext(ctx context.Context, dbPath, root string) (res Result, err error) {
	return runAtomicContext(ctx, dbPath, root, true)
}

func runAtomicContext(ctx context.Context, dbPath, root string, strictFreshness bool) (res Result, err error) {
	ctx = nonNilContext(ctx)
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	root, err = ValidateRepositoryRoot(root)
	if err != nil {
		return Result{}, err
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	dbPath, err = normalizeIndexPath(dbPath)
	if err != nil {
		return Result{}, err
	}
	lock, err := acquireIndexLock(dbPath)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if lockErr := lock.Release(); lockErr != nil {
			err = errors.Join(err, fmt.Errorf("release index lock: %w", lockErr))
		}
	}()

	building := dbPath + BuildingSuffix
	manifestBuilding := manifestBuildingPath(dbPath)
	var main, store *graph.Store
	defer func() {
		if store != nil {
			_ = store.Close()
		}
		if main != nil {
			_ = main.Close()
		}
		if cleanupErr := removeIndexArtifacts(building); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("cleanup failed build: %w", cleanupErr))
		}
		if cleanupErr := removeManifestArtifacts(manifestBuilding); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("cleanup failed manifest: %w", cleanupErr))
		}
	}()

	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := recoverIndexReplacement(dbPath); err != nil {
		return Result{}, fmt.Errorf("recover interrupted replacement: %w", err)
	}
	if err := removeIndexArtifacts(building); err != nil {
		return Result{}, fmt.Errorf("remove stale build: %w", err)
	}
	if err := removeManifestArtifacts(manifestBuilding); err != nil {
		return Result{}, fmt.Errorf("remove stale manifest build: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	liveGraphPresent := false
	if _, statErr := os.Stat(dbPath); statErr == nil || !os.IsNotExist(statErr) {
		liveGraphPresent = true
	}
	var openErr error
	main, openErr = graph.Open(dbPath)
	if openErr != nil {
		// A graph that cannot be opened is a freshness miss, not a repository
		// or resolver failure. The old path is deliberately left in place while
		// the independent .building graph is created.
		main = nil
	}
	in, reused, err := prepareIndexingContext(ctx, main, root, strictFreshness)
	if err != nil {
		return Result{}, err
	}
	if openErr != nil {
		in.existingGraph = liveGraphPresent
		in.graphFreshnessMiss = true
	}
	if reused != nil {
		if err := main.Close(); err != nil {
			main = nil
			return Result{}, fmt.Errorf("close unchanged index: %w", err)
		}
		main = nil
		return *reused, nil
	}
	if !in.graphFreshnessMiss {
		in.reuseFrom = main
	}
	// A freshness miss deliberately keeps the live handle out of reuseFrom so
	// runPipelineContext cannot stream old CALLS. Keep the handle open only until
	// the final checkpoint below: an active WAL reader must still be able to veto
	// replacement, while the independent .building graph can proceed.

	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	store, err = graph.Open(building)
	if err != nil {
		return Result{}, err
	}
	res, err = runPipelineContext(ctx, store, in)
	if err != nil {
		return res, err
	}
	if strictFreshness {
		// RunAtomic's replacement gate keeps the exact SQLite/FTS/endpoint
		// validation. Run's benchmark/test path deliberately skips this heavy
		// post-build pass; callers can still invoke Store.ValidateIntegrity
		// directly when they need the full diagnostic.
		if err := validateStoreIntegrity(store); err != nil {
			return res, fmt.Errorf("validate built index: %w", err)
		}
	}
	resManifest := in.manifest
	resManifest.Status = res.Status
	resManifest.Resolver = res.Resolver
	resManifest.GraphContentDigest, err = store.LogicalGraphDigest(in.project)
	if err != nil {
		return res, fmt.Errorf("digest built index: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return res, err
	}
	if err := store.Checkpoint(); err != nil {
		return res, fmt.Errorf("checkpoint built index: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return res, err
	}
	if err := store.Close(); err != nil {
		store = nil
		return res, fmt.Errorf("close built index: %w", err)
	}
	store = nil
	resManifest.GraphIdentity, err = graphIdentity(building)
	if err != nil {
		return res, fmt.Errorf("identify built index: %w", err)
	}
	if err := writeManifestFile(manifestBuilding, resManifest); err != nil {
		return res, fmt.Errorf("write index manifest: %w", err)
	}
	if manifestPostWriteHook != nil {
		manifestPostWriteHook()
	}
	if err = ctx.Err(); err != nil {
		return res, err
	}
	if main != nil {
		if err := main.Checkpoint(); err != nil {
			return res, fmt.Errorf("checkpoint existing index: %w", err)
		}
		if err := main.Close(); err != nil {
			main = nil
			return res, fmt.Errorf("close existing index: %w", err)
		}
		main = nil
	}

	if err = ctx.Err(); err != nil {
		return res, err
	}
	// Install the manifest before replacing the live graph. If manifest
	// installation fails, the old graph and its old manifest remain paired. If
	// graph replacement fails after this point, the new manifest intentionally
	// mismatches the preserved old graph, which is fail-closed on the next start.
	// Readers are excluded by the shared/exclusive index lock for this brief
	// two-file commit window.
	if replaceManifestErr != nil {
		return res, fmt.Errorf("commit index manifest: %w", replaceManifestErr)
	}
	if err := replaceManifestPlatform(manifestBuilding, ManifestPath(dbPath)); err != nil {
		return res, fmt.Errorf("commit index manifest: %w", err)
	}
	if manifestPostReplaceHook != nil {
		manifestPostReplaceHook()
	}
	if err = ctx.Err(); err != nil {
		return res, err
	}
	if err := commitBuiltIndex(ctx, building, dbPath); err != nil {
		return res, fmt.Errorf("commit index: %w", err)
	}
	return res, nil
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func commitBuiltIndex(ctx context.Context, building, dbPath string) error {
	// Build sidecars must not survive a replacement: SQLite sidecars are named
	// after the destination path and could otherwise be paired with the wrong
	// main database after the rename.
	if err := removeSQLiteSidecars(building); err != nil {
		return fmt.Errorf("remove build sidecars: %w", err)
	}
	if replaceBuiltIndexErr != nil {
		return replaceBuiltIndexErr
	}
	if beforeBuiltIndexReplaceHook != nil {
		beforeBuiltIndexReplaceHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Linearization point: once the platform replacement begins, cancellation
	// may race with the rename and the commit is allowed to finish. The check
	// immediately above is the last cancellation gate before substitution.
	return replaceBuiltIndexPlatform(building, dbPath)
}

func runPipeline(store *graph.Store, in pipelineInput) (Result, error) {
	return runPipelineContext(context.Background(), store, in)
}

func runPipelineContext(ctx context.Context, store *graph.Store, in pipelineInput) (Result, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if pipelineContextHook != nil {
		if err := pipelineContextHook(ctx); err != nil {
			return Result{}, err
		}
	}
	if pipelinePreflightHook != nil {
		pipelinePreflightHook()
	}
	if err := pipelinePreflight(); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !in.repositoryScanned {
		return Result{}, errors.New("pipeline requires a validated repository observation")
	}
	files := in.files
	tsdirs := in.tsdirs
	var err error
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if err := store.ReplaceProject(in.project); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	nodeCount, defEdges, err := indexDefinitionsBatchedContext(ctx, store, in.project, files)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	k, d, err := store.InsertEdges(defEdges)
	if err != nil {
		return Result{}, fmt.Errorf("insert defines edges: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	edgesKept, edgesDropped := k, d
	defEdges = nil
	memory.Gate()

	importEdges, err := collectImportsStreamingContext(ctx, in.project, files)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	k, d, err = store.InsertEdges(importEdges)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	edgesKept += k
	edgesDropped += d
	importEdges = nil
	memory.Gate()

	spans, err := store.FunctionSpans(in.project)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	enc := scip.BuildEnclosingFromSpans(spans)
	expectedScopes := expectedResolverScopeKeys(in.root, files, tsdirs)

	scipRep, err := resolveTSCallsWithDirs(ctx, store, in.project, in.root, tsdirs, enc, in.changed)
	if err != nil {
		return Result{}, fmt.Errorf("ts calls: %w", err)
	}
	resolverReport := ResolverReport{Scopes: append([]ResolverScopeStatus(nil), scipRep.Scopes...)}
	resolverEdgesKept := scipRep.EdgesKept
	edgesKept += scipRep.EdgesKept
	edgesDropped += scipRep.EdgesDropped
	memory.Gate()

	goEdges, goScope, err := resolveGoCalls(ctx, in.project, in.root, files, enc, in.changed)
	if err != nil {
		return Result{}, fmt.Errorf("go calls: %w", err)
	}
	if goScope.Resolver != "" {
		resolverReport.Scopes = append(resolverReport.Scopes, goScope)
	}
	k, d, err = store.InsertEdges(goEdges)
	if err != nil {
		return Result{}, fmt.Errorf("insert go call edges: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	edgesKept += k
	edgesDropped += d
	resolverEdgesKept += k
	goEdges = nil
	memory.Gate()

	// A Ruby scope is either re-resolved here or streamed unchanged below; changed
	// scope gating makes the two paths mutually exclusive.
	rubyEdges, rubyScope, err := resolveRubyCalls(ctx, store, in.project, files, enc, in.changed)
	if err != nil {
		return Result{}, fmt.Errorf("ruby calls: %w", err)
	}
	if rubyScope.Resolver != "" {
		resolverReport.Scopes = append(resolverReport.Scopes, rubyScope)
	}
	k, d, err = store.InsertEdges(rubyEdges)
	if err != nil {
		return Result{}, fmt.Errorf("insert ruby call edges: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	edgesKept += k
	edgesDropped += d
	resolverEdgesKept += k
	rubyEdges = nil
	memory.Gate()

	if err := resolverReport.ValidateExpected(expectedScopes); err != nil {
		return Result{}, fmt.Errorf("validate resolver report: %w", err)
	}
	if resolverReport.HasFailures() {
		failedResult := Result{
			Project: in.project, Files: len(files), Nodes: nodeCount,
			EdgesKept: edgesKept, EdgesDropped: edgesDropped,
			Status: StatusStale, Resolver: resolverReport,
			ScipScopes: scipRep.ScopesRun, ScipPeakRSS: scipRep.PeakRSS, ScipHeapCapMB: scipRep.HeapCapMB,
		}
		if in.existingGraph {
			return failedResult, &ResolverFailure{Report: resolverReport}
		}
		// A first index may commit structural data, but never a partial CALLS
		// set. Successful resolver edges from this attempt are removed as one
		// class, leaving a graph whose degraded status is explicit.
		if err := store.DeleteEdgesByType(in.project, graph.EdgeCalls); err != nil {
			return failedResult, fmt.Errorf("discard partial resolver edges: %w", err)
		}
		edgesKept -= resolverEdgesKept
	}

	if in.reuseFrom != nil {
		k, d, err = insertReusedCallEdgesContext(ctx, store, in.reuseFrom, in.project, in.changed, tsdirs)
		if err != nil {
			return Result{}, fmt.Errorf("insert reused call edges: %w", err)
		}
		edgesKept += k
		edgesDropped += d
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
	}
	enc = nil
	memory.Gate()

	if !memory.SkipSimilar() || similarPassOverride != nil {
		similarPass := resolveSimilarFromSpans
		if similarPassOverride != nil {
			similarPass = similarPassOverride
		}
		simEdges, err := similarPass(ctx, in.project, in.root, spans)
		if err != nil {
			return Result{}, err
		}
		k, d, err = store.InsertEdges(simEdges)
		if err != nil {
			return Result{}, fmt.Errorf("insert similar edges: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		edgesKept += k
		edgesDropped += d
	}
	spans = nil
	memory.Gate()

	status := StatusHealthy
	if resolverReport.HasFailures() {
		status = StatusDegraded
	}
	if err := validateResolverStatus(status, resolverReport); err != nil {
		return Result{}, fmt.Errorf("validate resolver status: %w", err)
	}
	return Result{
		Project: in.project, Files: len(files), Nodes: nodeCount,
		EdgesKept: edgesKept, EdgesDropped: edgesDropped,
		Status: status, Resolver: resolverReport,
		ScipScopes: scipRep.ScopesRun, ScipPeakRSS: scipRep.PeakRSS, ScipHeapCapMB: scipRep.HeapCapMB,
	}, nil
}

// indexDefinitionsBatched extracts definitions with bounded parallelism, flushes
// nodes to SQLite per batch, and returns DEFINES edges to insert in one shot (edges
// are tiny vs nodes — holding them all is cheap; reloading idByQN per batch is not).
func indexDefinitionsBatchedContext(ctx context.Context, store *graph.Store, project string, files []SourceFile) (nodes int, defEdges []graph.Edge, err error) {
	ctx = nonNilContext(ctx)
	workers := memory.MaxWorkers()
	batchSize := memory.BatchSize()

	for start := 0; start < len(files); start += batchSize {
		if err := ctx.Err(); err != nil {
			return nodes, defEdges, err
		}
		end := start + batchSize
		if end > len(files) {
			end = len(files)
		}
		batch := files[start:end]

		type out struct {
			nodes []graph.Node
			edges []graph.Edge
			err   error
		}
		results := make([]out, len(batch))
		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup
		for i, f := range batch {
			wg.Add(1)
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wg.Done()
				wg.Wait()
				return nodes, defEdges, ctx.Err()
			}
			go func(i int, f SourceFile) {
				defer wg.Done()
				defer func() { <-sem }()
				n, e, readErr := ExtractDefinitionsChecked(project, f)
				results[i] = out{nodes: n, edges: e, err: readErr}
			}(i, f)
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nodes, defEdges, err
		}

		var batchNodes []graph.Node
		var batchEdges []graph.Edge
		for _, r := range results {
			if r.err != nil {
				return nodes, defEdges, r.err
			}
			batchNodes = append(batchNodes, r.nodes...)
			batchEdges = append(batchEdges, r.edges...)
		}
		results = nil
		if err := store.InsertNodes(batchNodes); err != nil {
			return nodes, defEdges, fmt.Errorf("insert nodes: %w", err)
		}
		nodes += len(batchNodes)
		defEdges = append(defEdges, batchEdges...)
		batchNodes, batchEdges = nil, nil
		memory.Gate()
	}
	return nodes, defEdges, nil
}

func indexDefinitionsBatched(store *graph.Store, project string, files []SourceFile) (nodes int, defEdges []graph.Edge, err error) {
	return indexDefinitionsBatchedContext(context.Background(), store, project, files)
}

// ProjectName derives a stable project key from the repo root (matches the
// upstream convention of slugging the absolute path).
func ProjectName(root string) string {
	if canonical, err := CanonicalPath(root); err == nil {
		root = canonical
	} else if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	}
	slug := filepath.ToSlash(root)
	repl := func(r rune) rune {
		switch r {
		case '/', ':', '\\', ' ':
			return '-'
		}
		return r
	}
	out := []rune{}
	for _, r := range slug {
		out = append(out, repl(r))
	}
	s := string(out)
	for len(s) > 0 && s[0] == '-' {
		s = s[1:]
	}
	return s
}
