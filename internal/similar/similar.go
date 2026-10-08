package similar

import (
	"context"
	"os"
	"sort"
	"strconv"

	"github.com/Lordymine/codegraph/internal/graph"
)

// Tuning for the SIMILAR_TO pass: 128 hashes split into 32 bands of 4 rows puts the
// LSH probability knee near Jaccard ~0.7 — pairs above are very likely to share a band
// (become candidates), pairs well below are very unlikely, so we avoid the O(n^2) scan.
const (
	shingleK  = 3
	numHashes = 128
	lshBands  = 32
	lshRows   = numHashes / lshBands
)

// Resource budget for the SIMILAR_TO pass (P4). The LSH pass is quadratic in
// the worst case — N identical bodies share every band, so one bucket holds
// all N docs and yields N(N-1)/2 candidate pairs (P0: 1200 clones → 720k pairs
// into a 145 MiB edge table). The budget applies BEFORE the unbounded
// seen-map/edge-slice growth: pairs stop being examined at MaxPairs, edges
// stop being emitted at MaxEdges. Kept output is always a deterministic prefix
// (sorted buckets, doc-ordered pairs, sorted edges), so a budgeted run is
// stable across runs and restarts — never a map-random sample.
const (
	DefaultMaxPairs = 250000
	DefaultMaxEdges = 50000
)

// Coverage statuses.
const (
	StatusComplete = "complete"
	StatusPartial  = "partial"
	StatusOmitted  = "omitted"
)

// Limits bounds one similarity pass. Non-positive fields mean "default".
type Limits struct {
	MaxPairs int
	MaxEdges int
}

// DefaultLimits returns the shipped budget: generous for real repos (which
// stay complete), binding for clone-bomb corpora.
func DefaultLimits() Limits { return Limits{MaxPairs: DefaultMaxPairs, MaxEdges: DefaultMaxEdges} }

// LimitsFromEnv returns the shipped budget unless overridden for debugging:
// CODEGRAPH_SIMILAR_MAX_PAIRS / CODEGRAPH_SIMILAR_MAX_EDGES. Unparsable or
// non-positive values fall back to the default (there is no "unlimited": the
// point of the budget is that it always exists).
func LimitsFromEnv() Limits {
	lim := DefaultLimits()
	if v, err := strconv.Atoi(os.Getenv("CODEGRAPH_SIMILAR_MAX_PAIRS")); err == nil && v > 0 {
		lim.MaxPairs = v
	}
	if v, err := strconv.Atoi(os.Getenv("CODEGRAPH_SIMILAR_MAX_EDGES")); err == nil && v > 0 {
		lim.MaxEdges = v
	}
	return lim
}

func (l Limits) withDefaults() Limits {
	if l.MaxPairs <= 0 {
		l.MaxPairs = DefaultMaxPairs
	}
	if l.MaxEdges <= 0 {
		l.MaxEdges = DefaultMaxEdges
	}
	return l
}

// Version identifies the algorithm plus the budget that produced an edge set.
// It is persisted in the manifest fingerprint: changing the algorithm or the
// budget invalidates no-op reuse, so a graph built under one budget is never
// certified as the output of another.
func (l Limits) Version() string {
	l = l.withDefaults()
	return "minhash-lsh-v1+pairs=" + strconv.Itoa(l.MaxPairs) + "+edges=" + strconv.Itoa(l.MaxEdges)
}

// Coverage describes how much of the similarity pass actually ran. A partial
// pass keeps a deterministic prefix of edges (never a random sample) and says
// so: clone answers from it may miss pairs.
type Coverage struct {
	Status        string `json:"status"` // complete | partial | omitted ("" = not recorded)
	Docs          int    `json:"docs"`
	PairsExamined int    `json:"pairs_examined"`
	PairsCapped   bool   `json:"pairs_capped"`
	EdgesEmitted  int    `json:"edges_emitted"`
	EdgesCapped   bool   `json:"edges_capped"`
}

// Summary renders one line for status tools and logs.
func (c Coverage) Summary() string {
	switch c.Status {
	case StatusComplete:
		return "complete"
	case StatusPartial:
		return "partial"
	case StatusOmitted:
		return "omitted"
	default:
		return "not recorded"
	}
}

// Notice renders the actionable context prepended to `similar` answers when
// coverage is incomplete. Empty when complete.
func (c Coverage) Notice() string {
	if c.Status == StatusComplete {
		return ""
	}
	if c.Status == StatusOmitted {
		return "similarity index omitted for this graph (low-memory host or CODEGRAPH_SKIP_SIMILAR): `similar` answers are empty, not evidence of no clones"
	}
	if c.Status != StatusPartial {
		return ""
	}
	reason := ""
	switch {
	case c.PairsCapped && c.EdgesCapped:
		reason = "pair and edge budgets hit"
	case c.PairsCapped:
		reason = "pair budget hit"
	case c.EdgesCapped:
		reason = "edge budget hit"
	default:
		reason = "stopped early"
	}
	return "similarity index partial (docs=" + strconv.Itoa(c.Docs) +
		", pairs examined=" + strconv.Itoa(c.PairsExamined) +
		", edges=" + strconv.Itoa(c.EdgesEmitted) + ", " + reason +
		"): clone answers may miss pairs; raise CODEGRAPH_SIMILAR_MAX_PAIRS/CODEGRAPH_SIMILAR_MAX_EDGES and reindex for full coverage"
}

// Doc is a symbol to compare for near-cloning: its qualified name and token stream.
type Doc struct {
	QN     string
	Tokens []string
}

// SigDoc carries a precomputed MinHash signature — used by the memory-budget indexer
// so tokenized function bodies are not all retained at once.
type SigDoc struct {
	QN  string
	Sig []uint64
}

// Edges returns SIMILAR_TO edges between docs whose estimated Jaccard similarity is at
// least threshold. Candidate pairs come from LSH banding (not an all-pairs scan); each
// surviving pair yields one symmetric pair (smaller QN -> larger) carrying the score.
// Docs too short to form a shingle are ignored (trivial bodies are not clones).
func Edges(project string, docs []Doc, threshold float64) []graph.Edge {
	sigDocs := make([]SigDoc, 0, len(docs))
	for _, d := range docs {
		if len(d.Tokens) >= shingleK {
			sigDocs = append(sigDocs, SigDoc{QN: d.QN, Sig: Signature(d.Tokens, shingleK, numHashes)})
		}
	}
	return EdgesFromSignatures(project, sigDocs, threshold)
}

// EdgesFromSignatures is like Edges but accepts precomputed signatures only.
// It runs under the default budget; prefer EdgesFromSignaturesContext when the
// caller needs coverage or cancellation.
func EdgesFromSignatures(project string, docs []SigDoc, threshold float64) []graph.Edge {
	edges, _, _ := EdgesFromSignaturesContext(context.Background(), project, docs, threshold, DefaultLimits())
	return edges
}

// EdgesFromSignaturesContext is the budgeted similarity pass. Candidate pairs
// are examined in deterministic order (buckets sorted by band+key, doc indexes
// ascending within a bucket), so stopping at the pair budget keeps a stable
// prefix across runs — never a map-random sample. The emitted edges are sorted
// and truncated to the edge budget, likewise deterministic. Cancellation is
// honored between pairs; a canceled pass returns an error and no partial
// edges (the pipeline keeps the previous graph instead).
func EdgesFromSignaturesContext(ctx context.Context, project string, docs []SigDoc, threshold float64, limits Limits) ([]graph.Edge, Coverage, error) {
	lim := limits.withDefaults()
	cov := Coverage{Status: StatusComplete, Docs: len(docs)}
	// Input order must not leak into budgeted output: the pipeline feeds docs
	// in map-iteration order, so sort by QN first — otherwise two identical
	// runs would keep different prefixes under a binding budget.
	sorted := append([]SigDoc(nil), docs...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].QN < sorted[b].QN })
	sigs := make([][]uint64, len(sorted))
	qns := make([]string, len(sorted))
	for i, d := range sorted {
		sigs[i] = d.Sig
		qns[i] = d.QN
	}
	edges, err := edgesFromSigsBudgeted(ctx, project, qns, sigs, threshold, lim, &cov)
	if err != nil {
		return nil, Coverage{Docs: len(docs)}, err
	}
	cov.EdgesEmitted = len(edges)
	if cov.PairsCapped || cov.EdgesCapped {
		cov.Status = StatusPartial
	}
	return edges, cov, nil
}

func edgesFromSigsBudgeted(ctx context.Context, project string, qns []string, sigs [][]uint64, threshold float64, lim Limits, cov *Coverage) ([]graph.Edge, error) {
	// LSH: bucket doc indices by (band, band-hash); a shared bucket is a candidate pair.
	type bucket struct{ band, key uint64 }
	buckets := map[bucket][]int{}
	for i, sig := range sigs {
		if sig == nil {
			continue
		}
		for b := 0; b < lshBands; b++ {
			bk := bucket{uint64(b), bandHash(sig[b*lshRows : (b+1)*lshRows])}
			buckets[bk] = append(buckets[bk], i)
		}
	}
	// Deterministic bucket order: map iteration is random, so sort. Members
	// stay in doc-index (insertion) order.
	keys := make([]bucket, 0, len(buckets))
	for bk := range buckets {
		keys = append(keys, bk)
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].band != keys[b].band {
			return keys[a].band < keys[b].band
		}
		return keys[a].key < keys[b].key
	})

	type pair struct{ i, j int }
	seen := map[pair]struct{}{}
	keep := &topEdges{cap: lim.MaxEdges}
	examined := 0
	capped := false
outer:
	for _, bk := range keys {
		idxs := buckets[bk]
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				if examined >= lim.MaxPairs {
					capped = true
					break outer
				}
				if examined%1024 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				i, j := idxs[a], idxs[b]
				if i > j {
					i, j = j, i
				}
				if _, ok := seen[pair{i, j}]; ok {
					continue
				}
				seen[pair{i, j}] = struct{}{}
				examined++
				score := EstJaccard(sigs[i], sigs[j])
				if score < threshold {
					continue
				}
				src, dst := qns[i], qns[j]
				if src > dst {
					src, dst = dst, src
				}
				keep.add(graph.Edge{
					Project: project, SourceQN: src, TargetQN: dst,
					Type:  graph.EdgeSimilarTo,
					Props: map[string]any{"similarity": round2(score)},
				})
			}
		}
	}
	cov.PairsExamined = examined
	cov.PairsCapped = capped
	cov.EdgesCapped = keep.overflow

	// The retained set is the same deterministic (source, target) prefix the old
	// collect-everything-then-sort-then-truncate produced, with memory bounded to
	// MaxEdges instead of MaxPairs (P4).
	return keep.sorted(), nil
}

// bandHash folds a band's rows into one key (FNV-1a over their bytes).
func bandHash(rows []uint64) uint64 {
	h := uint64(fnvOffset64)
	for _, v := range rows {
		for s := 0; s < 64; s += 8 {
			h ^= (v >> s) & 0xff
			h *= fnvPrime64
		}
	}
	return h
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
