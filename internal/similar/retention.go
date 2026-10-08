package similar

import (
	"container/heap"
	"sort"

	"github.com/Lordymine/codegraph/internal/graph"
)

// topEdges retains the cap smallest SIMILAR_TO edges by (source, target) while the
// pair scan runs. Collecting into one slice and truncating after the sort keeps
// MaxPairs edges in memory only to discard most of them; a bounded max-heap keeps
// the exact same retained set at O(cap) memory (P4). Because the retained set is
// identical, the emitted edges (and the coverage status) are unchanged.
type topEdges struct {
	cap      int
	heap     edgeMaxHeap
	overflow bool
}

// add records one candidate. overflow becomes true once any candidate is dropped,
// which is the "output was capped" signal the coverage reports.
func (t *topEdges) add(e graph.Edge) {
	if t.cap <= 0 {
		t.overflow = true
		return
	}
	if len(t.heap) < t.cap {
		heap.Push(&t.heap, e)
		return
	}
	if edgeLess(e, t.heap[0]) {
		t.heap[0] = e
		heap.Fix(&t.heap, 0)
	}
	t.overflow = true
}

// sorted returns the retained edges in ascending (source, target) order.
func (t *topEdges) sorted() []graph.Edge {
	out := make([]graph.Edge, len(t.heap))
	copy(out, t.heap)
	sort.Slice(out, func(i, j int) bool { return edgeLess(out[i], out[j]) })
	return out
}

// edgeLess orders edges by (source, target) — the canonical output order.
func edgeLess(a, b graph.Edge) bool {
	if a.SourceQN != b.SourceQN {
		return a.SourceQN < b.SourceQN
	}
	return a.TargetQN < b.TargetQN
}

// edgeMaxHeap is a max-heap over edgeLess, so the root is the largest retained
// edge and is the one evicted when a smaller candidate arrives.
type edgeMaxHeap []graph.Edge

func (h edgeMaxHeap) Len() int           { return len(h) }
func (h edgeMaxHeap) Less(i, j int) bool { return edgeLess(h[j], h[i]) }
func (h edgeMaxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *edgeMaxHeap) Push(x any) { *h = append(*h, x.(graph.Edge)) }

func (h *edgeMaxHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	*h = old[:n-1]
	return e
}
