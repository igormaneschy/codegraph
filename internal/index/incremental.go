package index

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/securefile"
)

// Changes is the set of files that differ from the indexed snapshot.
type Changes struct {
	Changed []string // indexed, but content hash differs now
	Added   []string // on disk, absent from the index
	Deleted []string // in the index, gone from disk
	files   []SourceFile
}

// These markers live in the same map as ordinary resolver scopes but cannot be
// confused with a repository-relative scope. They make invalidation policy
// explicit at the reuse boundary, including old edges whose scope no longer
// exists in the current tsconfig enumeration.
const (
	allCallScopesMarker   = "\x00all-call-scopes"
	allTSCallScopesMarker = "\x00all-ts-call-scopes"
)

// Any reports whether anything changed since the last index.
func (c Changes) Any() bool {
	return len(c.Changed)+len(c.Added)+len(c.Deleted) > 0
}

// Summary renders the change set as the compact wire format the detect_changes tool
// returns: one `status<TAB>path` line per file (changed, then added, then deleted).
func (c Changes) Summary() string {
	var b strings.Builder
	for _, p := range c.Changed {
		b.WriteString("changed\t" + p + "\n")
	}
	for _, p := range c.Added {
		b.WriteString("added\t" + p + "\n")
	}
	for _, p := range c.Deleted {
		b.WriteString("deleted\t" + p + "\n")
	}
	return b.String()
}

// DetectChanges compares the source files currently under root against the per-file
// content hashes recorded at the last index (Store.FileHashes). It is the basis for
// skipping a re-index when nothing changed, and later for re-resolving only the
// scopes whose files moved. A never-indexed project reports every file as Added.
func DetectChanges(store *graph.Store, project, root string) (Changes, error) {
	root, err := ValidateRepositoryRoot(root)
	if err != nil {
		return Changes{}, err
	}
	return detectChangesCanonical(store, project, root)
}

func detectChangesCanonical(store *graph.Store, project, root string) (Changes, error) {
	return detectChangesCanonicalContext(context.Background(), store, project, root)
}

func detectChangesCanonicalContext(ctx context.Context, store *graph.Store, project, root string) (Changes, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return Changes{}, err
	}
	files, err := discoverCanonicalContext(ctx, root)
	if err != nil {
		return Changes{}, err
	}
	stored, err := store.FileHashes(project)
	if err != nil {
		return Changes{}, err
	}

	var ch Changes
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return Changes{}, err
		}
		seen[f.RelPath] = true
		data, err := securefile.ReadFile(f.AbsPath)
		if err != nil {
			return Changes{}, fmt.Errorf("read discovered source %q: %w", f.RelPath, err)
		}
		switch prev, ok := stored[f.RelPath]; {
		case !ok:
			ch.Added = append(ch.Added, f.RelPath)
		case prev != hashBytes(data):
			ch.Changed = append(ch.Changed, f.RelPath)
		}
	}
	for path := range stored {
		if err := ctx.Err(); err != nil {
			return Changes{}, err
		}
		if !seen[path] {
			ch.Deleted = append(ch.Deleted, path)
		}
	}
	ch.files = files
	return ch, nil
}

// scopeOf returns the CALLS scope a repo-relative file belongs to. Go files share
// the one "go" scope (go/packages + VTA is whole-module); a TS/JS file belongs to
// the tsconfig-project directory that most tightly encloses it, or "" (the repo-root
// scip run) when no subproject does. Scopes are the unit of incremental re-resolution.
func scopeOf(rel string, tsconfigDirs []string) string {
	ext := strings.ToLower(filepath.Ext(rel))
	if ext == ".go" {
		return "go"
	}
	switch ext {
	case ".rb", ".rake", ".ru", ".rbi":
		return "ruby"
	}
	best, bestLen := "", -1
	for _, d := range tsconfigDirs {
		if d != "" && (rel == d || strings.HasPrefix(rel, d+"/")) && len(d) > bestLen {
			best, bestLen = d, len(d)
		}
	}
	return best
}

const reusedCallEdgeBatch = 2048

// forEachReusableCallEdgeContext invokes fn for each stored CALLS edge whose
// caller scope is not being re-resolved. source must still hold the pre-reindex
// graph.
func forEachReusableCallEdgeContext(ctx context.Context, source *graph.Store, project string, changed map[string]bool, tsconfigDirs []string, fn func(graph.Edge) error) error {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	return source.ForEachCallEdge(project, func(e graph.CallEdge) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if changed[allCallScopesMarker] ||
			(isTSSourcePath(e.SourceFile) && changed[allTSCallScopesMarker]) ||
			changed[scopeOf(e.SourceFile, tsconfigDirs)] {
			return nil
		}
		err := fn(graph.Edge{
			Project: project, SourceQN: e.SourceQN, TargetQN: e.TargetQN,
			Type: graph.EdgeCalls, Props: e.Props,
		})
		if err != nil {
			return err
		}
		return ctx.Err()
	})
}

// insertReusedCallEdgesContext streams reusable CALLS edges from source into
// target in batches. source must hold a pre-wipe graph snapshot (second
// connection + BeginReadSnapshot for Run, or the main store file for RunAtomic).
func insertReusedCallEdgesContext(ctx context.Context, target, source *graph.Store, project string, changed map[string]bool, tsconfigDirs []string) (inserted, dropped int, err error) {
	ctx = nonNilContext(ctx)
	var batch []graph.Edge
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		k, d, e := target.InsertEdges(batch)
		inserted += k
		dropped += d
		batch = batch[:0]
		return e
	}
	err = forEachReusableCallEdgeContext(ctx, source, project, changed, tsconfigDirs, func(e graph.Edge) error {
		batch = append(batch, e)
		if len(batch) >= reusedCallEdgeBatch {
			return flush()
		}
		return nil
	})
	if err != nil {
		return inserted, dropped, err
	}
	if err := ctx.Err(); err != nil {
		return inserted, dropped, err
	}
	if err := flush(); err != nil {
		return inserted, dropped, err
	}
	return inserted, dropped, nil
}

// changedScopes is the set of CALLS scopes touched by a change set — exactly the
// scopes whose resolver must re-run. A scope absent from the result has no changed
// file and reuses its stored edges.
func changedScopes(ch Changes, tsconfigDirs []string) map[string]bool {
	out := map[string]bool{}
	for _, group := range [][]string{ch.Changed, ch.Added, ch.Deleted} {
		for _, rel := range group {
			out[scopeOf(rel, tsconfigDirs)] = true
		}
	}
	return out
}

// changedScopesWithTSDependencies conservatively expands any TS/JS source
// transition to every current TS scope. Package/workspace/path-alias ownership
// is not fully resolved by the lightweight import pass, so a guessed reverse
// importer set is not safe for CALLS reuse.
func changedScopesWithTSDependencies(ctx context.Context, ch Changes, tsconfigDirs []string) (map[string]bool, error) {
	out := changedScopes(ch, tsconfigDirs)
	for _, paths := range [][]string{ch.Changed, ch.Added, ch.Deleted} {
		for _, rel := range paths {
			if !isTSSourcePath(rel) {
				continue
			}
			// Package/workspace/path-alias ownership is not fully resolved by the
			// lightweight import pass. Reusing only a guessed reverse-importer set
			// could certify a stale caller in another TS project, so every current
			// TS scope is invalidated for any TS/JS source transition.
			out[allTSCallScopesMarker] = true
			for _, dir := range tsconfigDirs {
				out[dir] = true
			}
			return out, nil
		}
	}
	return out, nil
}

func isTSSourcePath(rel string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return true
	default:
		return false
	}
}
