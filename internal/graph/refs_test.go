package graph

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestRefs_MatchFullNodeQueries pins P3's contract: the compact projection
// returns exactly the same refs (order, fields) as the full-node paged queries,
// so dropping the properties decode is not observable.
func TestRefs_MatchFullNodeQueries(t *testing.T) {
	s := openPhaseStore(t)
	seedPhaseNodes(t, s, "p", 40)
	if _, _, err := s.InsertEdges(chainEdges("p", 0, 39)); err != nil {
		t.Fatal(err)
	}

	hits, err := s.SearchPage("p", "f000", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := s.SearchRefs("p", "f000", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != len(refs) {
		t.Fatalf("search length differs: full=%d compact=%d", len(hits), len(refs))
	}
	for i := range hits {
		if hits[i].Node.QualifiedName != refs[i].QualifiedName || hits[i].Node.Name != refs[i].Name ||
			hits[i].Node.Label != refs[i].Label || hits[i].Node.FilePath != refs[i].FilePath ||
			hits[i].Node.StartLine != refs[i].StartLine || hits[i].Node.EndLine != refs[i].EndLine {
			t.Fatalf("search ref %d differs: full=%+v compact=%+v", i, hits[i].Node, refs[i])
		}
	}

	for _, direction := range []string{"in", "out", "both"} {
		nodes, err := s.NeighborsPage("p", "p:a.go.f00020", direction, "CALLS", 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		compact, err := s.NeighborRefs("p", "p:a.go.f00020", direction, "CALLS", 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != len(compact) {
			t.Fatalf("%s length differs: full=%d compact=%d", direction, len(nodes), len(compact))
		}
		for i := range nodes {
			if nodes[i].QualifiedName != compact[i].QualifiedName || nodes[i].FilePath != compact[i].FilePath ||
				nodes[i].StartLine != compact[i].StartLine || nodes[i].EndLine != compact[i].EndLine {
				t.Fatalf("%s ref %d differs: full=%+v compact=%+v", direction, i, nodes[i], compact[i])
			}
		}
	}
}

// TestNeighborRefs_RejectsUnknownDirection pins the validation the shared query
// builder now enforces (a bad direction used to fall through to "out").
func TestNeighborRefs_RejectsUnknownDirection(t *testing.T) {
	s := openPhaseStore(t)
	seedPhaseNodes(t, s, "p", 2)
	if _, err := s.NeighborRefs("p", "p:a.go.f00001", "sideways", "", 10, 0); err == nil ||
		!strings.Contains(err.Error(), "invalid direction") {
		t.Fatalf("unknown direction error=%v, want an actionable rejection", err)
	}
	if _, err := s.NeighborsPage("p", "p:a.go.f00001", "sideways", "", 10, 0); err == nil {
		t.Fatal("the full-node query must reject an unknown direction too")
	}
}

// refBenchStore seeds one hub with `callers` callers, each carrying realistic
// properties, and returns the hub's qualified name.
func refBenchStore(b testing.TB, callers int) (*Store, string) {
	b.Helper()
	s, err := Open(b.TempDir() + "/g.db")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	const p = "p"
	hub := Node{Project: p, Label: LabelFunction, Name: "hub", QualifiedName: p + ":a.go.hub",
		FilePath: "a.go", StartLine: 1, EndLine: 2, Props: map[string]any{"complexity": 3, "is_exported": true}}
	if err := s.InsertNodes([]Node{hub}); err != nil {
		b.Fatal(err)
	}
	nodes := make([]Node, 0, callers)
	edges := make([]Edge, 0, callers)
	for i := 0; i < callers; i++ {
		name := fmt.Sprintf("caller%05d", i)
		qn := p + ":a.go." + name
		nodes = append(nodes, Node{Project: p, Label: LabelFunction, Name: name, QualifiedName: qn,
			FilePath: "a.go", StartLine: 10 + i, EndLine: 30 + i,
			Props: map[string]any{"complexity": i % 20, "is_exported": false, "decorators": []any{"Get", "Post"}, "lang": "go"}})
		edges = append(edges, Edge{Project: p, SourceQN: qn, TargetQN: hub.QualifiedName, Type: EdgeCalls})
	}
	if err := s.InsertNodes(nodes); err != nil {
		b.Fatal(err)
	}
	if _, _, err := s.InsertEdges(edges); err != nil {
		b.Fatal(err)
	}
	return s, hub.QualifiedName
}

// BenchmarkNeighborRefs_Compact measures the compact projection.
func BenchmarkNeighborRefs_Compact(b *testing.B) {
	s, hub := refBenchStore(b, 500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.NeighborRefs("p", hub, "in", "CALLS", 500, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkNeighborRefs_FullNode measures the previous full-node path (properties
// selected and JSON-decoded) for comparison.
func BenchmarkNeighborRefs_FullNode(b *testing.B) {
	s, hub := refBenchStore(b, 500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.NeighborsPage("p", hub, "in", "CALLS", 500, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSearchRefs_Compact / FullNode are the same comparison for search.
func BenchmarkSearchRefs_Compact(b *testing.B) {
	s, _ := refBenchStore(b, 500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.SearchRefs("p", "caller", "", 200, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchRefs_FullNode(b *testing.B) {
	s, _ := refBenchStore(b, 500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.SearchPage("p", "caller", "", 200, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkNeighborRefs_FirstPage / DeepPage measure the LIMIT/OFFSET cost on a
// large caller set: if the deep page is much slower, a keyset cursor is worth it.
func BenchmarkNeighborRefs_FirstPage(b *testing.B) {
	s, hub := refBenchStore(b, 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.NeighborRefs("p", hub, "in", "CALLS", 500, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNeighborRefs_DeepPage(b *testing.B) {
	s, hub := refBenchStore(b, 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.NeighborRefs("p", hub, "in", "CALLS", 500, 4500); err != nil {
			b.Fatal(err)
		}
	}
}

// TestNeighborRefsAfter_MatchesOffsetPaging pins P2's keyset contract: walking
// keyset pages returns exactly the same ordered refs as walking LIMIT/OFFSET
// pages, across every direction (including the "both" UNION dedup).
func TestNeighborRefsAfter_MatchesOffsetPaging(t *testing.T) {
	s := openPhaseStore(t)
	seedPhaseNodes(t, s, "p", 60)
	hub := "p:a.go.f00040"
	var edges []Edge
	for i := 0; i < 60; i++ {
		if i == 40 {
			continue
		}
		other := fmt.Sprintf("p:a.go.f%05d", i)
		edges = append(edges,
			Edge{Project: "p", SourceQN: other, TargetQN: hub, Type: EdgeCalls},
			Edge{Project: "p", SourceQN: hub, TargetQN: other, Type: EdgeCalls})
	}
	if _, _, err := s.InsertEdges(edges); err != nil {
		t.Fatal(err)
	}

	const pageSize = 7
	for _, direction := range []string{"in", "out", "both"} {
		var keyset []string
		after := ""
		for {
			page, err := s.NeighborRefsAfter("p", hub, direction, "CALLS", after, pageSize)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range page {
				keyset = append(keyset, r.QualifiedName)
			}
			if len(page) < pageSize {
				break
			}
			after = page[len(page)-1].QualifiedName
		}

		var offset []string
		for off := 0; ; off += pageSize {
			page, err := s.NeighborRefs("p", hub, direction, "CALLS", pageSize, off)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range page {
				offset = append(offset, r.QualifiedName)
			}
			if len(page) < pageSize {
				break
			}
		}

		if !slices.Equal(keyset, offset) {
			t.Fatalf("%s: keyset=%v offset=%v", direction, keyset, offset)
		}
		if len(keyset) == 0 {
			t.Fatalf("%s: empty enumeration", direction)
		}
	}
}

// BenchmarkNeighborRefsAfter_DeepPage is the keyset counterpart of
// BenchmarkNeighborRefs_DeepPage: continuing after a name should cost the same as
// the first page.
func BenchmarkNeighborRefsAfter_DeepPage(b *testing.B) {
	s, hub := refBenchStore(b, 5000)
	seed, err := s.NeighborRefs("p", hub, "in", "CALLS", 1, 4500)
	if err != nil || len(seed) != 1 {
		b.Fatalf("seed row: %v", err)
	}
	after := seed[0].QualifiedName
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.NeighborRefsAfter("p", hub, "in", "CALLS", after, 500); err != nil {
			b.Fatal(err)
		}
	}
}
