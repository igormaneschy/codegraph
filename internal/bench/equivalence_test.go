package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/index"
	"github.com/Lordymine/codegraph/internal/query"
)

// writeHubFixture builds a deterministic Go-only module (no tsconfig, so no
// scip download): Hub plus nCallers plain-func callers (each calling Hub
// twice — set semantics must collapse them), one method caller, one comment
// decoy and one string decoy mentioning Hub without calling it.
func writeHubFixture(t *testing.T, nCallers int) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module p3.test\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hub.go"), []byte("package p3\n\nfunc Hub(x int) int { return x }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	per, rem := nCallers/10, nCallers%10
	n := 0
	for f := 0; f < 10; f++ {
		count := per
		if f == 0 {
			count += rem
		}
		if count == 0 {
			continue
		}
		body := "package p3\n\n// Hub central: comment decoy, not a call.\n"
		for k := 0; k < count; k++ {
			body += fmt.Sprintf("func Caller%04d(v int) int { return Hub(v+%d) + Hub(v-%d) }\n", n, n, n)
			n++
		}
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("c%02d.go", f)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	extra := "package p3\n\ntype Svc struct{}\n\nfunc (s Svc) CallHub(v int) int { return Hub(v) }\n\nfunc NotACaller(s string) string { return \"Hub\" }\n"
	if err := os.WriteFile(filepath.Join(root, "extra.go"), []byte(extra), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func indexFixture(t *testing.T, root string) (string, *query.Engine) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	res, err := index.RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("index fixture: %v", err)
	}
	if res.Status == index.StatusDegraded {
		t.Fatalf("fixture indexed degraded: %s", res.Resolver.Summary())
	}
	st, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return dbPath, query.NewEngine(st, index.ProjectName(root), root)
}

// TestGraphCompletenessAgainstIndependentOracle pins P3's core claim: on a hub
// with >500 callers the graph returns exactly the go/ast oracle's set — full
// enumeration, no truncation, decoys excluded on both sides.
func TestGraphCompletenessAgainstIndependentOracle(t *testing.T) {
	const n = 600
	root := writeHubFixture(t, n)
	_, eng := indexFixture(t, root)

	oracle, err := GoCallersOracle(root, "Hub")
	if err != nil {
		t.Fatalf("oracle: %v", err)
	}
	if len(oracle) != n+1 { // plain callers + the Svc.CallHub method
		t.Fatalf("oracle found %d callers, want %d", len(oracle), n+1)
	}

	corpus, err := LoadCorpus(root)
	if err != nil {
		t.Fatal(err)
	}
	q := Question{Kind: "callers", QN: index.ProjectName(root) + ":hub.go.Hub", Name: "Hub"}
	out, err := RunOneWithOracle(eng, corpus, q, oracle)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Complete {
		t.Errorf("graph incomplete vs independent oracle: graph=%d oracle=%d", out.GraphResults, out.Oracle)
	}
	if out.GraphResults != n+1 {
		t.Errorf("graph returned %d callers, want %d", out.GraphResults, n+1)
	}
	if out.Graph.Calls < 2 {
		t.Errorf("600+ callers must span pages (calls=%d); single-shot would be a truncation", out.Graph.Calls)
	}
}

// TestPageSizeNeverManufacturesACheaperAnswer pins that shrinking the page
// cannot win artificially: the identical set comes back at 500/50/7, with more
// round-trips and (via extra trailers) no fewer tokens.
func TestPageSizeNeverManufacturesACheaperAnswer(t *testing.T) {
	root := writeHubFixture(t, 120)
	_, eng := indexFixture(t, root)
	q := Question{Kind: "callers", QN: index.ProjectName(root) + ":hub.go.Hub", Name: "Hub"}

	type res struct {
		set  map[string]bool
		cost Cost
	}
	got := map[int]res{}
	for _, size := range []int{500, 50, 7} {
		cost, set, err := graphCostPages(eng, q, size)
		if err != nil {
			t.Fatal(err)
		}
		got[size] = res{set, cost}
	}
	base := got[500].set
	if len(base) != 121 {
		t.Fatalf("page-500 set has %d callers, want 121", len(base))
	}
	for _, size := range []int{50, 7} {
		other := got[size].set
		if len(other) != len(base) {
			t.Fatalf("page-%d set has %d callers, want %d: smaller pages truncate", size, len(other), len(base))
		}
		for qn := range base {
			if !other[qn] {
				t.Fatalf("page-%d misses %q", size, qn)
			}
		}
		if got[size].cost.Calls <= got[500].cost.Calls {
			t.Errorf("page-%d calls=%d, want more than page-500 calls=%d", size, got[size].cost.Calls, got[500].cost.Calls)
		}
		if got[size].cost.Tokens < got[500].cost.Tokens {
			t.Errorf("page-%d tokens=%d below page-500 tokens=%d: trailers must be charged", size, got[size].cost.Tokens, got[500].cost.Tokens)
		}
	}
}

// TestTrajectorySeparatesPrepareFromQuery pins the edit-refresh loop on a
// small fixture: staleness is observed, the re-query sees the new caller, and
// prepare cost amortizes away as session length grows.
func TestTrajectorySeparatesPrepareFromQuery(t *testing.T) {
	root := writeHubFixture(t, 3)
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	q := Question{Kind: "callers", QN: index.ProjectName(root) + ":hub.go.Hub", Name: "Hub"}

	traj, err := MeasureTrajectory(dbPath, root, q, func(root string) error {
		return os.WriteFile(filepath.Join(root, "zz_new.go"),
			[]byte("package p3\n\nfunc BrandNew(v int) int { return Hub(v) }\n"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if traj.StaleFiles < 1 {
		t.Errorf("edit must show as stale before reindex, got %d files", traj.StaleFiles)
	}
	if traj.Q2Results != traj.Q1Results+1 {
		t.Errorf("re-query sees %d callers, want Q1+1=%d", traj.Q2Results, traj.Q1Results+1)
	}
	if traj.IndexStatus != string(index.StatusHealthy) {
		t.Errorf("trajectory index status = %q, want healthy", traj.IndexStatus)
	}
	if a1, a100 := traj.AmortizedSeconds(1), traj.AmortizedSeconds(100); a1 <= a100 {
		t.Errorf("amortized time must fall with session length: k=1 → %.3fs, k=100 → %.3fs", a1, a100)
	}
	if traj.Q1.Calls != traj.Q2.Calls {
		t.Errorf("tiny-hub query calls changed across reindex: Q1=%d Q2=%d", traj.Q1.Calls, traj.Q2.Calls)
	}
}
