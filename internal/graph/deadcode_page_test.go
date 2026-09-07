package graph

import (
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

// TestDeadCodeCandidates_BatchesCoverAll pins the streaming contract: small
// batches walk the whole raw set in (file_path, start_line) order with no
// duplicates or omissions, and exhaustion returns empty (not an error).
func TestDeadCodeCandidates_BatchesCoverAll(t *testing.T) {
	s := seedDeadMix(t, 30, 10)
	var got []string
	prev := ""
	for off := 0; ; off += 7 {
		batch, err := s.DeadCodeCandidates("p", off, 7)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			break
		}
		for _, n := range batch {
			key := fmt.Sprintf("%s:%06d", n.FilePath, n.StartLine)
			if key < prev {
				t.Fatalf("batch order violated: %q after %q", key, prev)
			}
			prev = key
			got = append(got, n.QualifiedName)
		}
		if len(got) > 100 {
			t.Fatal("walk did not terminate")
		}
	}
	seen := map[string]bool{}
	for _, qn := range got {
		if seen[qn] {
			t.Fatalf("duplicate %q across batches", qn)
		}
		seen[qn] = true
	}
	// 30 dead + main + used-callers...: every uncalled Function/Method streams.
	// main is uncalled (entry, still a raw candidate); used* have inbound CALLS.
	if len(got) < 30 {
		t.Errorf("walk missed raw candidates: got %d, want >= 30", len(got))
	}
	if _, err := s.DeadCodeCandidates("p", 100000, 7); err != nil {
		t.Fatalf("past-end batch must be empty, not an error: %v", err)
	} else if n, _ := s.DeadCodeCandidates("p", 100000, 7); len(n) != 0 {
		t.Errorf("past-end batch = %d rows, want 0", len(n))
	}
	if _, err := s.DeadCodeCandidates("p", -1, 7); err == nil {
		t.Error("negative offset must fail")
	}
	if _, err := s.DeadCodeCandidates("p", 0, 0); err == nil {
		t.Error("non-positive batch must fail")
	}
}
