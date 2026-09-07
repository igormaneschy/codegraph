package bench

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/index"
)

// TestTrajectoryHoldsAcrossPages runs the edit-refresh loop on a 600-caller
// hub, where the question itself spans pages: the re-query must see the new
// caller, staleness must be observed, and prepare cost must amortize away.
func TestTrajectoryHoldsAcrossPages(t *testing.T) {
	root := writeHubFixture(t, 600)
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	q := Question{Kind: "callers", QN: index.ProjectName(root) + ":hub.go.Hub", Name: "Hub"}
	traj, err := MeasureTrajectory(dbPath, root, q, func(root string) error {
		return os.WriteFile(filepath.Join(root, "zz_new.go"),
			[]byte("package p3\n\nfunc BrandNew(v int) int { return Hub(v) }\n"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("index=%.2fs reindex=%.2fs stale=%d q1tok=%d/%dcalls q2tok=%d results=%d->%d q1sec=%.3f amort1=%.2f amort10=%.2f amort100=%.2f status=%s",
		traj.IndexSeconds, traj.ReindexSeconds, traj.StaleFiles, traj.Q1.Tokens, traj.Q1.Calls,
		traj.Q2.Tokens, traj.Q1Results, traj.Q2Results, traj.Q1Seconds,
		traj.AmortizedSeconds(1), traj.AmortizedSeconds(10), traj.AmortizedSeconds(100), traj.IndexStatus)
	if traj.StaleFiles < 1 {
		t.Errorf("edit must show as stale before reindex, got %d", traj.StaleFiles)
	}
	if traj.Q1Results != 601 || traj.Q2Results != 602 {
		t.Errorf("results %d->%d, want 601->602", traj.Q1Results, traj.Q2Results)
	}
	if traj.Q1.Calls < 2 || traj.Q2.Calls < 2 {
		t.Errorf("600+ callers must span pages: Q1=%d calls Q2=%d calls", traj.Q1.Calls, traj.Q2.Calls)
	}
	if a1, a100 := traj.AmortizedSeconds(1), traj.AmortizedSeconds(100); a1 <= a100 {
		t.Errorf("amortized time must fall with session length: k=1 → %.3fs, k=100 → %.3fs", a1, a100)
	}
}
