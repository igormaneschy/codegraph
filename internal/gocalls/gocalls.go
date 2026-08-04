// Package gocalls resolves Go CALLS edges in-process via go/packages + a VTA
// (Variable Type Analysis) call graph — the same machinery gopls
// uses, no subprocess, no main needed. VTA tracks the concrete types each variable
// can hold, so an interface call dispatched on a known type does not over-approximate
// to every implementation of that interface (CHA's main imprecision). Only edges
// between functions/methods that exist in the node set are kept (honest precision);
// stdlib/dep targets are dropped. Test files are loaded (packages Tests:true) so
// calls from *_test.go contribute edges too — a major recall source for "who calls X".
package gocalls

import (
	"context"
	"fmt"
	"go/types"
	"log"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/Lordymine/codegraph/internal/graph"
)

// resolverVersion is part of the graph freshness identity. It covers the
// in-process Go resolver and its VTA admission rules, not only x/tools' module
// version.
const resolverVersion = "go-vta-resolver-v2"

// ResolverVersion returns the version recorded in an index manifest.
func ResolverVersion() string { return resolverVersion }

// CallEdges loads the Go packages under root and returns CALLS edges whose caller
// and callee are both known nodes. Package diagnostics and resolver panics are
// returned as failures; the indexer may commit structural-only output on a
// first run, but never certifies a partial resolver result as healthy.
func CallEdges(project, root string, known func(qn string) bool) (edges []graph.Edge, err error) {
	return CallEdgesContext(context.Background(), project, root, known)
}

// CallEdgesContext passes cancellation to go/packages and checks it around the
// SSA/VTA phases, which do not expose a context-aware API. A canceled context
// is returned instead of being downgraded to the resolver's best-effort path.
func CallEdgesContext(ctx context.Context, project, root string, known func(qn string) bool) (edges []graph.Edge, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Defense in depth: the call-graph build avoids prog.RuntimeTypes() (the known
	// generics panic — see safeAllFunctions), but recover anyway so any other
	// x/tools edge case degrades to "no Go CALLS for this repo" rather than crashing
	// the whole index.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("codegraph: go call graph panic in %s: %v", root, r)
			edges = nil
			err = fmt.Errorf("go call graph: %v", r)
		}
	}()

	// Tests:true loads *_test.go too, so test functions contribute caller/callee
	// edges (the dominant recall source for "who calls X" on library code).
	cfg := &packages.Config{Context: ctx, Mode: packages.LoadAllSyntax, Tests: true, Dir: root}
	pkgs, loadErr := packages.Load(cfg, "./...")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if loadErr != nil {
		return nil, loadErr
	}
	if err := packageLoadErrors(pkgs); err != nil {
		return nil, err
	}
	prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prog.Build()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// VTA refines a CHA seed using the concrete types each variable can hold — far
	// fewer false interface-dispatch edges than CHA alone, and no main needed. We use
	// our own safeAllFunctions + chaCallGraph (see cha.go) instead of
	// ssautil.AllFunctions + cha.CallGraph: both upstream helpers call
	// prog.RuntimeTypes(), which panics on generic instantiations ("ForEachElement …
	// *types.TypeParam", x/tools v0.46) and silently zeroed Go CALLS for generic-heavy
	// repos (gh-cli). Our copies are RuntimeTypes-free; the CHA seed still gives VTA
	// the candidate set it refines, so precision is unchanged on non-generic repos.
	fns := safeAllFunctions(prog)
	cg := vta.CallGraph(fns, chaCallGraph(fns))
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	for fn, node := range cg.Nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Credit a call to the named function that lexically contains it: a call
		// written inside a function literal (cobra's Run: func(){...}) lives in an
		// anonymous closure that is not a graph node, so without this its edges are
		// dropped — the dominant "who calls X" recall hole. Matches IDE find-callers.
		callerQN, ok := funcToQN(enclosingNamed(fn), project, root)
		if !ok || !known(callerQN) {
			continue
		}
		for _, out := range node.Out {
			calleeQN, ok := funcToQN(out.Callee.Func, project, root)
			// A self-edge (recursion) is kept: a function that calls itself is a real
			// caller of itself, which the eval oracle and IDE find-callers both count.
			if !ok || !known(calleeQN) {
				continue
			}
			key := callerQN + "\x00" + calleeQN
			if seen[key] {
				continue
			}
			seen[key] = true
			edges = append(edges, graph.Edge{
				Project: project, SourceQN: callerQN, TargetQN: calleeQN,
				Type:  graph.EdgeCalls,
				Props: map[string]any{"resolver": "vta", "confidence": "high"},
			})
		}
	}
	return edges, nil
}

// packageLoadErrors catches diagnostics attached to individual packages. A
// nil packages.Load error does not guarantee a complete type environment; if
// any package failed, VTA over the remaining packages would be a silently
// partial resolver result and must be reported as a failure instead.
func packageLoadErrors(pkgs []*packages.Package) error {
	var messages []string
	for _, pkg := range pkgs {
		for _, pkgErr := range pkg.Errors {
			message := pkg.PkgPath + ": " + pkgErr.Msg
			if pkgErr.Pos != "" {
				message += " (" + pkgErr.Pos + ")"
			}
			messages = append(messages, message)
			if len(messages) == 8 {
				return fmt.Errorf("go package load failed: %s", strings.Join(messages, "; "))
			}
		}
	}
	if len(messages) == 0 {
		return nil
	}
	return fmt.Errorf("go package load failed: %s", strings.Join(messages, "; "))
}

// enclosingNamed walks up from an anonymous function (closure) to the named
// function or method that lexically contains it; ssa.Function.Parent() is non-nil
// only for anonymous functions, so a package-level function or method is returned
// unchanged. Calls written inside a closure are thus credited to that named parent.
func enclosingNamed(fn *ssa.Function) *ssa.Function {
	for fn != nil && fn.Parent() != nil {
		fn = fn.Parent()
	}
	return fn
}

// funcToQN maps an SSA function to a codegraph qualified name, matching the M1
// Go scheme: "<project>:<relpath>.<RecvType>.<name>" for methods,
// "<project>:<relpath>.<name>" for functions. Returns false for synthetic
// functions and anything outside the repo (stdlib/deps). Callers pass the result
// of enclosingNamed, so closures arrive already mapped to their named parent.
func funcToQN(fn *ssa.Function, project, root string) (string, bool) {
	if fn == nil || fn.Pkg == nil || fn.Synthetic != "" {
		return "", false
	}
	pos := fn.Prog.Fset.Position(fn.Pos())
	if !pos.IsValid() {
		return "", false
	}
	rel, err := filepath.Rel(root, pos.Filename)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "../") {
		return "", false // outside the repo
	}
	name := fn.Name()
	if recv := fn.Signature.Recv(); recv != nil {
		rt := recvTypeName(recv.Type())
		if rt == "" {
			return "", false
		}
		return project + ":" + rel + "." + rt + "." + name, true
	}
	return project + ":" + rel + "." + name, true
}

func recvTypeName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	return ""
}
