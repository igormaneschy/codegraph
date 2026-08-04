package index

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

// cancelAfterChecksContext is a deterministic phase-boundary test context. It
// cancels on a specific Err check, so cancellation tests do not depend on sleeps
// or filesystem timing.
type cancelAfterChecksContext struct {
	context.Context
	cancel context.CancelFunc
	limit  int
	checks atomic.Int32
}

func (c *cancelAfterChecksContext) Err() error {
	if int(c.checks.Add(1)) >= c.limit {
		c.cancel()
	}
	return c.Context.Err()
}

func newCancelAfterChecksContext(limit int) (*cancelAfterChecksContext, context.CancelFunc) {
	base, cancel := context.WithCancel(context.Background())
	return &cancelAfterChecksContext{Context: base, cancel: cancel, limit: limit}, cancel
}

func TestDiscoverCanonicalContextCancelsPerEntry(t *testing.T) {
	root := t.TempDir()
	const fileCount = 64
	for i := 0; i < fileCount; i++ {
		writeFile(t, root, fmt.Sprintf("src/file-%03d.go", i), "package src\n")
	}

	ctx, cancel := newCancelAfterChecksContext(5)
	defer cancel()
	files, err := discoverCanonicalContext(ctx, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("discovery error = %v, want context.Canceled", err)
	}
	if len(files) >= fileCount {
		t.Fatalf("discovery processed all %d files after cancellation: %d", fileCount, len(files))
	}
	if ctx.checks.Load() < 2 {
		t.Fatalf("discovery cancellation was not observed during the walk: checks=%d", ctx.checks.Load())
	}
}

func TestCollectImportsStreamingContextCancelsDuringFileCollection(t *testing.T) {
	root := t.TempDir()
	const fileCount = 32
	files := make([]SourceFile, 0, fileCount+1)
	writeFile(t, root, "dep.ts", "export const dep = 1\n")
	files = append(files, SourceFile{AbsPath: filepath.Join(root, "dep.ts"), RelPath: "dep.ts", Lang: LangTS})
	for i := 0; i < fileCount; i++ {
		rel := filepath.ToSlash(fmt.Sprintf("src/file-%03d.ts", i))
		writeFile(t, root, rel, "import { dep } from '../dep'\nexport const value = dep\n")
		files = append(files, SourceFile{AbsPath: filepath.Join(root, filepath.FromSlash(rel)), RelPath: rel, Lang: LangTS})
	}

	// The first 2N+3 checks cover the existence pass, the Ruby-load-path
	// inspection pass, and the first source-file boundary. Cancel on the next
	// source boundary, after deterministic partial work but before the batch ends.
	ctx, cancel := newCancelAfterChecksContext(2*len(files) + 4)
	defer cancel()
	edges, err := collectImportsStreamingContext(ctx, "p", files)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("import collection error = %v, want context.Canceled", err)
	}
	if len(edges) == 0 || len(edges) >= fileCount {
		t.Fatalf("import collection did not stop partway through files: edges=%d want 1..%d", len(edges), fileCount-1)
	}
}

func TestForEachReusableCallEdgeContextCancelsPerEdge(t *testing.T) {
	project := "p"
	dir := t.TempDir()
	source, err := graph.Open(filepath.Join(dir, "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	const edgeCount = 16
	nodes := make([]graph.Node, 0, edgeCount*2)
	edges := make([]graph.Edge, 0, edgeCount)
	for i := 0; i < edgeCount; i++ {
		sourceQN := fmt.Sprintf("%s:source-%02d.go.f", project, i)
		targetQN := fmt.Sprintf("%s:target-%02d.go.f", project, i)
		nodes = append(nodes,
			graph.Node{Project: project, Label: graph.LabelFunction, Name: "source", QualifiedName: sourceQN, FilePath: fmt.Sprintf("source-%02d.go", i)},
			graph.Node{Project: project, Label: graph.LabelFunction, Name: "target", QualifiedName: targetQN, FilePath: fmt.Sprintf("target-%02d.go", i)},
		)
		edges = append(edges, graph.Edge{Project: project, SourceQN: sourceQN, TargetQN: targetQN, Type: graph.EdgeCalls})
	}
	if err := source.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.InsertEdges(edges); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := newCancelAfterChecksContext(4)
	defer cancel()
	seen := 0
	err = forEachReusableCallEdgeContext(ctx, source, project, map[string]bool{}, nil, func(graph.Edge) error {
		seen++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("reused CALLS streaming error = %v, want context.Canceled", err)
	}
	if seen == 0 || seen >= edgeCount {
		t.Fatalf("reused CALLS streaming processed %d/%d edges after cancellation", seen, edgeCount)
	}
}
