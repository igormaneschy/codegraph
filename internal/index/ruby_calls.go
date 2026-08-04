package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/scip"
)

// rubyCallTargets is the storage boundary for the Ruby resolver. The production
// implementation is the graph store query; the seam makes storage-failure status
// deterministic without weakening the narrow no-heuristic resolver policy.
var rubyCallTargets = func(store *graph.Store, project string) ([]graph.RubyCallTarget, error) {
	return store.RubySingletonCallTargets(project)
}

// resolveRubyCalls emits only calls whose receiver is an absolute Ruby constant
// and whose singleton method has one explicit repository declaration. This is a
// deliberately small static subset: no inferred receiver types, lexical constant
// lookup, dynamic dispatch, send, or Rails metaprogramming can create an edge.
func resolveRubyCalls(ctx context.Context, store *graph.Store, project string, files []SourceFile, enc scip.Enclosing, changed map[string]bool) ([]graph.Edge, ResolverScopeStatus, error) {
	ctx = nonNilContext(ctx)
	scopeStatus := ResolverScopeStatus{Resolver: "ruby-static", Scope: "ruby"}
	if err := ctx.Err(); err != nil {
		return nil, scopeStatus, err
	}
	// Do not let an old changed-map entry manufacture a Ruby scope for a
	// repository that has no Ruby input. Applicability must precede Reused so
	// the production report is exact rather than repaired by downstream filtering.
	if !hasRuby(files) {
		return nil, ResolverScopeStatus{}, nil
	}
	if changed != nil && !changed["ruby"] {
		scopeStatus.Reused = true
		return nil, scopeStatus, nil
	}
	scopeStatus.Attempted = true
	targets, err := rubyCallTargets(store, project)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, scopeStatus, err
		}
		scopeStatus.Failed = true
		scopeStatus.Error = fmt.Errorf("load Ruby call targets: %w", err).Error()
		return nil, scopeStatus, nil
	}
	byOwnerAndName := uniqueRubyCallTargets(targets)
	if len(byOwnerAndName) == 0 {
		scopeStatus.Succeeded = true
		return nil, scopeStatus, nil
	}

	var edges []graph.Edge
	seen := map[string]bool{}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, scopeStatus, err
		}
		if f.Lang != LangRuby {
			continue
		}
		src, err := os.ReadFile(f.AbsPath)
		if err != nil {
			scopeStatus.Failed = true
			scopeStatus.Error = fmt.Errorf("read Ruby source %q: %w", f.RelPath, err).Error()
			return nil, scopeStatus, nil
		}
		for _, edge := range rubyCallEdgesInSource(project, f.RelPath, src, enc, byOwnerAndName) {
			key := edge.SourceQN + "\x00" + edge.TargetQN
			if !seen[key] {
				seen[key] = true
				edges = append(edges, edge)
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, scopeStatus, err
		}
	}
	scopeStatus.Succeeded = true
	return edges, scopeStatus, nil
}

func uniqueRubyCallTargets(targets []graph.RubyCallTarget) map[string]string {
	out := make(map[string]string, len(targets))
	for _, target := range targets {
		key := target.Owner + "\x00" + target.Name
		if existing, ok := out[key]; ok && existing != target.QualifiedName {
			out[key] = "" // reopened/duplicated declaration: do not choose a target
			continue
		}
		out[key] = target.QualifiedName
	}
	return out
}

func rubyCallEdgesInSource(project, relPath string, src []byte, enc scip.Enclosing, targets map[string]string) []graph.Edge {
	// Definitions are intentionally parsed and persisted before relationships. This
	// second, bounded parse keeps that phase boundary and avoids retaining ASTs for
	// every Ruby file in memory between pipeline passes.
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(rubyLang); err != nil {
		return nil
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()

	var edges []graph.Edge
	var walk func(*tree_sitter.Node)
	walk = func(n *tree_sitter.Node) {
		if n.Kind() == "call" {
			receiver := rubyNodeName(n.ChildByFieldName("receiver"), src)
			method := rubyNodeName(n.ChildByFieldName("method"), src)
			if owner, ok := rubyAbsoluteConstant(receiver); ok {
				if targetQN := targets[owner+"\x00"+method]; targetQN != "" {
					if callerQN, ok := enc.At(relPath, int(n.StartPosition().Row)+1); ok {
						edges = append(edges, graph.Edge{
							Project: project, SourceQN: callerQN, TargetQN: targetQN, Type: graph.EdgeCalls,
							Props: map[string]any{"resolver": "ruby-static", "confidence": "high", "evidence": "absolute_constant_receiver"},
						})
					}
				}
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())
	return edges
}

func rubyAbsoluteConstant(receiver string) (string, bool) {
	if !strings.HasPrefix(receiver, "::") {
		return "", false
	}
	owner := strings.TrimPrefix(receiver, "::")
	return owner, rubyConstantName(owner)
}
