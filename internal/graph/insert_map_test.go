package graph

import (
	"fmt"
	"path/filepath"
	"testing"
)

// seedPhaseNodes inserts n chainable function nodes (f00000..) in one project.
func seedPhaseNodes(t *testing.T, s *Store, project string, n int) {
	t.Helper()
	nodes := make([]Node, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, Node{
			Project: project, Label: LabelFunction, Name: fmt.Sprintf("f%05d", i),
			QualifiedName: fmt.Sprintf("%s:a.go.f%05d", project, i),
			FilePath:      "a.go", StartLine: 10 + i, EndLine: 11 + i,
			Props: map[string]any{"is_exported": false},
		})
	}
	if err := s.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
}

func chainEdges(project string, from, to int) []Edge {
	var out []Edge
	for i := from; i < to; i++ {
		out = append(out, Edge{
			Project:  project,
			SourceQN: fmt.Sprintf("%s:a.go.f%05d", project, i),
			TargetQN: fmt.Sprintf("%s:a.go.f%05d", project, i+1),
			Type:     EdgeCalls,
		})
	}
	return out
}

func openPhaseStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestInsertEdges_LateNodesResolve pins the "tardy nodes" hazard: edges
// inserted after NEW nodes arrive (routes, late passes) must resolve — the
// phase map is rebuilt explicitly on node mutation, never stale.
func TestInsertEdges_LateNodesResolve(t *testing.T) {
	s2 := openPhaseStore(t)
	seedPhaseNodes(t, s2, "p", 5)
	if k, d, err := s2.InsertEdges(chainEdges("p", 0, 4)); err != nil || k != 4 || d != 0 {
		t.Fatalf("batch1 = %d/%d, err=%v; want 4/0", k, d, err)
	}
	extra := []Node{{
		Project: "p", Label: LabelFunction, Name: "late",
		QualifiedName: "p:a.go.late", FilePath: "a.go", StartLine: 999, EndLine: 1000,
		Props: map[string]any{"is_exported": false},
	}}
	if err := s2.InsertNodes(extra); err != nil {
		t.Fatal(err)
	}
	late := []Edge{
		{Project: "p", SourceQN: "p:a.go.f00004", TargetQN: "p:a.go.late", Type: EdgeCalls},
		{Project: "p", SourceQN: "p:a.go.late", TargetQN: "p:a.go.f00000", Type: EdgeCalls},
	}
	if k, d, err := s2.InsertEdges(late); err != nil || k != 2 || d != 0 {
		t.Fatalf("late batch = %d/%d, err=%v; want 2/0 (stale map would drop)", k, d, err)
	}
	callers, err := s2.Neighbors("p", "p:a.go.late", "in", string(EdgeCalls), 10)
	if err != nil || len(callers) != 1 || callers[0].Name != "f00004" {
		t.Fatalf("late edge mislinked: %+v err=%v", callers, err)
	}
}

// TestInsertEdges_ProjectIsolation pins that the phase map never leaks across
// projects: interleaved inserts resolve within their own project.
func TestInsertEdges_ProjectIsolation(t *testing.T) {
	s := openPhaseStore(t)
	seedPhaseNodes(t, s, "p1", 3)
	seedPhaseNodes(t, s, "p2", 3)
	if k, _, err := s.InsertEdges(chainEdges("p1", 0, 2)); err != nil || k != 2 {
		t.Fatalf("p1 batch: k=%d err=%v", k, err)
	}
	if k, _, err := s.InsertEdges(chainEdges("p2", 0, 2)); err != nil || k != 2 {
		t.Fatalf("p2 batch: k=%d err=%v", k, err)
	}
	// Same QN tail, other project: must drop, not cross-link.
	ghost := []Edge{{Project: "p1", SourceQN: "p2:a.go.f00000", TargetQN: "p2:a.go.f00001", Type: EdgeCalls}}
	if _, d, err := s.InsertEdges(ghost); err != nil || d != 1 {
		t.Fatalf("cross-project edge must drop: d=%d err=%v", d, err)
	}
	for _, p := range []string{"p1", "p2"} {
		ns, err := s.Neighbors(p, p+":a.go.f00001", "in", string(EdgeCalls), 10)
		if err != nil || len(ns) != 1 || ns[0].QualifiedName != p+":a.go.f00000" {
			t.Fatalf("project %s mislinked: %+v err=%v", p, ns, err)
		}
	}
}

// TestInsertEdges_ReplaceProjectInvalidates pins id recycling: after a wipe,
// reinserted QNs get fresh ids and edges link the new rows — never the dead map.
func TestInsertEdges_ReplaceProjectInvalidates(t *testing.T) {
	s := openPhaseStore(t)
	seedPhaseNodes(t, s, "p", 3)
	if _, _, err := s.InsertEdges(chainEdges("p", 0, 2)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProject("p"); err != nil {
		t.Fatal(err)
	}
	seedPhaseNodes(t, s, "p", 3)
	if k, d, err := s.InsertEdges(chainEdges("p", 0, 2)); err != nil || k != 2 || d != 0 {
		t.Fatalf("post-wipe batch = %d/%d, err=%v; want 2/0", k, d, err)
	}
	ns, err := s.Neighbors("p", "p:a.go.f00002", "in", string(EdgeCalls), 10)
	if err != nil || len(ns) != 1 || ns[0].Name != "f00001" {
		t.Fatalf("post-wipe edge mislinked: %+v err=%v", ns, err)
	}
}

// TestInsertEdges_DroppedDuplicatesPreserved pins the accounting contract the
// pipeline relies on: unknown endpoints and duplicates count as dropped,
// inserted counts only new rows.
func TestInsertEdges_DroppedDuplicatesPreserved(t *testing.T) {
	s := openPhaseStore(t)
	seedPhaseNodes(t, s, "p", 3)
	batch := append(chainEdges("p", 0, 2),
		Edge{Project: "p", SourceQN: "p:a.go.f00000", TargetQN: "p:a.go.ghost", Type: EdgeCalls},
		Edge{Project: "p", SourceQN: "p:a.go.ghost", TargetQN: "p:a.go.f00001", Type: EdgeCalls},
	)
	if k, d, err := s.InsertEdges(batch); err != nil || k != 2 || d != 2 {
		t.Fatalf("first insert = %d/%d, err=%v; want 2/2", k, d, err)
	}
	if k, d, err := s.InsertEdges(chainEdges("p", 0, 2)); err != nil || k != 0 || d != 2 {
		t.Fatalf("reinsert = %d/%d, err=%v; want 0/2 duplicates dropped", k, d, err)
	}
}

// BenchmarkInsertEdges_PhasedReuse measures repeated edge phases over a stable
// node set: the QN→id map builds once, not once per call.
func BenchmarkInsertEdges_PhasedReuse(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "g.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	const n = 10000
	nodes := make([]Node, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, Node{
			Project: "p", Label: LabelFunction, Name: fmt.Sprintf("f%05d", i),
			QualifiedName: fmt.Sprintf("p:a.go.f%05d", i),
			FilePath:      "a.go", StartLine: 10 + i, EndLine: 11 + i,
		})
	}
	if err := s.InsertNodes(nodes); err != nil {
		b.Fatal(err)
	}
	batch := chainEdges("p", 0, 500)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.InsertEdges(batch); err != nil {
			b.Fatal(err)
		}
	}
}
