package graph

import (
	"strings"
	"testing"
)

func TestInsertEdges_InvalidBatchLeavesExistingGraphUnchanged(t *testing.T) {
	for _, invalid := range []struct {
		name, project, source, target, errorText string
	}{
		{"mixed projects", "q", "p:a.go.f00001", "p:a.go.f00002", `expected project "p"`},
		{"mixed projects with own endpoints", "q", "q:a.go.f00001", "q:a.go.f00002", `expected project "p"`},
		{"empty project", "", "p:a.go.f00001", "p:a.go.f00002", `project ""`},
		{"empty source", "p", "", "p:a.go.f00002", `source=""`},
		{"empty target", "p", "p:a.go.f00001", "", `target=""`},
		{"blank project", " ", "p:a.go.f00001", "p:a.go.f00002", `project " "`},
		{"blank source", "p", " ", "p:a.go.f00002", `source=" "`},
		{"blank target", "p", "p:a.go.f00001", " ", `target=" "`},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			store := openPhaseStore(t)
			seedPhaseNodes(t, store, "p", 4)
			seedPhaseNodes(t, store, "q", 3)
			if inserted, dropped, err := store.InsertEdges(chainEdges("p", 2, 3)); err != nil || inserted != 1 || dropped != 0 {
				t.Fatalf("seed edge=%d/%d err=%v, want 1/0 nil", inserted, dropped, err)
			}
			before, err := store.LogicalGraphDigest("p")
			if err != nil {
				t.Fatal(err)
			}
			batch := append(chainEdges("p", 0, 1), Edge{Project: invalid.project, SourceQN: invalid.source, TargetQN: invalid.target, Type: EdgeCalls})
			inserted, dropped, err := store.InsertEdges(batch)
			if err == nil || !strings.Contains(err.Error(), "edge 1") || !strings.Contains(err.Error(), invalid.errorText) || inserted != 0 || dropped != 0 {
				t.Fatalf("invalid batch=%d/%d err=%v; want contextual error and 0/0", inserted, dropped, err)
			}
			after, err := store.LogicalGraphDigest("p")
			if err != nil || after != before {
				t.Fatalf("invalid batch changed graph: before=%q after=%q err=%v", before, after, err)
			}
			if _, count, err := store.Stats("q"); err != nil || count != 0 {
				t.Fatalf("invalid batch persisted foreign edges: count=%d err=%v", count, err)
			}
			if err := store.ValidateIntegrity(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInsertEdges_EmptyProjectRejectedAtFirstItem(t *testing.T) {
	store := openPhaseStore(t)
	inserted, dropped, err := store.InsertEdges([]Edge{{SourceQN: "a", TargetQN: "b", Type: EdgeCalls}})
	if err == nil || !strings.Contains(err.Error(), `edge 0: project ""`) || inserted != 0 || dropped != 0 {
		t.Fatalf("empty project=%d/%d err=%v; want named validation error", inserted, dropped, err)
	}
}

func TestInsertEdges_EmptyBatchIsNoOp(t *testing.T) {
	store := openPhaseStore(t)
	if inserted, dropped, err := store.InsertEdges(nil); err != nil || inserted != 0 || dropped != 0 {
		t.Fatalf("empty batch=%d/%d err=%v, want 0/0 nil", inserted, dropped, err)
	}
}
