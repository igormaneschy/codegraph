package index

import (
	"context"
	"fmt"

	"github.com/Lordymine/codegraph/internal/graph"
)

// pipelineInput carries incremental state shared by Run and RunAtomic.
type pipelineInput struct {
	project string
	root    string
	changed map[string]bool
	files   []SourceFile
	tsdirs  []string
	// repositoryScanned means files, source identity, tsdirs, and manifest were
	// captured by the same validated observation. The pipeline must use this
	// handoff rather than rediscovering the repository after freshness checks.
	repositoryScanned bool
	reuseFrom         *graph.Store // pre-reindex DB; CALLS streamed via insertReusedCallEdgesContext
	manifest          Manifest
	existingGraph     bool
	// graphFreshnessMiss means the old graph could not be trusted as a CALLS
	// snapshot. The production caller must build without reuseFrom, while the
	// live path remains untouched until the new build commits.
	graphFreshnessMiss bool
}

const repositoryObservationRetries = 3

// repositoryObservationHook is a deterministic test seam for changing the
// repository between the first and validating observations. Production leaves
// it nil; an unstable repository fails closed after the bounded retry policy.
var repositoryObservationHook func()

// prepareIndexing detects changes and builds pipelineInput. When the repo is
// unchanged and already indexed, reused is non-nil and input is nil.
func prepareIndexing(store *graph.Store, root string) (input pipelineInput, reused *Result, err error) {
	root, err = ValidateRepositoryRoot(root)
	if err != nil {
		return pipelineInput{}, nil, err
	}
	return prepareIndexingContext(context.Background(), store, root, true)
}

func prepareIndexingContext(ctx context.Context, store *graph.Store, root string, strictFreshness bool) (input pipelineInput, reused *Result, err error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return pipelineInput{}, nil, err
	}
	project := ProjectName(root)
	scan, err := scanRepositoryContext(ctx, root)
	if err != nil {
		return pipelineInput{}, nil, err
	}

	// scanRepositoryContext establishes all repository-side boundaries before
	// interpreting a graph read failure as a freshness miss. Discovery, source
	// reads, manifest inputs, and resolver config errors remain visible rather
	// than being hidden by a rebuild attempt.
	if err := ctx.Err(); err != nil {
		return pipelineInput{}, nil, err
	}
	if store == nil {
		scan, err = validateRepositoryObservation(ctx, root, scan)
		if err != nil {
			return pipelineInput{}, nil, err
		}
		scan.sourceObservations = nil
		return pipelineInput{
			project: project, root: root, changed: allResolverScopesChanged(scan.tsdirs),
			files: scan.files, tsdirs: scan.tsdirs,
			repositoryScanned: true, manifest: scan.manifest, graphFreshnessMiss: true,
		}, nil, nil
	}

	// A readable SQLite handle is not enough to certify a production no-op.
	// Structural, FTS, endpoint, and JSON validation is the final gate inside
	// freshManifestFor for the strict path. Run is the legacy in-place/test path
	// and keeps its lighter preparation contract for large memory-budget
	// exercises.
	graphFreshnessMiss := false
	existingGraph := false
	var storedHashes map[string]string
	haveStoredHashes := false

	if !graphFreshnessMiss {
		storedHashes, err = store.FileHashes(project)
		if err != nil {
			graphFreshnessMiss = true
			existingGraph = true
		} else {
			haveStoredHashes = true
		}
	}

	rubyAnalysisCurrent := false
	if !graphFreshnessMiss {
		rubyAnalysisCurrent, err = store.RubyAnalysisCurrent(project, rubyAnalysisVersion)
		if err != nil {
			graphFreshnessMiss = true
			existingGraph = true
		}
	}
	if err := ctx.Err(); err != nil {
		return pipelineInput{}, nil, err
	}

	n, e := 0, 0
	if !graphFreshnessMiss {
		n, e, err = store.Stats(project)
		if err != nil {
			graphFreshnessMiss = true
			existingGraph = true
		} else {
			existingGraph = n > 0
		}
	}
	if err := ctx.Err(); err != nil {
		return pipelineInput{}, nil, err
	}

	// This is the final repository observation before the no-op decision or
	// pipeline handoff. It includes source membership/content, resolver scopes,
	// and manifest inputs from one scan and retries only a bounded number of
	// times when the repository changes while it is being observed. Its return is
	// the freshness linearization point; later filesystem mutations are observed
	// by the next run rather than being claimed impossible to race.
	scan, err = validateRepositoryObservation(ctx, root, scan)
	if err != nil {
		return pipelineInput{}, nil, err
	}
	files, tsdirs, currentManifest := scan.files, scan.tsdirs, scan.manifest
	expectedScopes := expectedResolverScopeKeys(root, files, tsdirs)

	var changes Changes
	if haveStoredHashes {
		changes = changesFromRepositoryScan(scan, storedHashes)
	}
	scan.sourceObservations = nil
	var storedManifest Manifest
	var manifestFresh bool
	if !graphFreshnessMiss {
		if strictFreshness {
			storedManifest, manifestFresh = freshManifestFor(store, project, currentManifest)
		} else {
			// Run is the legacy in-place/test entry point. Its callers may deliberately
			// add graph sentinels between runs to verify scope reuse; RunAtomic is the
			// production freshness boundary and always performs the full integrity and
			// logical-digest certification above.
			storedManifest, manifestFresh = manifestFingerprintFor(store, currentManifest)
		}
	}
	if manifestFresh {
		if err := storedManifest.Resolver.ValidateExpected(expectedScopes); err != nil {
			manifestFresh = false
		}
	}
	if !graphFreshnessMiss && !changes.Any() && rubyAnalysisCurrent && manifestFresh {
		return pipelineInput{}, &Result{
			Project: project, Files: len(files), Nodes: n, EdgesKept: e,
			Reused: true, Status: storedManifest.Status, Resolver: storedManifest.Resolver,
		}, nil
	}

	var changed map[string]bool
	if graphFreshnessMiss || !haveStoredHashes {
		changed = allResolverScopesChanged(tsdirs)
	} else {
		changed, err = changedScopesWithTSDependencies(ctx, changes, tsdirs)
		if err != nil {
			return pipelineInput{}, nil, err
		}
	}
	if !manifestFresh {
		// A config, dependency, discovery, parser, or resolver identity change
		// invalidates every resolver environment. Reusing a caller from any
		// TypeScript scope would otherwise certify a callee under an old type
		// environment. The explicit marker also prevents an old edge from a
		// removed/renamed scope being remapped into a new root scope.
		changed[allCallScopesMarker] = true
		for _, dir := range tsdirs {
			changed[dir] = true
		}
		changed["go"] = true
		changed["ruby"] = true
	}
	if !rubyAnalysisCurrent {
		// A Ruby analysis-version mismatch is a graph freshness miss, not merely
		// a Ruby resolver scope change. The old graph cannot remain a CALLS
		// snapshot while the structural Ruby analysis is rebuilt.
		changed["ruby"] = true
	}
	return pipelineInput{
		project: project, root: root, changed: changed, files: files,
		tsdirs:            tsdirs,
		repositoryScanned: true,
		manifest:          currentManifest, existingGraph: existingGraph,
		graphFreshnessMiss: graphFreshnessMiss || !manifestFresh || !rubyAnalysisCurrent,
	}, nil, nil
}

func validateRepositoryObservation(ctx context.Context, root string, initial repositoryScan) (repositoryScan, error) {
	previous := initial
	for attempt := 0; attempt < repositoryObservationRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return repositoryScan{}, err
		}
		if repositoryObservationHook != nil {
			repositoryObservationHook()
		}
		current, err := scanRepositoryContext(ctx, root)
		if err != nil {
			return repositoryScan{}, err
		}
		if sameRepositoryScan(previous, current) {
			return current, nil
		}
		previous = current
	}
	return repositoryScan{}, fmt.Errorf("repository observation unstable after %d validation attempts", repositoryObservationRetries)
}

func allResolverScopesChanged(tsdirs []string) map[string]bool {
	changed := map[string]bool{
		allCallScopesMarker:   true,
		allTSCallScopesMarker: true,
		"go":                  true,
		"ruby":                true,
	}
	for _, dir := range tsdirs {
		changed[dir] = true
	}
	return changed
}

func changesFromRepositoryScan(scan repositoryScan, stored map[string]string) Changes {
	var changes Changes
	seen := make(map[string]bool, len(scan.sourceObservations))
	for _, observation := range scan.sourceObservations {
		seen[observation.path] = true
		if previous, ok := stored[observation.path]; !ok {
			changes.Added = append(changes.Added, observation.path)
		} else if previous != observation.hash {
			changes.Changed = append(changes.Changed, observation.path)
		}
	}
	for path := range stored {
		if !seen[path] {
			changes.Deleted = append(changes.Deleted, path)
		}
	}
	changes.files = scan.files
	return changes
}
