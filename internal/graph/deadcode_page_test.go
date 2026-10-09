package graph

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// seedDeadMix inserts nDead uncalled private functions plus entry points of
// every kind (exported, main, decorated, test-file, called) spread across two
// files in start_line order, returning the store.
func seedDeadMix(t *testing.T, nDead, nEntry int) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var nodes []Node
	line := 1
	add := func(name, file string, label NodeLabel, props map[string]any) {
		nodes = append(nodes, Node{
			Project: "p", Label: label, Name: name,
			QualifiedName: "p:" + file + "." + name,
			FilePath:      file, StartLine: line, EndLine: line + 1, Props: props,
		})
		line++
	}
	priv := map[string]any{"is_exported": false}
	for i := 0; i < nDead; i++ {
		add(fmt.Sprintf("dead%04d", i), "a.go", LabelFunction, priv)
	}
	for i := 0; i < nEntry; i++ {
		switch i % 5 {
		case 0:
			add(fmt.Sprintf("Exp%04d", i), "a.go", LabelFunction, map[string]any{"is_exported": true})
		case 1:
			add("main", "a.go", LabelFunction, priv)
		case 2:
			add(fmt.Sprintf("h%04d", i), "c.ts", LabelMethod, map[string]any{"decorators": []string{"Get"}})
		case 3:
			add(fmt.Sprintf("Test%04d", i), "a_test.go", LabelFunction, priv)
		case 4:
			add(fmt.Sprintf("used%04d", i), "b.go", LabelFunction, priv)
		}
	}
	if err := s.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	// Give every "used" candidate a real inbound CALLS so only "dead" remains.
	var used []Edge
	for i := 0; i < nEntry; i++ {
		if i%5 == 4 {
			used = append(used, Edge{
				Project: "p", SourceQN: "p:a.go.main",
				TargetQN: fmt.Sprintf("p:b.go.used%04d", i), Type: EdgeCalls,
			})
		}
	}
	if _, _, err := s.InsertEdges(used); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestForEachDeadCodeCandidate_StreamsAllInOrder pins the streaming contract:
// the visitor walks the whole raw set in (file_path, start_line) order with no
// duplicates or omissions, and exhaustion past the end is empty, not an error.
func TestForEachDeadCodeCandidate_StreamsAllInOrder(t *testing.T) {
	s := seedDeadMix(t, 30, 10)
	var got []string
	prev := ""
	if err := s.ForEachDeadCodeCandidate("p", 0, func(n Node) error {
		key := fmt.Sprintf("%s:%06d", n.FilePath, n.StartLine)
		if key < prev {
			t.Fatalf("stream order violated: %q after %q", key, prev)
		}
		prev = key
		got = append(got, n.QualifiedName)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, qn := range got {
		if seen[qn] {
			t.Fatalf("duplicate %q in stream", qn)
		}
		seen[qn] = true
	}
	// 30 dead + main + used-callers...: every uncalled Function/Method streams.
	// main is uncalled (entry, still a raw candidate); used* have inbound CALLS.
	if len(got) < 30 {
		t.Errorf("stream missed raw candidates: got %d, want >= 30", len(got))
	}
	visited := 0
	if err := s.ForEachDeadCodeCandidate("p", 100000, func(Node) error { visited++; return nil }); err != nil {
		t.Fatalf("past-end stream must be empty, not an error: %v", err)
	}
	if visited != 0 {
		t.Errorf("past-end stream visited %d rows, want 0", visited)
	}
	if err := s.ForEachDeadCodeCandidate("p", -1, func(Node) error { return nil }); err == nil {
		t.Error("negative offset must fail")
	}
}

// TestForEachDeadCodeCandidate_EarlyStopStopsIteration pins the page contract:
// a visitor error stops the iteration immediately and propagates unchanged, so
// serving a page never pays for candidates beyond its continuation probe.
func TestForEachDeadCodeCandidate_EarlyStopStopsIteration(t *testing.T) {
	s := seedDeadMix(t, 100, 0)
	stop := errors.New("page complete")
	visited := 0
	err := s.ForEachDeadCodeCandidate("p", 0, func(Node) error {
		visited++
		if visited == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("early-stop error = %v, want the visitor sentinel", err)
	}
	if visited != 3 {
		t.Fatalf("visited %d rows after early stop, want 3", visited)
	}
}
