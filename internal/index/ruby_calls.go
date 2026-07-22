package index

import (
	"os"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/scip"
)

// rubyStaticCallsVersion invalidates a previously structural-only Ruby graph once
// when the verified static-call contract changes.
const rubyStaticCallsVersion = 1

// resolveRubyCalls emits only calls whose receiver is an absolute Ruby constant
// and whose singleton method has one explicit repository declaration. This is a
// deliberately small static subset: no inferred receiver types, lexical constant
// lookup, dynamic dispatch, send, or Rails metaprogramming can create an edge.
func resolveRubyCalls(store *graph.Store, project string, files []SourceFile, enc scip.Enclosing, changed map[string]bool) ([]graph.Edge, error) {
	if changed != nil && !changed["ruby"] {
		return nil, nil
	}
	targets, err := store.RubySingletonCallTargets(project)
	if err != nil {
		return nil, err
	}
	byOwnerAndName := uniqueRubyCallTargets(targets)
	if len(byOwnerAndName) == 0 {
		return nil, nil
	}

	var edges []graph.Edge
	seen := map[string]bool{}
	for _, f := range files {
		if f.Lang != LangRuby {
			continue
		}
		src, err := os.ReadFile(f.AbsPath)
		if err != nil {
			continue // structural indexing remains usable when a file becomes unreadable
		}
		for _, edge := range rubyCallEdgesInSource(project, f.RelPath, src, enc, byOwnerAndName) {
			key := edge.SourceQN + "\x00" + edge.TargetQN
			if !seen[key] {
				seen[key] = true
				edges = append(edges, edge)
			}
		}
	}
	return edges, nil
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
