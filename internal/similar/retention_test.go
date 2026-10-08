package similar

import (
	"fmt"
	"sort"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

// TestTopEdges_MatchesSortAndTruncate pins P4's core guarantee: bounded retention
// returns exactly the set that collecting every edge, sorting, and truncating to
// the cap produced — the change is memory, not output.
func TestTopEdges_MatchesSortAndTruncate(t *testing.T) {
	all := make([]graph.Edge, 0, 500)
	for i := 0; i < 500; i++ {
		all = append(all, graph.Edge{
			SourceQN: fmt.Sprintf("s%03d", i),
			TargetQN: fmt.Sprintf("d%03d", (i*7)%500),
			Type:     graph.EdgeSimilarTo,
		})
	}
	const capacity = 40

	reference := append([]graph.Edge(nil), all...)
	sort.Slice(reference, func(i, j int) bool { return edgeLess(reference[i], reference[j]) })
	want := reference[:capacity]

	keep := &topEdges{cap: capacity}
	for _, e := range all {
		keep.add(e)
	}
	got := keep.sorted()
	if len(got) != len(want) {
		t.Fatalf("retained %d edges, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].SourceQN != want[i].SourceQN || got[i].TargetQN != want[i].TargetQN {
			t.Fatalf("edge %d = %s->%s, want %s->%s", i, got[i].SourceQN, got[i].TargetQN, want[i].SourceQN, want[i].TargetQN)
		}
	}
	if !keep.overflow {
		t.Fatal("overflow must be reported once an edge is dropped")
	}
}

// TestTopEdges_ExactFitIsNotOverflow pins that a pass at exactly the cap is not
// reported as capped (coverage must not claim truncation it did not do).
func TestTopEdges_ExactFitIsNotOverflow(t *testing.T) {
	keep := &topEdges{cap: 3}
	for _, e := range []graph.Edge{{SourceQN: "a"}, {SourceQN: "b"}, {SourceQN: "c"}} {
		keep.add(e)
	}
	if keep.overflow {
		t.Fatal("an exact fit must not report overflow")
	}
	if len(keep.sorted()) != 3 {
		t.Fatalf("retained %d, want 3", len(keep.sorted()))
	}
}
