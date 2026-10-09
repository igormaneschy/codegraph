package bench

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Lordymine/codegraph/internal/query"
)

func physicalTemp(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSampleReportsProductionDecisionsAndIndependentCalls(t *testing.T) {
	root := physicalTemp(t)
	if err := os.WriteFile(filepath.Join(root, "gateway.rb"), []byte("class Gateway\n def self.run\n end\nend\nclass Checkout\n def process\n  ::Gateway.run\n end\nend\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := SampleRequest{Root: root, Database: filepath.Join(physicalTemp(t), "sample.db"), Queries: []MatrixQuery{{Tool: "callees", Target: "gateway.rb.Checkout#process", Limit: 1, Expected: []string{"gateway.rb.Gateway.run"}}, {Tool: "search", Target: "run", Limit: 1}}}
	first, err := Sample(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "healthy" || first.Metrics.Decision != "rebuild" || first.Metrics.Outcome != "success" || first.SelfPeakRSSBytes == 0 || first.GoAllocBytes == 0 || len(first.Digest) != 64 || !first.Queries[0].ExpectedVerified {
		t.Fatalf("sample=%+v", first)
	}
	unchanged, err := Sample(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Metrics.Decision != "noop" || unchanged.Digest != first.Digest {
		t.Fatalf("unchanged=%+v", unchanged)
	}
	request.Queries[0].MinResults = 2
	if _, err := Sample(context.Background(), request); err == nil {
		t.Fatal("query below explicit result minimum accepted")
	}
	request.Queries[0].MinResults = 0
	request.Queries[0].Expected = []string{"gateway.rb.Wrong.run"}
	if _, err := Sample(context.Background(), request); err == nil {
		t.Fatal("wrong independent CALLS accepted")
	}
}

func TestSampleRejectsInvalidRequestsAndCancellation(t *testing.T) {
	for _, request := range []SampleRequest{{}, {Root: "root"}, {Root: "root", Database: "db", Queries: []MatrixQuery{{Tool: "unknown", Target: "target", Limit: 1}}}, {Root: "root", Database: "db", Queries: []MatrixQuery{{Tool: "search", Target: "x", Limit: 2001}}}} {
		if _, err := Sample(context.Background(), request); err == nil {
			t.Fatalf("invalid request %+v accepted", request)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sample, err := Sample(ctx, SampleRequest{Root: physicalTemp(t), Database: filepath.Join(physicalTemp(t), "db")})
	if err == nil || sample.Metrics.Outcome != "cancelled" {
		t.Fatalf("sample=%+v err=%v", sample, err)
	}
}

type fakeRefPager struct{ pages []query.RefPage }

func (pager *fakeRefPager) next(_ string) (query.RefPage, error) {
	page := pager.pages[0]
	pager.pages = pager.pages[1:]
	return page, nil
}

func TestWalkPagesRetainsOrderAndMetersAllPages(t *testing.T) {
	pager := &fakeRefPager{pages: []query.RefPage{
		{Refs: []query.Ref{{QualifiedName: "test:a.go.Run"}}, HasMore: true, Cursor: "next", Generation: "g"},
		{Refs: []query.Ref{{QualifiedName: "test:b.go.Run"}}, Cursor: "-", Generation: "g"},
	}}
	expectedBytes := len(pager.pages[0].WireText()) + len(pager.pages[1].WireText())
	measurement, identities, err := walkPages(context.Background(), MatrixQuery{Tool: "callers", Target: "x", Limit: 1}, pager.next, 2)
	if err != nil || measurement.Pages != 2 || measurement.Bytes != expectedBytes || measurement.Results != 2 || !slices.Equal(identities, []string{"a.go.Run", "b.go.Run"}) {
		t.Fatalf("measurement=%+v identities=%v err=%v", measurement, identities, err)
	}
}

func TestWalkPagesRejectsIncompleteOrChangingWalks(t *testing.T) {
	ref := []query.Ref{{QualifiedName: "test:a.go.Run"}}
	for _, pages := range [][]query.RefPage{
		{{Refs: ref, HasMore: true, Cursor: "-"}},
		{{HasMore: true, Cursor: "next"}},
		{{Refs: ref, HasMore: true, Cursor: "next", Generation: "g"}, {Refs: ref, HasMore: true, Cursor: "next", Generation: "g"}},
		{{Refs: ref, HasMore: true, Cursor: "next", Generation: "g"}, {Refs: ref, Generation: "other"}},
	} {
		pager := &fakeRefPager{pages: pages}
		if _, _, err := walkPages(context.Background(), MatrixQuery{}, pager.next, len(pages)); err == nil {
			t.Fatal("incomplete/changing walk accepted")
		}
	}
	pager := &fakeRefPager{pages: []query.RefPage{{Refs: ref, HasMore: true, Cursor: "next"}}}
	if _, _, err := walkPages(context.Background(), MatrixQuery{}, pager.next, 1); err == nil {
		t.Fatal("page cap accepted as complete")
	}
}

func TestSelfPeakRSSMeasurementIsAvailable(t *testing.T) {
	rss, err := selfPeakRSSBytes()
	if err != nil || rss == 0 {
		t.Fatalf("RSS=%d err=%v", rss, err)
	}
}
