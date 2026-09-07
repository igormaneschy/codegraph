package query

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

// TestEngine_Callers_DefaultLimitDoesNotTruncateHubs pins that the default limit
// (limit=0) does not silently cap an exhaustive relationship answer at 50. callers
// is meant to be complete — truncating a hub like gh-cli's iostreams.Test (448
// callers) at 50 turns a recall ceiling into a wrong answer. A 60-caller symbol
// must come back whole by default.
func TestEngine_Callers_DefaultLimitDoesNotTruncateHubs(t *testing.T) {
	store, err := graph.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const project = "proj"
	const n = 60
	nodes := []graph.Node{{
		Project: project, Label: graph.LabelFunction, Name: "hub",
		QualifiedName: project + ":a.go.hub", FilePath: "a.go", StartLine: 1, EndLine: 2,
	}}
	var edges []graph.Edge
	for i := range n {
		name := "c" + strconv.Itoa(i)
		nodes = append(nodes, graph.Node{
			Project: project, Label: graph.LabelFunction, Name: name,
			QualifiedName: project + ":a.go." + name, FilePath: "a.go", StartLine: 10 + i, EndLine: 11 + i,
		})
		edges = append(edges, graph.Edge{
			Project: project, SourceQN: project + ":a.go." + name, TargetQN: project + ":a.go.hub", Type: graph.EdgeCalls,
		})
	}
	if err := store.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertEdges(edges); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(store, project, t.TempDir())
	page, err := eng.CallersPage("a.go.hub", 0, "") // 0 = use the default limit
	if err != nil {
		t.Fatalf("CallersPage: %v", err)
	}
	if len(page.Refs) != n {
		t.Errorf("default limit truncated an exhaustive answer: got %d callers, want %d", len(page.Refs), n)
	}
	if page.HasMore {
		t.Errorf("60 callers must fit one default page; trailer says has_more with cursor %q", page.Cursor)
	}
}

// TestEngine_Callers_PagesEnumerateHubFully pins P2's core acceptance: a hub
// larger than one page enumerates whole via cursors — no duplicates, no
// omissions — and the byte-budget cut keeps every qualified_name intact.
func TestEngine_Callers_PagesEnumerateHubFully(t *testing.T) {
	store, err := graph.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const project = "proj"
	const n = 600
	nodes := []graph.Node{{
		Project: project, Label: graph.LabelFunction, Name: "hub",
		QualifiedName: project + ":a.go.hub", FilePath: "a.go", StartLine: 1, EndLine: 2,
	}}
	var edges []graph.Edge
	for i := range n {
		name := "c" + strconv.Itoa(i)
		nodes = append(nodes, graph.Node{
			Project: project, Label: graph.LabelFunction, Name: name,
			QualifiedName: project + ":a.go." + name, FilePath: "a.go", StartLine: 10 + i, EndLine: 11 + i,
		})
		edges = append(edges, graph.Edge{
			Project: project, SourceQN: project + ":a.go." + name, TargetQN: project + ":a.go.hub", Type: graph.EdgeCalls,
		})
	}
	if err := store.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertEdges(edges); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(store, project, t.TempDir())
	seen := map[string]bool{}
	pages := 0
	cursor := ""
	for {
		page, err := eng.CallersPage("a.go.hub", 0, cursor)
		if err != nil {
			t.Fatalf("CallersPage cursor %q: %v", cursor, err)
		}
		pages++
		for _, r := range page.Refs {
			if !strings.HasPrefix(r.QualifiedName, project+":a.go.c") {
				t.Fatalf("cut qualified_name on page %d: %q", pages, r.QualifiedName)
			}
			if seen[r.QualifiedName] {
				t.Fatalf("duplicate %q across pages", r.QualifiedName)
			}
			seen[r.QualifiedName] = true
		}
		if !page.HasMore {
			if page.Cursor != "-" {
				t.Errorf("final page cursor = %q, want \"-\"", page.Cursor)
			}
			break
		}
		if page.Cursor == "" || page.Cursor == "-" {
			t.Fatalf("has_more without a continuation cursor on page %d", pages)
		}
		cursor = page.Cursor
		if pages > 10 {
			t.Fatalf("600 callers took more than 10 pages; budget logic regressed")
		}
	}
	if len(seen) != n {
		t.Errorf("hub enumeration incomplete: got %d unique callers, want %d in %d pages", len(seen), n, pages)
	}
	if pages < 2 {
		t.Errorf("600 callers fit in %d page(s); expected the byte budget to force paging", pages)
	}
}
