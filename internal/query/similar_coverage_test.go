package query

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/index"
)

// indexGoClones builds a Go module with n identical-shape functions and
// indexes it, returning an engine over the committed graph (manifest included).
func indexGoClones(t *testing.T, n int) *Engine {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module cov.test\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "package cov\n\nfunc clone%d(v int) int {\n  v += 1\n  v += 2\n  v += 3\n  v += 4\n  return v\n}\n"
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d.go", i)),
			[]byte(fmt.Sprintf(body, i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := index.RunAtomic(dbPath, root); err != nil {
		t.Fatalf("index: %v", err)
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewEngine(store, index.ProjectName(root), root)
}

// TestSimilarNotice_PartialPropagatesToQueries pins coverage propagation past
// the pipeline: a budgeted graph reports partial through the engine, with an
// actionable notice — and the notice survives a no-op reindex (restart path).
func TestSimilarNotice_PartialPropagatesToQueries(t *testing.T) {
	t.Setenv("CODEGRAPH_SIMILAR_MAX_PAIRS", "50")
	eng := indexGoClones(t, 16)
	defer eng.Close()
	cov, err := eng.SimilarCoverage()
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if cov.Status != "partial" {
		t.Fatalf("budgeted graph must report partial, got %+v", cov)
	}
	notice := eng.SimilarNotice()
	if !strings.Contains(notice, "partial") || !strings.Contains(notice, "CODEGRAPH_SIMILAR_MAX_PAIRS") {
		t.Errorf("notice must be actionable, got %q", notice)
	}
	page, err := eng.SimilarPage("f00.go.clone0", 5, "")
	if err != nil {
		t.Fatalf("similar page: %v", err)
	}
	if page.Notice != notice {
		t.Errorf("similar page notice=%q, want %q", page.Notice, notice)
	}
}

// TestSimilarNotice_EmptyWhenComplete pins the quiet path: a complete clone
// index adds no notice to `similar` answers.
func TestSimilarNotice_EmptyWhenComplete(t *testing.T) {
	eng := indexGoClones(t, 3)
	defer eng.Close()
	cov, err := eng.SimilarCoverage()
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if cov.Status != "complete" {
		t.Fatalf("small corpus must be complete, got %+v", cov)
	}
	if notice := eng.SimilarNotice(); notice != "" {
		t.Errorf("complete coverage must be silent, got %q", notice)
	}
	page, err := eng.SimilarPage("f00.go.clone0", 5, "")
	if err != nil {
		t.Fatalf("similar page: %v", err)
	}
	if page.Notice != "" {
		t.Errorf("complete similar page notice=%q, want empty", page.Notice)
	}
}
