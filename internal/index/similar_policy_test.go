package index

import (
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/memory"
	"github.com/Lordymine/codegraph/internal/similar"
)

func TestSimilarPolicy_SkipTransitionsRebuildAndRestoreEdges(t *testing.T) {
	t.Setenv("CODEGRAPH_SKIP_SIMILAR", "0")
	if memory.SkipSimilar() {
		t.Skip("R12 enabled-pass transition requires a host profile that does not auto-skip similarity")
	}
	root := writeCloneCorpus(t, 4)
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	previousVersion := ""
	for _, skip := range []string{"1", "0", "1"} {
		t.Setenv("CODEGRAPH_SKIP_SIMILAR", skip)
		result, err := RunAtomic(dbPath, root)
		if err != nil || result.Reused || result.Status != StatusHealthy {
			t.Fatalf("skip=%s result=%+v err=%v, want healthy rebuild", skip, result, err)
		}
		wantCoverage := similar.StatusComplete
		if skip == "1" {
			wantCoverage = similar.StatusOmitted
		}
		manifest, err := ReadManifest(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if manifest.Similar.Status != wantCoverage || result.Similar.Status != wantCoverage || manifest.SimilarVersion == previousVersion {
			t.Fatalf("skip=%s manifest=%+v result=%+v, want new policy and %s", skip, manifest, result, wantCoverage)
		}
		previousVersion = manifest.SimilarVersion
		assertStoredSimilarPolicy(t, dbPath, ProjectName(root), skip == "0")
		noOp, err := RunAtomic(dbPath, root)
		if err != nil || !noOp.Reused || noOp.Similar.Status != wantCoverage {
			t.Fatalf("same policy no-op=%+v err=%v", noOp, err)
		}
	}
}

func assertStoredSimilarPolicy(t *testing.T, dbPath, project string, enabled bool) {
	t.Helper()
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodes, err := store.Neighbors(project, project+":f00.ts.clone0", "both", string(graph.EdgeSimilarTo), 10)
	if err != nil || enabled && len(nodes) != 3 || !enabled && len(nodes) != 0 {
		t.Fatalf("enabled=%v clone neighbors=%+v err=%v", enabled, nodes, err)
	}
	if err := store.ValidateIntegrity(); err != nil {
		t.Fatal(err)
	}
}
