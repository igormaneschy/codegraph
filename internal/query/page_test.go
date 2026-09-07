package query

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

// TestPage_NegativeLimitIsActionable pins validation-before-allocation:
// negatives, incoherent ranges and malformed cursors fail with guidance,
// never with a panic or a silently empty answer.
func TestPage_NegativeLimitIsActionable(t *testing.T) {
	store, err := graph.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := NewEngine(store, "proj", t.TempDir())

	for name, fn := range map[string]func() error{
		"callers":   func() error { _, err := eng.CallersPage("x", -1, ""); return err },
		"search":    func() error { _, err := eng.SearchPage("x", "", -5, ""); return err },
		"dead_code": func() error { _, err := eng.DeadCodePage(-2, ""); return err },
		"snippet":   func() error { _, err := eng.SnippetPage("a.go", 1, 0, -3, ""); return err },
	} {
		if err := fn(); err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Errorf("%s negative limit must be an actionable error, got %v", name, err)
		}
	}
	if _, err := eng.CallersPage("x", 0, "bogus-cursor"); err == nil ||
		!strings.Contains(err.Error(), "cursor") {
		t.Errorf("malformed cursor must be actionable, got %v", err)
	}
	if _, err := eng.SnippetPage("a.go", 5, 2, 0, ""); err == nil ||
		!strings.Contains(err.Error(), "bad range") {
		t.Errorf("incoherent range must be actionable, got %v", err)
	}
}

// TestPage_GiantLimitClampsBeforeAlloc pins that a huge limit cannot size
// buffers proportionally to itself: it clamps to MaxPageRefs and the answer
// stays verifiable via has_more + cursor.
func TestPage_GiantLimitClampsBeforeAlloc(t *testing.T) {
	store, err := graph.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const project = "proj"
	if err := store.InsertNodes([]graph.Node{{
		Project: project, Label: graph.LabelFunction, Name: "hub",
		QualifiedName: project + ":a.go.hub", FilePath: "a.go", StartLine: 1, EndLine: 2,
	}}); err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(store, project, t.TempDir())
	page, err := eng.CallersPage("a.go.hub", 1<<30, "")
	if err != nil {
		t.Fatalf("giant limit must clamp, not fail: %v", err)
	}
	if page.HasMore {
		t.Errorf("empty hub must not claim more; cursor %q", page.Cursor)
	}
}

// TestPage_CursorBindsToQuery pins that a cursor cannot wander: reuse across a
// different question fails with orientation to restart, never mixed rows.
func TestPage_CursorBindsToQuery(t *testing.T) {
	store, err := graph.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const project = "proj"
	mk := func(name string) graph.Node {
		return graph.Node{
			Project: project, Label: graph.LabelFunction, Name: name,
			QualifiedName: project + ":a.go." + name, FilePath: "a.go", StartLine: 1, EndLine: 2,
		}
	}
	nodes := []graph.Node{mk("hub")}
	var edges []graph.Edge
	for i := 0; i < 12; i++ {
		name := "caller" + string(rune('a'+i))
		nodes = append(nodes, mk(name))
		edges = append(edges, graph.Edge{
			Project: project, SourceQN: project + ":a.go." + name,
			TargetQN: project + ":a.go.hub", Type: graph.EdgeCalls,
		})
	}
	if err := store.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertEdges(edges); err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(store, project, t.TempDir())
	first, err := eng.CallersPage("a.go.hub", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if !first.HasMore || first.Cursor == "-" {
		t.Fatalf("12 callers at page size 5 must continue: %+v", first)
	}
	if _, err := eng.CalleesPage("a.go.hub", 5, first.Cursor); err == nil ||
		!strings.Contains(err.Error(), "different query") {
		t.Errorf("cross-tool cursor reuse must fail, got %v", err)
	}
	if _, err := eng.CallersPage("a.go.hub", 7, first.Cursor); err == nil ||
		!strings.Contains(err.Error(), "different query") {
		t.Errorf("cross-page-size cursor reuse must fail, got %v", err)
	}
	second, err := eng.CallersPage("a.go.hub", 5, first.Cursor)
	if err != nil {
		t.Fatalf("same-query continuation: %v", err)
	}
	if len(first.Refs)+len(second.Refs) != 10 {
		t.Errorf("pages cover %d + %d rows, want 5 + 5", len(first.Refs), len(second.Refs))
	}
	seen := map[string]bool{}
	for _, r := range append(first.Refs, second.Refs...) {
		if seen[r.QualifiedName] {
			t.Fatalf("duplicate %q across pages", r.QualifiedName)
		}
		seen[r.QualifiedName] = true
	}
}

// TestPage_CursorBindsToGeneration pins snapshot isolation across pages: a
// cursor issued before a reindex is rejected after it, never mixing rows from
// two graphs.
func TestPage_CursorBindsToGeneration(t *testing.T) {
	store, err := graph.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const project = "proj"
	nodes := []graph.Node{{
		Project: project, Label: graph.LabelFunction, Name: "hub",
		QualifiedName: project + ":a.go.hub", FilePath: "a.go", StartLine: 1, EndLine: 2,
	}}
	for i := 0; i < 8; i++ {
		name := "caller" + string(rune('a'+i))
		nodes = append(nodes, graph.Node{
			Project: project, Label: graph.LabelFunction, Name: name,
			QualifiedName: project + ":a.go." + name, FilePath: "a.go", StartLine: 1, EndLine: 2,
		})
	}
	if err := store.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	var edges []graph.Edge
	for i := 0; i < 8; i++ {
		name := "caller" + string(rune('a'+i))
		edges = append(edges, graph.Edge{
			Project: project, SourceQN: project + ":a.go." + name,
			TargetQN: project + ":a.go.hub", Type: graph.EdgeCalls,
		})
	}
	if _, _, err := store.InsertEdges(edges); err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(store, project, t.TempDir())
	first, err := eng.CallersPage("a.go.hub", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if !first.HasMore {
		t.Fatal("8 callers at page size 5 must continue")
	}
	// Forge a cursor identical except for the generation: same shape the
	// session produces after a refresh commits a new manifest.
	stale := pageCursor{V: 1, Gen: "stale-gen", Fp: refFingerprint("callers", project+":a.go.hub", "in", "CALLS", 5), Off: 5}
	if _, err := eng.CallersPage("a.go.hub", 5, encodeCursor(stale)); err == nil ||
		!strings.Contains(err.Error(), "generation") {
		t.Errorf("stale-generation cursor must be rejected, got %v", err)
	}
}
