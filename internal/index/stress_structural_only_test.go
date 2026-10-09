package index

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

func TestGoStressCallsStoredDefinesCannotSubstituteForCalls(t *testing.T) {
	store := twoFileStructuralStressGraph(t)
	nodes, edges, err := store.Stats("fixture")
	if err != nil || nodes != 6 || edges != 4 {
		t.Fatalf("structural graph=%d nodes %d edges err=%v", nodes, edges, err)
	}
	result := healthyStressResult()
	result.Nodes = nodes
	result.EdgesKept = edges
	count, err := validateGoStressCalls(result, store, 2)
	if err == nil || count != 0 || !strings.Contains(err.Error(), "missing 4 expected CALLS") {
		t.Fatalf("structural graph passed the CALLS oracle: count=%d err=%v", count, err)
	}
}

func twoFileStructuralStressGraph(t *testing.T) *graph.Store {
	t.Helper()
	store, err := graph.Open(filepath.Join(t.TempDir(), "structural.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	nodes := []graph.Node{
		{Project: "fixture", QualifiedName: "fixture:stress/f0.go", Label: graph.LabelFile, Name: "f0.go", FilePath: "stress/f0.go"},
		{Project: "fixture", QualifiedName: "fixture:stress/f1.go", Label: graph.LabelFile, Name: "f1.go", FilePath: "stress/f1.go"},
		{Project: "fixture", QualifiedName: "fixture:stress/f0.go.Fn0", Label: graph.LabelFunction, Name: "Fn0", FilePath: "stress/f0.go"},
		{Project: "fixture", QualifiedName: "fixture:stress/f0.go.local0", Label: graph.LabelFunction, Name: "local0", FilePath: "stress/f0.go"},
		{Project: "fixture", QualifiedName: "fixture:stress/f1.go.Fn1", Label: graph.LabelFunction, Name: "Fn1", FilePath: "stress/f1.go"},
		{Project: "fixture", QualifiedName: "fixture:stress/f1.go.local1", Label: graph.LabelFunction, Name: "local1", FilePath: "stress/f1.go"},
	}
	if err := store.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes[2:] {
		inserted, dropped, err := store.InsertEdges([]graph.Edge{{Project: "fixture", SourceQN: "fixture:" + node.FilePath, TargetQN: node.QualifiedName, Type: graph.EdgeDefines}})
		if err != nil || inserted != 1 || dropped != 0 {
			t.Fatalf("DEFINES insert=%d dropped=%d err=%v", inserted, dropped, err)
		}
	}
	return store
}
