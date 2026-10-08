package graph

import (
	"testing"
)

func TestInsertNodes_DuplicatesPreserveSearchAndIntegrity(t *testing.T) {
	for _, betweenBatches := range []bool{false, true} {
		name := "same batch"
		if betweenBatches {
			name = "between batches"
		}
		t.Run(name, func(t *testing.T) {
			store := openPhaseStore(t)
			alpha := Node{Project: "p", Label: LabelFunction, Name: "Alpha", QualifiedName: "p:a.go.Alpha", FilePath: "a.go", StartLine: 1, EndLine: 2}
			beta := Node{Project: "p", Label: LabelFunction, Name: "Beta", QualifiedName: "p:a.go.Beta", FilePath: "a.go", StartLine: 3, EndLine: 4}
			duplicate := alpha
			duplicate.Name, duplicate.StartLine = "Ignored", 9
			batch := []Node{alpha, beta, alpha, duplicate}
			if betweenBatches {
				if err := store.InsertNodes(batch[:2]); err != nil {
					t.Fatal(err)
				}
				batch = batch[2:]
			}
			if err := store.InsertNodes(batch); err != nil {
				t.Fatal(err)
			}
			for _, want := range []Node{alpha, beta} {
				hits, err := store.Search("p", want.Name, "", 10)
				if err != nil || len(hits) != 1 || hits[0].Node.QualifiedName != want.QualifiedName || hits[0].Node.StartLine != want.StartLine {
					t.Fatalf("search %q = %+v, err=%v; want original %+v", want.Name, hits, err, want)
				}
			}
			if hits, err := store.Search("p", "Ignored", "", 10); err != nil || len(hits) != 0 {
				t.Fatalf("ignored declaration is searchable: hits=%+v err=%v", hits, err)
			}
			if count, _, err := store.Stats("p"); err != nil || count != 2 {
				t.Fatalf("nodes=%d err=%v, want 2", count, err)
			}
			if err := store.ValidateIntegrity(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInsertNodes_SameQualifiedNameInDifferentProjects(t *testing.T) {
	store := openPhaseStore(t)
	nodes := []Node{
		{Project: "p", Label: LabelFunction, Name: "Alpha", QualifiedName: "shared", FilePath: "a.go"},
		{Project: "q", Label: LabelFunction, Name: "Beta", QualifiedName: "shared", FilePath: "b.go"},
	}
	if err := store.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	for _, want := range nodes {
		hits, err := store.Search(want.Project, want.Name, "", 10)
		if err != nil || len(hits) != 1 || hits[0].Node.FilePath != want.FilePath {
			t.Fatalf("project %q: hits=%+v err=%v, want %+v", want.Project, hits, err, want)
		}
	}
	if err := store.ValidateIntegrity(); err != nil {
		t.Fatal(err)
	}
}
