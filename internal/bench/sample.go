package bench

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/index"
)

const MatrixMethod = "production-matrix-v1"

// SampleRequest operates on an explicitly disposable repository and private DB.
// Example: Sample(ctx, SampleRequest{Root: root, Database: db}) runs RunAtomic.
type SampleRequest struct {
	Root     string        `json:"root"`
	Database string        `json:"database"`
	Queries  []MatrixQuery `json:"queries"`
}

type MatrixQuery struct {
	Tool       string   `json:"tool"`
	Target     string   `json:"target"`
	Limit      int      `json:"limit"`
	Expected   []string `json:"expected,omitempty"`
	MinResults int      `json:"min_results,omitempty"`
}

type IndexSample struct {
	Method           string            `json:"method"`
	Status           index.IndexStatus `json:"status"`
	Metrics          index.RunMetrics  `json:"metrics"`
	WallNS           int64             `json:"wall_ns"`
	GoAllocBytes     uint64            `json:"go_alloc_bytes"`
	GoMallocs        uint64            `json:"go_mallocs"`
	SelfPeakRSSBytes uint64            `json:"self_peak_rss_bytes"`
	Digest           string            `json:"graph_digest"`
	Provenance       SampleProvenance  `json:"provenance"`
	Files            int               `json:"files"`
	Nodes            int               `json:"nodes"`
	Edges            int               `json:"edges"`
	Queries          []WalkSample      `json:"queries"`
}

func Sample(ctx context.Context, request SampleRequest) (IndexSample, error) {
	if err := validateSampleRequest(request); err != nil {
		return IndexSample{}, err
	}
	sample, result, err := measureIndex(ctx, request)
	if err != nil {
		return sample, err
	}
	sample.Status, sample.Metrics = result.Status, result.Metrics
	sample.Files, sample.Nodes, sample.Edges = result.Files, result.Nodes, result.EdgesKept
	store, err := graph.Open(request.Database)
	if err != nil {
		return sample, err
	}
	defer store.Close()
	if err := store.ValidateIntegrity(); err != nil {
		return sample, err
	}
	sample.Provenance, err = sampleProvenance(request.Database)
	if err != nil {
		return sample, err
	}
	sample.Digest, err = store.LogicalGraphDigest(result.Project)
	if err != nil {
		return sample, err
	}
	sample.Queries, err = measureWalks(ctx, store, request, result.Project)
	return sample, err
}

func measureIndex(ctx context.Context, request SampleRequest) (IndexSample, index.Result, error) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	result, err := index.RunAtomicContext(ctx, request.Database, request.Root)
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	rss, rssErr := selfPeakRSSBytes()
	sample := IndexSample{Method: MatrixMethod, Status: result.Status, Metrics: result.Metrics, WallNS: elapsed.Nanoseconds(), GoAllocBytes: after.TotalAlloc - before.TotalAlloc, GoMallocs: after.Mallocs - before.Mallocs, SelfPeakRSSBytes: rss}
	if err != nil {
		return sample, result, err
	}
	return sample, result, rssErr
}

func validateSampleRequest(request SampleRequest) error {
	if request.Root == "" || request.Database == "" {
		return fmt.Errorf("invalid sample root=%q database=%q: want explicit disposable root and private database", request.Root, request.Database)
	}
	for _, query := range request.Queries {
		if query.Target == "" || query.Limit < 1 || query.Limit > 2000 || query.MinResults < 0 {
			return fmt.Errorf("invalid query %+v: want target and limit in [1,2000]", query)
		}
		switch query.Tool {
		case "callers", "callees", "search":
		default:
			return fmt.Errorf("unsupported sample tool %q: want callers, callees or search", query.Tool)
		}
	}
	return nil
}
