package index

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/similar"
)

// writeCloneCorpus creates n files with identical-shape functions (names
// differ, bodies match) — the clone-bomb shape from the P0 clones fixture,
// scaled down. No tsconfig, so no scip download: defs/similar only.
func writeCloneCorpus(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	body := `export function clone%d(x: number, y: string) {
  const v0 = x + 0 + y.length;
  const v1 = x + 3 + y.length;
  const v2 = x + 6 + y.length;
  const v3 = x + 9 + y.length;
  const v4 = x + 12 + y.length;
  return v0 + v1 + v2 + v3 + v4;
}
`
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d.ts", i)), []byte(fmt.Sprintf(body, i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestSimilarBudget_PartialCoveragePersisted pins the budgeted pipeline: a
// clone corpus under a tiny pair budget commits a deterministic partial edge
// set with partial coverage in Result AND manifest — and a no-op rerun
// preserves that coverage instead of recomputing or dropping it.
func TestSimilarBudget_PartialCoveragePersisted(t *testing.T) {
	t.Setenv("CODEGRAPH_SIMILAR_MAX_PAIRS", "50")
	t.Setenv("CODEGRAPH_SIMILAR_MAX_EDGES", "100000")
	root := writeCloneCorpus(t, 24) // 276 near-identical pairs vs a 50-pair budget
	dbPath := filepath.Join(t.TempDir(), "graph.db")

	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if res.Similar.Status != similar.StatusPartial {
		t.Fatalf("clone corpus must report partial similarity, got %+v", res.Similar)
	}
	if !res.Similar.PairsCapped || res.Similar.PairsExamined != 50 {
		t.Errorf("pair budget not honored: %+v", res.Similar)
	}
	if res.Status != StatusHealthy {
		t.Errorf("partial similarity must not degrade CALLS trust: status=%q", res.Status)
	}

	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.Similar.Status != similar.StatusPartial || manifest.Similar.PairsExamined != 50 {
		t.Errorf("manifest must persist coverage, got %+v", manifest.Similar)
	}
	if manifest.SimilarVersion == "" {
		t.Error("manifest must record the similarity version")
	}

	// No-op rerun: same files, same budget → reuse WITH coverage preserved.
	res2, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if !res2.Reused {
		t.Fatal("unchanged corpus under the same budget must reuse")
	}
	if res2.Similar.Status != similar.StatusPartial || res2.Similar.PairsExamined != 50 {
		t.Errorf("no-op must preserve coverage, got %+v", res2.Similar)
	}
}

// TestSimilarBudget_ChangeInvalidatesNoOp pins budget-aware freshness: the
// same files under a different budget are NOT fresh — the graph rebuilds
// instead of certifying one budget's output as another's.
func TestSimilarBudget_ChangeInvalidatesNoOp(t *testing.T) {
	root := writeCloneCorpus(t, 8)
	dbPath := filepath.Join(t.TempDir(), "graph.db")

	t.Setenv("CODEGRAPH_SIMILAR_MAX_PAIRS", "50")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("index: %v", err)
	}
	t.Setenv("CODEGRAPH_SIMILAR_MAX_PAIRS", "60")
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if res.Reused {
		t.Error("a budget change must invalidate no-op reuse")
	}
	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SimilarVersion != "minhash-lsh-v1+pairs=60+edges=50000" {
		t.Errorf("manifest version = %q, want the new budget recorded", manifest.SimilarVersion)
	}
}

// TestSimilarBudget_CompleteByDefault pins the acceptance's second half: a
// normal corpus under the shipped budget is complete, and stays byte-identical
// to the pre-budget algorithm (legacy wrapper agrees edge-for-edge).
func TestSimilarBudget_CompleteByDefault(t *testing.T) {
	root := writeCloneCorpus(t, 4) // 6 pairs: far under any budget
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if res.Similar.Status != similar.StatusComplete {
		t.Errorf("small corpus must be complete, got %+v", res.Similar)
	}
	if res.Similar.Notice() != "" {
		t.Errorf("complete coverage must have no notice, got %q", res.Similar.Notice())
	}
}

// TestSimilarBudget_OmittedWhenSkipped pins that skipping the pass is explicit:
// low-memory hosts (or CODEGRAPH_SKIP_SIMILAR) record omitted coverage instead
// of an empty-but-"complete" clone index.
func TestSimilarBudget_OmittedWhenSkipped(t *testing.T) {
	t.Setenv("CODEGRAPH_SKIP_SIMILAR", "1")
	root := writeCloneCorpus(t, 4)
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if res.Similar.Status != similar.StatusOmitted {
		t.Errorf("skipped pass must report omitted, got %+v", res.Similar)
	}
	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Similar.Status != similar.StatusOmitted {
		t.Errorf("manifest must persist omitted, got %+v", manifest.Similar)
	}
}
