package similar

import (
	"context"
	"testing"
)

// BenchmarkBudgetedPass_CloneBomb measures a clone-bomb pass whose candidate edges
// vastly exceed MaxEdges. P4 keeps only MaxEdges in memory instead of every emitted
// edge up to MaxPairs; B/op is the metric that changes.
func BenchmarkBudgetedPass_CloneBomb(b *testing.B) {
	docs := identicalSigDocs(600) // ~179700 candidate pairs, all above threshold
	lim := Limits{MaxPairs: 200000, MaxEdges: 500}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := EdgesFromSignaturesContext(ctx, "p", docs, 0.7, lim); err != nil {
			b.Fatal(err)
		}
	}
}
