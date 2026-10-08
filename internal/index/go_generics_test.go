package index

import (
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

// TestGoGenericMethodQualifiedName pins the node side of R07: a generic receiver
// `Box[T]` must yield the nominal QN `<file>.Box.Get`, matching the SSA resolver's
// receiver type. Leaking the type parameter (`Box[T].Get`) drops every CALLS edge
// to and from the method.
func TestGoGenericMethodQualifiedName(t *testing.T) {
	src := `package sample

type Box[T any] struct{ v T }

func (b Box[T]) Get() T { return b.v }

func (b *Box[T]) Set(v T) { b.v = v }

func Map[T any](in []T) []T { return in }
`
	nodes, _ := extractDefsFromSource("proj", "box.go", LangGo, []byte(src))
	methods := map[string]graph.Node{}
	for _, n := range nodes {
		if n.Label == graph.LabelMethod {
			methods[n.QualifiedName] = n
		}
	}
	for _, want := range []string{"proj:box.go.Box.Get", "proj:box.go.Box.Set"} {
		if _, ok := methods[want]; !ok {
			t.Errorf("missing method QN %q; got %v", want, keysOfNodes(methods))
		}
	}
	for qn, n := range methods {
		if strings.Contains(qn, "[") {
			t.Errorf("method QN %q leaks type parameters", qn)
		}
		if n.Props["receiver"] != "Box" {
			t.Errorf("method %q receiver prop = %v, want Box", qn, n.Props["receiver"])
		}
	}
}

func keysOfNodes(nodes map[string]graph.Node) []string {
	out := make([]string, 0, len(nodes))
	for qn := range nodes {
		out = append(out, qn)
	}
	return out
}
