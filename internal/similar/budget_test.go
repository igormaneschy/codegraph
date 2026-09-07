package similar

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Lordymine/codegraph/internal/graph"
)

// identicalSigDocs returns n docs sharing one signature: every pair is a
// candidate scoring 1.0 — the clone-bomb worst case (P0 clones fixture:
// 1200 identical bodies → 720k pairs).
func identicalSigDocs(n int) []SigDoc {
	sig := Signature([]string{"a", "b", "c", "d", "e", "f", "g", "h"}, 3, 128)
	docs := make([]SigDoc, n)
	for i := range docs {
		docs[i] = SigDoc{QN: string(rune('a'+i/26)) + string(rune('a'+i%26)), Sig: append([]uint64(nil), sig...)}
	}
	return docs
}

func qnsOf(edges []graph.Edge) [][2]string {
	out := make([][2]string, len(edges))
	for i, e := range edges {
		out[i] = [2]string{e.SourceQN, e.TargetQN}
	}
	return out
}

// TestBudgetedPass_AdversarialTerminatesWithinBudget pins P4's core guarantee:
// a clone bomb stops at the pair budget with partial coverage instead of
// growing an unbounded pair set — and the kept edges are usable, not garbage.
func TestBudgetedPass_AdversarialTerminatesWithinBudget(t *testing.T) {
	docs := identicalSigDocs(300) // 44850 pairs, all scoring 1.0
	lim := Limits{MaxPairs: 1000, MaxEdges: 100}
	edges, cov, err := EdgesFromSignaturesContext(context.Background(), "p", docs, 0.7, lim)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Status != StatusPartial {
		t.Errorf("clone bomb must report partial, got %q", cov.Status)
	}
	if !cov.PairsCapped || cov.PairsExamined != 1000 {
		t.Errorf("pair budget not honored: examined=%d capped=%v", cov.PairsExamined, cov.PairsCapped)
	}
	if len(edges) != 100 || !cov.EdgesCapped {
		t.Errorf("edge cap not honored: edges=%d capped=%v", len(edges), cov.EdgesCapped)
	}
	if cov.Docs != 300 {
		t.Errorf("docs=%d, want 300", cov.Docs)
	}
	if notice := cov.Notice(); notice == "" {
		t.Error("partial coverage must carry an actionable notice")
	}
}

// TestBudgetedPass_DeterministicPrefix pins that budgeted output is stable:
// two runs agree exactly, and doubling the pair budget extends (never
// reshuffles) the kept prefix — despite map-random bucket iteration.
func TestBudgetedPass_DeterministicPrefix(t *testing.T) {
	docs := identicalSigDocs(200)
	run := func(pairs int) [][2]string {
		edges, _, err := EdgesFromSignaturesContext(context.Background(), "p", docs, 0.7,
			Limits{MaxPairs: pairs, MaxEdges: 100000})
		if err != nil {
			t.Fatal(err)
		}
		return qnsOf(edges)
	}
	a, b := run(2000), run(2000)
	if len(a) != len(b) {
		t.Fatalf("same budget gave %d vs %d edges: not deterministic", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("same budget diverges at edge %d: %v vs %v", i, a[i], b[i])
		}
	}
	wide := run(4000)
	if len(wide) < len(a) {
		t.Fatalf("wider budget gave fewer edges (%d < %d)", len(wide), len(a))
	}
	for i := range a {
		if wide[i] != a[i] {
			t.Fatalf("wider budget reshuffled edge %d: %v vs %v", i, wide[i], a[i])
		}
	}
	// Sorted (source, target) order: the truncation prefix property.
	for i := 1; i < len(wide); i++ {
		if wide[i-1][0] > wide[i][0] || (wide[i-1][0] == wide[i][0] && wide[i-1][1] > wide[i][1]) {
			t.Fatalf("edges not sorted at %d: %v then %v", i, wide[i-1], wide[i])
		}
	}
}

// TestBudgetedPass_InputOrderIndependent pins that pipeline map-iteration
// order cannot leak into budgeted output: shuffled input keeps the identical
// edge set under a binding budget.
func TestBudgetedPass_InputOrderIndependent(t *testing.T) {
	docs := identicalSigDocs(200)
	shuffled := append([]SigDoc(nil), docs...)
	for i, j := 0, len(shuffled)-1; i < j; i, j = i+1, j-1 {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}
	lim := Limits{MaxPairs: 2000, MaxEdges: 100000}
	a, _, err := EdgesFromSignaturesContext(context.Background(), "p", docs, 0.7, lim)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := EdgesFromSignaturesContext(context.Background(), "p", shuffled, 0.7, lim)
	if err != nil {
		t.Fatal(err)
	}
	qa, qb := qnsOf(a), qnsOf(b)
	if len(qa) != len(qb) {
		t.Fatalf("input order changed output size: %d vs %d", len(qa), len(qb))
	}
	for i := range qa {
		if qa[i] != qb[i] {
			t.Fatalf("input order changed edge %d: %v vs %v", i, qa[i], qb[i])
		}
	}
}

// untouched by the budget: full edge set, complete coverage, empty notice.
func TestBudgetedPass_CompleteWhenUnderBudget(t *testing.T) {
	docs := identicalSigDocs(20) // 190 pairs, well under defaults
	edges, cov, err := EdgesFromSignaturesContext(context.Background(), "p", docs, 0.7, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if cov.Status != StatusComplete {
		t.Errorf("small corpus must be complete, got %+v", cov)
	}
	if cov.PairsExamined != 190 || len(edges) != 190 {
		t.Errorf("examined=%d edges=%d, want 190/190", cov.PairsExamined, len(edges))
	}
	if cov.Notice() != "" {
		t.Errorf("complete coverage must have no notice, got %q", cov.Notice())
	}
	// Legacy wrapper agrees (same edges, no budget surprise).
	legacy := EdgesFromSignatures("p", docs, 0.7)
	if len(legacy) != len(edges) {
		t.Errorf("legacy wrapper gave %d edges, budgeted gave %d", len(legacy), len(edges))
	}
}

// TestBudgetedPass_CancelMidRun pins that cancellation is honored inside the
// pair loop — the 32s clones pass must die on cancel, not run to completion.
// The timeout (1ms) vs the work (millions of pairs) leaves no flake margin.
func TestBudgetedPass_CancelMidRun(t *testing.T) {
	docs := identicalSigDocs(3000) // ~4.5M pairs: no machine finishes in 1ms
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, _, err := EdgesFromSignaturesContext(ctx, "p", docs, 0.7, Limits{MaxPairs: 1 << 30, MaxEdges: 1 << 30})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mid-run cancel must fail with DeadlineExceeded, got %v", err)
	}
}

// TestBudgetedPass_PreCanceledFailsFast pins the trivial direction too.
func TestBudgetedPass_PreCanceledFailsFast(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := EdgesFromSignaturesContext(ctx, "p", identicalSigDocs(10), 0.7, DefaultLimits())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled pass must fail fast, got %v", err)
	}
}

// TestLimitsFromEnv pins the debug overrides and the no-unlimited rule.
func TestLimitsFromEnv(t *testing.T) {
	t.Setenv("CODEGRAPH_SIMILAR_MAX_PAIRS", "1234")
	t.Setenv("CODEGRAPH_SIMILAR_MAX_EDGES", "56")
	lim := LimitsFromEnv()
	if lim.MaxPairs != 1234 || lim.MaxEdges != 56 {
		t.Fatalf("env overrides not honored: %+v", lim)
	}
	t.Setenv("CODEGRAPH_SIMILAR_MAX_PAIRS", "not-a-number")
	t.Setenv("CODEGRAPH_SIMILAR_MAX_EDGES", "-5")
	lim = LimitsFromEnv()
	if lim.MaxPairs != DefaultMaxPairs || lim.MaxEdges != DefaultMaxEdges {
		t.Fatalf("bad env values must fall back to defaults: %+v", lim)
	}
	want := "minhash-lsh-v1+pairs=1234+edges=56"
	if got := (Limits{MaxPairs: 1234, MaxEdges: 56}).Version(); got != want {
		t.Fatalf("version = %q, want %q", got, want)
	}
	if DefaultLimits().Version() == (Limits{MaxPairs: 1}.Version()) {
		t.Fatal("version must distinguish budgets")
	}
}
