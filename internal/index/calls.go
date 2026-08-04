package index

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	scippb "github.com/scip-code/scip/bindings/go/scip"

	"github.com/Lordymine/codegraph/internal/gocalls"
	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/memory"
	"github.com/Lordymine/codegraph/internal/scip"
)

// ScipReport summarizes scip-typescript resource use across all TS scopes in one
// index run. PeakRSS is the max child RSS observed (Linux/WSL); zero elsewhere.
type ScipReport struct {
	ScopesRun    int
	PeakRSS      uint64
	HeapCapMB    int
	EdgesKept    int
	EdgesDropped int
	Scopes       []ResolverScopeStatus
}

// Resolver hooks keep failure-path tests deterministic without weakening the
// production resolver boundary. The defaults are the real batch resolvers.
var (
	scipRunAndRead = scip.RunAndReadContext
	goCallEdges    = gocalls.CallEdgesContext
)

// runSCIPInvocation gives every resolver call a private, random artifact
// directory. A scope name is not an identity: two repositories can resolve the
// same scope concurrently, and cleanup must never remove another invocation's
// output.
func runSCIPInvocation(ctx context.Context, dir string) (idx *scippb.Index, st scip.RunStats, err error) {
	tempDir, err := os.MkdirTemp("", "codegraph-scip-")
	if err != nil {
		return nil, st, fmt.Errorf("create private SCIP output directory: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(tempDir); cleanupErr != nil {
			cleanupErr = fmt.Errorf("cleanup private SCIP output directory %q: %w", tempDir, cleanupErr)
			if err == nil {
				err = cleanupErr
			} else {
				err = errors.Join(err, cleanupErr)
			}
		}
	}()
	outPath := filepath.Join(tempDir, "index.scip")
	return scipRunAndRead(ctx, dir, outPath)
}

// resolveTSCalls runs scip-typescript per tsconfig scope in isolation: one scope at
// a time, CALLS edges flushed to SQLite immediately, protobuf and Node heap released
// via memory.Gate() before the next scope or the Go VTA pass — same pattern as Go.
func resolveTSCalls(ctx context.Context, store *graph.Store, project, root string, enc scip.Enclosing, changed map[string]bool) (ScipReport, error) {
	dirs, err := tsconfigDirsContext(ctx, root)
	if err != nil {
		return ScipReport{}, err
	}
	return resolveTSCallsWithDirs(ctx, store, project, root, dirs, enc, changed)
}

// resolveTSCallsWithDirs consumes the scope list captured by preparation. The
// production path must not walk the repository again after the freshness scan;
// the wrapper above remains for focused callers that do not already have a scan.
func resolveTSCallsWithDirs(ctx context.Context, store *graph.Store, project, root string, dirs []string, enc scip.Enclosing, changed map[string]bool) (ScipReport, error) {
	ctx = nonNilContext(ctx)
	var rep ScipReport

	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if changed != nil && !changed[dir] {
			rep.Scopes = append(rep.Scopes, ResolverScopeStatus{
				Resolver: "scip-typescript", Scope: dir, Reused: true,
			})
			continue
		}
		scopeStatus := ResolverScopeStatus{Resolver: "scip-typescript", Scope: dir, Attempted: true}
		abs := filepath.Join(root, filepath.FromSlash(dir))
		idx, st, err := runSCIPInvocation(ctx, abs)
		if rep.HeapCapMB == 0 {
			rep.HeapCapMB = st.NodeHeapMB
		}
		if st.PeakRSSBytes > rep.PeakRSS {
			rep.PeakRSS = st.PeakRSSBytes
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return rep, err
			}
			scopeStatus.Failed = true
			scopeStatus.Error = err.Error()
			rep.Scopes = append(rep.Scopes, scopeStatus)
			continue
		}
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if idx == nil {
			scopeStatus.Failed = true
			scopeStatus.Error = "resolver returned no index"
			rep.Scopes = append(rep.Scopes, scopeStatus)
			continue
		}
		rep.ScopesRun++
		scopeStatus.Succeeded = true
		rep.Scopes = append(rep.Scopes, scopeStatus)
		scopeEdges := scip.CallEdges(idx, project, dir, enc)
		idx = nil
		kept, dropped, err := store.InsertEdges(scopeEdges)
		if err != nil {
			return rep, err
		}
		rep.EdgesKept += kept
		rep.EdgesDropped += dropped
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		scopeEdges = nil
		memory.Gate()
	}
	return rep, nil
}

// resolveGoCalls runs the in-process Go VTA resolver. Call only after TS scopes are
// done and gated — avoids overlapping the two largest memory spikes. The resolver
// can observe ctx before and after SSA/VTA, but x/tools' prog.Build is explicitly
// non-interruptible; the synchronous pipeline keeps Engine/store ownership until
// this call returns instead of detaching work during MCP shutdown.
func resolveGoCalls(ctx context.Context, project, root string, files []SourceFile, enc scip.Enclosing, changed map[string]bool) ([]graph.Edge, ResolverScopeStatus, error) {
	ctx = nonNilContext(ctx)
	scopeStatus := ResolverScopeStatus{Resolver: "go-vta", Scope: "go"}
	if err := ctx.Err(); err != nil {
		return nil, scopeStatus, err
	}
	// Applicability is established before any reuse decision. A changed map can
	// contain a stale "go=false" entry from a prior repository shape; reporting
	// Reused for a source-only or source-without-module repository would create an
	// unexpected production scope that ValidateExpected must never have to hide.
	if !hasGo(files) || !hasGoResolverConfig(root) {
		return nil, ResolverScopeStatus{}, nil
	}
	if changed != nil && !changed["go"] {
		scopeStatus.Reused = true
		return nil, scopeStatus, nil
	}
	scopeStatus.Attempted = true
	edges, err := goCallEdges(ctx, project, root, enc.Has)
	memory.Gate()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, scopeStatus, err
		}
		scopeStatus.Failed = true
		scopeStatus.Error = err.Error()
		log.Printf("codegraph: go calls skipped for %s: %v", root, err)
		return nil, scopeStatus, nil
	}
	scopeStatus.Succeeded = true
	return edges, scopeStatus, nil
}

func hasGo(files []SourceFile) bool {
	for _, f := range files {
		if f.Lang == LangGo {
			return true
		}
	}
	return false
}

func hasGoResolverConfig(root string) bool {
	for _, name := range []string{"go.mod", "go.work"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// tsconfigDirs finds the repo-relative directories scip-typescript should index,
// one per tsconfig.json. node_modules and hidden dirs are skipped. Monorepos have
// their tsconfigs in subprojects (apps/api, packages/x); a single-package repo
// (e.g. a TS library) has only a root tsconfig — in that case we return [""] to run
// scip at the root. When a root tsconfig and child configs coexist, the root is
// retained only when repository TS/JS files exist outside every child scope, so
// root-level calls are not silently dropped and child scopes are not duplicated.
func tsconfigDirs(root string) []string {
	dirs, _ := tsconfigDirsContext(context.Background(), root)
	return dirs
}

func tsconfigDirsContext(ctx context.Context, root string) ([]string, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var subDirs []string
	rootHas := false
	var sourceRels []string
	ignore, err := loadIgnore(root)
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			return classifyWalkError(root, path, ignore, walkErr)
		}
		rel := filepath.ToSlash(mustRel(root, path))
		if d.IsDir() {
			if shouldSkipDirectory(d.Name(), rel, ignore) {
				return filepath.SkipDir
			}
			return nil
		}
		if ignore.matchFile(rel) {
			return nil
		}
		if d.Name() == "tsconfig.json" {
			if err := verifyReadableSource(path); err != nil {
				return fmt.Errorf("read TypeScript config %q: %w", rel, err)
			}
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			if rel = filepath.ToSlash(rel); rel == "." {
				rootHas = true
			} else {
				subDirs = append(subDirs, rel)
			}
			return nil
		}
		if _, ok := langByExt[strings.ToLower(filepath.Ext(path))]; ok {
			if langByExt[strings.ToLower(filepath.Ext(path))] == LangTS ||
				langByExt[strings.ToLower(filepath.Ext(path))] == LangTSX ||
				langByExt[strings.ToLower(filepath.Ext(path))] == LangJS {
				sourceRels = append(sourceRels, rel)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(subDirs)
	if len(subDirs) == 0 && rootHas {
		return []string{""}, nil
	}
	if rootHas {
		for _, rel := range sourceRels {
			insideChild := false
			for _, dir := range subDirs {
				if rel == dir || strings.HasPrefix(rel, dir+"/") {
					insideChild = true
					break
				}
			}
			if !insideChild {
				subDirs = append(subDirs, "")
				break
			}
		}
	}
	sort.Strings(subDirs)
	return subDirs, nil
}
