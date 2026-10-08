package query

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

// TestEngine_Architecture pins the repo map: from graph aggregates it reports
// languages (by File node), node/edge counts, top packages (by symbols per dir),
// and the two hotspot rankings — by cyclomatic complexity (the M4 data) and by
// inbound CALLS (call hubs).
func TestEngine_Architecture(t *testing.T) {
	store, err := graph.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const p = "proj"
	file := func(path, lang string) graph.Node {
		return graph.Node{Project: p, Label: graph.LabelFile, Name: path, QualifiedName: p + ":" + path,
			FilePath: path, Props: map[string]any{"lang": lang}}
	}
	fn := func(name, path string, cx int) graph.Node {
		return graph.Node{Project: p, Label: graph.LabelFunction, Name: name, QualifiedName: p + ":" + path + "." + name,
			FilePath: path, StartLine: 1, EndLine: 2, Props: map[string]any{"complexity": cx}}
	}
	if err := store.InsertNodes([]graph.Node{
		file("a.go", "go"), file("b.ts", "ts"), file("internal/util.go", "go"),
		fn("simple", "a.go", 1), fn("branchy", "a.go", 5), fn("hub", "b.ts", 2),
		fn("helper", "internal/util.go", 1),
	}); err != nil {
		t.Fatal(err)
	}
	// simple and branchy both call hub → hub is the call hub (2 inbound).
	if _, _, err := store.InsertEdges([]graph.Edge{
		{Project: p, SourceQN: p + ":a.go.simple", TargetQN: p + ":b.ts.hub", Type: graph.EdgeCalls},
		{Project: p, SourceQN: p + ":a.go.branchy", TargetQN: p + ":b.ts.hub", Type: graph.EdgeCalls},
	}); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(store, p, t.TempDir())
	arch, err := eng.Architecture(10)
	if err != nil {
		t.Fatalf("Architecture: %v", err)
	}

	if arch.Languages["go"] != 2 || arch.Languages["ts"] != 1 {
		t.Errorf("languages = %v, want go=2 ts=1", arch.Languages)
	}
	if arch.NodeCounts["Function"] != 4 || arch.NodeCounts["File"] != 3 {
		t.Errorf("node counts = %v, want Function=4 File=3", arch.NodeCounts)
	}
	if arch.EdgeCounts["CALLS"] != 2 {
		t.Errorf("edge counts = %v, want CALLS=2", arch.EdgeCounts)
	}
	if len(arch.ComplexityHotspots) == 0 || arch.ComplexityHotspots[0].Ref.Name != "branchy" || arch.ComplexityHotspots[0].Metric != 5 {
		t.Errorf("top complexity hotspot = %+v, want branchy/5", arch.ComplexityHotspots)
	}
	if len(arch.CallHubs) == 0 || arch.CallHubs[0].Ref.Name != "hub" || arch.CallHubs[0].Metric != 2 {
		t.Errorf("top call hub = %+v, want hub/2", arch.CallHubs)
	}
	pkg := map[string]int{}
	for _, ps := range arch.Packages {
		pkg[ps.Dir] = ps.Symbols
	}
	if pkg["internal"] != 1 {
		t.Errorf("packages = %v, want dir 'internal' with 1 symbol", arch.Packages)
	}
}

// TestEngine_ArchitectureClampsTopN pins R16: a giant top_n cannot request an
// unbounded hotspot/package render; it clamps to MaxArchitectureTopN.
func TestEngine_ArchitectureClampsTopN(t *testing.T) {
	store, err := graph.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const p = "proj"
	if err := store.InsertNodes([]graph.Node{
		{Project: p, Label: graph.LabelFile, Name: "a.go", QualifiedName: p + ":a.go", FilePath: "a.go", Props: map[string]any{"lang": "go"}},
	}); err != nil {
		t.Fatal(err)
	}
	nodes := make([]graph.Node, 0, MaxArchitectureTopN+50)
	for i := 0; i < MaxArchitectureTopN+50; i++ {
		name := fmt.Sprintf("fn%04d", i)
		nodes = append(nodes, graph.Node{Project: p, Label: graph.LabelFunction, Name: name,
			QualifiedName: p + ":a.go." + name, FilePath: "a.go", StartLine: 1, EndLine: 2,
			Props: map[string]any{"complexity": i + 1}})
	}
	if err := store.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(store, p, t.TempDir())
	arch, err := eng.Architecture(1 << 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(arch.ComplexityHotspots) != MaxArchitectureTopN {
		t.Fatalf("hotspots=%d, want the clamp %d", len(arch.ComplexityHotspots), MaxArchitectureTopN)
	}
}

// TestEngine_ArchitectureCacheAndReopenInvalidation pins P6: a repeated
// orientation call is served from the cache while the served generation is
// unchanged, and reopening the engine (a new generation) recomputes it.
func TestEngine_ArchitectureCacheAndReopenInvalidation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "g.db")
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const p = "proj"
	fn := func(name string) graph.Node {
		return graph.Node{Project: p, Label: graph.LabelFunction, Name: name,
			QualifiedName: p + ":a.go." + name, FilePath: "a.go", StartLine: 1, EndLine: 2}
	}
	if err := store.InsertNodes([]graph.Node{fn("one"), fn("two")}); err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(store, p, t.TempDir())
	first, err := eng.Architecture(10)
	if err != nil {
		t.Fatal(err)
	}
	if first.NodeCounts["Function"] != 2 {
		t.Fatalf("first Architecture functions=%d, want 2", first.NodeCounts["Function"])
	}

	// Mutating the store without reopening does not change the served generation,
	// so the cached aggregate is returned (the graph is immutable per generation).
	if err := store.InsertNodes([]graph.Node{fn("three")}); err != nil {
		t.Fatal(err)
	}
	cached, err := eng.Architecture(10)
	if err != nil {
		t.Fatal(err)
	}
	if cached.NodeCounts["Function"] != 2 {
		t.Fatalf("cache miss: functions=%d, want the cached 2", cached.NodeCounts["Function"])
	}

	// Reopening reloads the manifest and invalidates the cache.
	if err := eng.Reopen(dbPath); err != nil {
		t.Fatal(err)
	}
	fresh, err := eng.Architecture(10)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.NodeCounts["Function"] != 3 {
		t.Fatalf("reopen did not invalidate the cache: functions=%d, want 3", fresh.NodeCounts["Function"])
	}
}

func benchmarkArchitectureStore(b *testing.B, functions int) *Engine {
	b.Helper()
	store, err := graph.Open(filepath.Join(b.TempDir(), "g.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	const p = "proj"
	nodes := make([]graph.Node, 0, functions)
	for i := 0; i < functions; i++ {
		name := fmt.Sprintf("fn%05d", i)
		nodes = append(nodes, graph.Node{Project: p, Label: graph.LabelFunction, Name: name,
			QualifiedName: p + ":a.go." + name, FilePath: "a.go", StartLine: 1, EndLine: 2,
			Props: map[string]any{"complexity": i % 25}})
	}
	if err := store.InsertNodes(nodes); err != nil {
		b.Fatal(err)
	}
	return NewEngine(store, p, b.TempDir())
}

// BenchmarkEngine_ArchitectureCache measures the served path after the first call
// (P6): a linear scan of the cached struct, no SQL.
func BenchmarkEngine_ArchitectureCache(b *testing.B) {
	eng := benchmarkArchitectureStore(b, 3000)
	if _, err := eng.Architecture(50); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := eng.Architecture(50); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEngine_ArchitectureCompute measures the cached path's predecessor: the
// full aggregate queries every call.
func BenchmarkEngine_ArchitectureCompute(b *testing.B) {
	eng := benchmarkArchitectureStore(b, 3000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := eng.computeArchitecture(50); err != nil {
			b.Fatal(err)
		}
	}
}
