package query

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

// seedDeadStream builds an engine over nDead uncalled private functions plus
// nEntry entry points (exported/main/decorated/test/called) in file order.
func seedDeadStream(t *testing.T, nDead, nEntry int) *Engine {
	t.Helper()
	store, err := graph.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const project = "proj"
	var nodes []graph.Node
	line := 1
	add := func(name, file string, label graph.NodeLabel, props map[string]any) {
		nodes = append(nodes, graph.Node{
			Project: project, Label: label, Name: name,
			QualifiedName: project + ":" + file + "." + name,
			FilePath:      file, StartLine: line, EndLine: line + 1, Props: props,
		})
		line++
	}
	priv := map[string]any{"is_exported": false}
	for i := 0; i < nDead; i++ {
		add(fmt.Sprintf("dead%04d", i), "a.go", graph.LabelFunction, priv)
	}
	for i := 0; i < nEntry; i++ {
		switch i % 5 {
		case 0:
			add(fmt.Sprintf("Exp%04d", i), "a.go", graph.LabelFunction, map[string]any{"is_exported": true})
		case 1:
			add("main", "a.go", graph.LabelFunction, priv)
		case 2:
			add(fmt.Sprintf("h%04d", i), "c.ts", graph.LabelMethod, map[string]any{"decorators": []string{"Get"}})
		case 3:
			add(fmt.Sprintf("Test%04d", i), "a_test.go", graph.LabelFunction, priv)
		case 4:
			add(fmt.Sprintf("used%04d", i), "b.go", graph.LabelFunction, priv)
		}
	}
	if err := store.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	var used []graph.Edge
	for i := 0; i < nEntry; i++ {
		if i%5 == 4 {
			used = append(used, graph.Edge{
				Project: project, SourceQN: project + ":a.go.main",
				TargetQN: fmt.Sprintf("%s:b.go.used%04d", project, i), Type: graph.EdgeCalls,
			})
		}
	}
	if _, _, err := store.InsertEdges(used); err != nil {
		t.Fatal(err)
	}
	return NewEngine(store, project, t.TempDir())
}

// walkDeadPages enumerates every dead-code page to exhaustion.
func walkDeadPages(t *testing.T, eng *Engine, limit int) []Ref {
	t.Helper()
	var all []Ref
	cursor := ""
	for pages := 0; ; pages++ {
		page, err := eng.DeadCodePage(limit, cursor)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Refs...)
		if !page.HasMore {
			if page.Cursor != "-" {
				t.Fatalf("final page cursor = %q, want \"-\"", page.Cursor)
			}
			return all
		}
		cursor = page.Cursor
		if pages > 100 {
			t.Fatal("dead-code walk did not terminate")
		}
	}
}

// TestDeadCodePage_StreamsFullAnswer pins equivalence with the old
// materialize-then-filter behavior: the streamed walk returns exactly the dead
// set (no entry points), in stable order, with no duplicates.
func TestDeadCodePage_StreamsFullAnswer(t *testing.T) {
	const nDead = 300
	eng := seedDeadStream(t, nDead, 200)
	all := walkDeadPages(t, eng, 50)
	if len(all) != nDead {
		t.Fatalf("streamed walk found %d dead, want %d", len(all), nDead)
	}
	seen := map[string]bool{}
	for _, r := range all {
		if seen[r.QualifiedName] {
			t.Fatalf("duplicate %q in streamed walk", r.QualifiedName)
		}
		seen[r.QualifiedName] = true
		if r.Name == "main" || len(r.Name) > 3 && r.Name[:3] == "Exp" {
			t.Errorf("entry point leaked into streamed answer: %q", r.Name)
		}
	}
	for i := 0; i < nDead; i++ {
		if !seen[fmt.Sprintf("proj:a.go.dead%04d", i)] {
			t.Fatalf("dead%04d missing from streamed walk", i)
		}
	}
}

// TestDeadCodePage_EntryPointsDontStarve pins the plan's explicit hazard: with
// entry points first in file order and the dead few last, small pages still
// find every candidate — many entry points never fake an end of results.
func TestDeadCodePage_EntryPointsDontStarve(t *testing.T) {
	eng := seedDeadStream(t, 3, 500)
	first, err := eng.DeadCodePage(2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Refs) != 2 || !first.HasMore {
		t.Fatalf("page 1 = %d refs has_more=%v, want 2/true", len(first.Refs), first.HasMore)
	}
	second, err := eng.DeadCodePage(2, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Refs) != 1 || second.HasMore {
		t.Fatalf("page 2 = %d refs has_more=%v, want 1/false", len(second.Refs), second.HasMore)
	}
}

func TestDeadCodePage_OldCursorAndTiedPositions(t *testing.T) {
	store, err := graph.Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodes := []graph.Node{}
	for _, name := range []string{"c", "a", "b"} {
		nodes = append(nodes, graph.Node{Project: "p", Label: graph.LabelFunction, Name: name,
			QualifiedName: "p:a.go." + name, FilePath: "a.go", StartLine: 1})
	}
	if err := store.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(store, "p", t.TempDir())
	first, err := eng.DeadCodePage(1, "")
	if err != nil || len(first.Refs) != 1 || first.Refs[0].Name != "a" {
		t.Fatalf("first tied-position page=%+v, err=%v", first, err)
	}
	newNext, err := eng.DeadCodePage(1, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRefCursor(first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	decoded.RawOff = 0 // pre-optimization cursor had only the filtered offset
	oldNext, err := eng.DeadCodePage(1, encodeCursor(decoded))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(newNext.Refs, oldNext.Refs) || len(newNext.Refs) != 1 || newNext.Refs[0].Name != "b" {
		t.Fatalf("new cursor page=%+v, old cursor page=%+v", newNext, oldNext)
	}
	last, err := eng.DeadCodePage(1, newNext.Cursor)
	if err != nil || len(last.Refs) != 1 || last.Refs[0].Name != "c" || last.HasMore {
		t.Fatalf("last tied-position page=%+v, err=%v", last, err)
	}
}

// TestDeadCodePage_BoundedAllocs pins the memory contract: serving one page
// from thousands of candidates allocates proportionally to the visited rows
// (the page plus its continuation probe), never to the total. Materializing
// the full set would pay one Node plus props-map plus JSON parse per
// candidate, which the streaming page never does; the bound stays as a
// regression tripwire.
func TestDeadCodePage_BoundedAllocs(t *testing.T) {
	eng := seedDeadStream(t, 4000, 0)
	if _, err := eng.DeadCodePage(50, ""); err != nil { // warm caches, stabilize GC
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(20, func() {
		if _, err := eng.DeadCodePage(50, ""); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("DeadCodePage(50) over 4000 candidates: %.0f allocs/run", allocs)
	if allocs > 20000 {
		t.Errorf("page serve took %.0f allocs, want <= 20000 (batch-proportional)", allocs)
	}
}

// BenchmarkDeadCodePage_FirstPage tracks serve cost over a large candidate set.
func BenchmarkDeadCodePage_FirstPage(b *testing.B) {
	benchmarkDeadCodePage(b, false)
}

func BenchmarkDeadCodePage_DeepPage(b *testing.B) {
	benchmarkDeadCodePage(b, true)
}

func benchmarkDeadCodePage(b *testing.B, deep bool) {
	store, err := graph.Open(filepath.Join(b.TempDir(), "g.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	const project = "proj"
	nodes := make([]graph.Node, 0, 4000)
	for i := 0; i < 4000; i++ {
		nodes = append(nodes, graph.Node{
			Project: project, Label: graph.LabelFunction, Name: fmt.Sprintf("dead%04d", i),
			QualifiedName: fmt.Sprintf("%s:a.go.dead%04d", project, i),
			FilePath:      "a.go", StartLine: 10 + i, EndLine: 11 + i,
			Props: map[string]any{"is_exported": false},
		})
	}
	if err := store.InsertNodes(nodes); err != nil {
		b.Fatal(err)
	}
	eng := NewEngine(store, project, b.TempDir())
	defer eng.Close()
	cursor := ""
	if deep {
		for pageIndex := 0; pageIndex < 60; pageIndex++ {
			page, err := eng.DeadCodePage(50, cursor)
			if err != nil || !page.HasMore {
				b.Fatalf("prepare deep page %d: page=%+v err=%v", pageIndex, page, err)
			}
			cursor = page.Cursor
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := eng.DeadCodePage(50, cursor); err != nil {
			b.Fatal(err)
		}
	}
}
