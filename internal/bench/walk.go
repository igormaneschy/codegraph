package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/query"
)

type WalkSample struct {
	Tool             string `json:"tool"`
	Target           string `json:"target"`
	Limit            int    `json:"limit"`
	Pages            int    `json:"pages"`
	Bytes            int    `json:"bytes"`
	Results          int    `json:"results"`
	WallNS           int64  `json:"wall_ns"`
	IdentityDigest   string `json:"identity_digest"`
	Equivalent       bool   `json:"store_equivalent"`
	ExpectedVerified bool   `json:"expected_verified"`
}

type refPager func(cursor string) (query.RefPage, error)

func measureWalks(ctx context.Context, store *graph.Store, request SampleRequest, project string) ([]WalkSample, error) {
	engine := query.NewEngine(store, project, request.Root)
	measurements := make([]WalkSample, 0, len(request.Queries))
	for _, item := range request.Queries {
		reference, err := referenceIdentities(ctx, store, project, item)
		if err != nil {
			return nil, err
		}
		measurement, identities, err := walkPages(ctx, item, enginePager(engine, item), 100000)
		if err != nil {
			return nil, err
		}
		if !slices.Equal(reference, identities) {
			return nil, fmt.Errorf("%s %q: paginated identities/order differ from store enumeration", item.Tool, item.Target)
		}
		if item.Expected != nil && !slices.Equal(item.Expected, identities) {
			return nil, fmt.Errorf("%s %q: identities differ from independent expected CALLS", item.Tool, item.Target)
		}
		if measurement.Results < item.MinResults {
			return nil, fmt.Errorf("%s %q returned %d refs; require at least %d", item.Tool, item.Target, measurement.Results, item.MinResults)
		}
		measurement.Equivalent, measurement.ExpectedVerified = true, item.Expected != nil
		measurements = append(measurements, measurement)
	}
	return measurements, nil
}

func enginePager(engine *query.Engine, item MatrixQuery) refPager {
	return func(cursor string) (query.RefPage, error) {
		switch item.Tool {
		case "callers":
			return engine.CallersPage(item.Target, item.Limit, cursor)
		case "callees":
			return engine.CalleesPage(item.Target, item.Limit, cursor)
		default:
			return engine.SearchPage(item.Target, "", item.Limit, cursor)
		}
	}
}

func walkPages(ctx context.Context, item MatrixQuery, pager refPager, maxPages int) (WalkSample, []string, error) {
	measurement := WalkSample{Tool: item.Tool, Target: item.Target, Limit: item.Limit}
	var identities []string
	seen := map[string]bool{}
	cursor, generation := "", ""
	started := time.Now()
	for measurement.Pages < maxPages {
		if err := ctx.Err(); err != nil {
			return measurement, nil, err
		}
		page, err := pager(cursor)
		if err != nil {
			return measurement, nil, err
		}
		if measurement.Pages > 0 && generation != page.Generation {
			return measurement, nil, fmt.Errorf("generation changed during full query walk")
		}
		generation = page.Generation
		measurement.Pages++
		measurement.Bytes += len(page.WireText())
		for _, ref := range page.Refs {
			identities = append(identities, query.StripProjectPrefix(ref.QualifiedName))
		}
		if !page.HasMore {
			return finishWalk(measurement, identities, time.Since(started))
		}
		if page.Cursor == "" || page.Cursor == "-" || seen[page.Cursor] || len(page.Refs) == 0 {
			return measurement, nil, fmt.Errorf("nonprogressing query cursor %q", page.Cursor)
		}
		seen[page.Cursor], cursor = true, page.Cursor
	}
	return measurement, nil, fmt.Errorf("query exceeded %d pages without terminal result", maxPages)
}

func finishWalk(measurement WalkSample, identities []string, elapsed time.Duration) (WalkSample, []string, error) {
	encoded, err := json.Marshal(identities)
	if err != nil {
		return measurement, nil, err
	}
	digest := sha256.Sum256(encoded)
	measurement.Results, measurement.WallNS, measurement.IdentityDigest = len(identities), elapsed.Nanoseconds(), hex.EncodeToString(digest[:])
	return measurement, identities, nil
}

func referenceIdentities(ctx context.Context, store *graph.Store, project string, item MatrixQuery) ([]string, error) {
	var identities []string
	for offset := 0; ; offset += 1000 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		refs, err := referenceBatch(store, project, item, offset)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			identities = append(identities, query.StripProjectPrefix(ref.QualifiedName))
		}
		if len(refs) < 1000 {
			return identities, nil
		}
	}
}

func referenceBatch(store *graph.Store, project string, item MatrixQuery, offset int) ([]graph.RefNode, error) {
	if item.Tool == "search" {
		return store.SearchRefs(project, item.Target, "", 1000, offset)
	}
	direction := "in"
	if item.Tool == "callees" {
		direction = "out"
	}
	return store.NeighborRefs(project, project+":"+item.Target, direction, string(graph.EdgeCalls), 1000, offset)
}
