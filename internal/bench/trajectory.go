package bench

import (
	"time"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/index"
	"github.com/Lordymine/codegraph/internal/query"
)

// Trajectory measures the edit-refresh loop with prepare cost separated from
// query cost: cold index → query → edit (staleness observed) → reindex →
// re-query. It runs on a caller-supplied repo the caller may mutate (tests use
// a temp fixture) — never point it at a repo you won't modify.
type Trajectory struct {
	Question Question
	// Prepare costs (wall clock).
	IndexSeconds   float64 // cold RunAtomic
	ReindexSeconds float64 // RunAtomic after the edit
	// Staleness observed between edit and reindex.
	StaleFiles int
	// Query costs (every real page, product wire format).
	Q1        Cost // before the edit
	Q1Results int
	Q1Seconds float64 // wall time of the Q1 page walk
	Q2        Cost    // after the reindex
	Q2Results int
	// Provenance of the reindexed graph.
	IndexStatus  string
	ResolverNote string
}

// AmortizedSeconds is the wall time per query over a session with k queries
// sharing one cold index + one edit/refresh round: prepare dominates short
// sessions, per-query cost dominates long ones.
func (t Trajectory) AmortizedSeconds(k int) float64 {
	if k <= 0 {
		k = 1
	}
	return (t.IndexSeconds + t.ReindexSeconds + float64(k)*t.Q1Seconds) / float64(k)
}

// MeasureTrajectory runs the full loop. edit mutates root (e.g. append a
// caller); q is asked before and after. Stores are closed before each
// RunAtomic so replacement never contends with an open handle (Windows).
func MeasureTrajectory(dbPath, root string, q Question, edit func(root string) error) (Trajectory, error) {
	var t Trajectory
	t.Question = q

	t0 := time.Now()
	if _, err := index.RunAtomic(dbPath, root); err != nil {
		return t, err
	}
	t.IndexSeconds = time.Since(t0).Seconds()

	q1, n1, q1sec, staleStore, err := queryWithStore(dbPath, root, q)
	if err != nil {
		return t, err
	}
	t.Q1, t.Q1Results, t.Q1Seconds = q1, n1, q1sec

	if err := edit(root); err != nil {
		_ = staleStore.Close()
		return t, err
	}
	ch, err := index.DetectChanges(staleStore, index.ProjectName(root), root)
	if err != nil {
		_ = staleStore.Close()
		return t, err
	}
	t.StaleFiles = len(ch.Changed) + len(ch.Added) + len(ch.Deleted)
	if err := staleStore.Close(); err != nil {
		return t, err
	}

	t0 = time.Now()
	res, err := index.RunAtomic(dbPath, root)
	if err != nil {
		return t, err
	}
	t.ReindexSeconds = time.Since(t0).Seconds()
	t.IndexStatus = string(res.Status)
	if res.Status == index.StatusDegraded {
		t.ResolverNote = res.Resolver.Summary()
	}

	q2, n2, _, freshStore, err := queryWithStore(dbPath, root, q)
	if err != nil {
		return t, err
	}
	t.Q2, t.Q2Results = q2, n2
	if err := freshStore.Close(); err != nil {
		return t, err
	}
	return t, nil
}

// queryWithStore walks every real page of q and returns the cost, the result
// count, the walk wall time, and the still-open store (caller closes it).
func queryWithStore(dbPath, root string, q Question) (Cost, int, float64, *graph.Store, error) {
	st, err := graph.Open(dbPath)
	if err != nil {
		return Cost{}, 0, 0, nil, err
	}
	eng := query.NewEngine(st, index.ProjectName(root), root)
	t0 := time.Now()
	g, set, err := graphCostPages(eng, q, ProductPageRefs)
	sec := time.Since(t0).Seconds()
	if err != nil {
		_ = st.Close()
		return Cost{}, 0, 0, nil, err
	}
	return g, len(set), sec, st, nil
}
