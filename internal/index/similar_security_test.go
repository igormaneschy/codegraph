package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/securefile"
)

func TestSimilarity_RejectsSourceReplacementAfterDefinitions(t *testing.T) {
	for _, replacement := range []string{"leaf symlink", "parent symlink", "directory"} {
		t.Run(replacement, func(t *testing.T) {
			root := securityPhysicalTempDir(t)
			const rel = "lib/source.rb"
			const source = "def alpha(x)\n  x + 1\nend\n"
			writeSecurityFile(t, root, rel, source)
			nodes, _, err := ExtractDefinitionsChecked("p", SourceFile{AbsPath: filepath.Join(root, rel), RelPath: rel, Lang: LangRuby})
			if err != nil {
				t.Fatal(err)
			}
			outside := securityPhysicalTempDir(t)
			writeSecurityFile(t, outside, "source.rb", source)
			path := filepath.Join(root, rel)
			target := filepath.Join(outside, "source.rb")
			if replacement == "parent symlink" {
				path, target = filepath.Dir(path), outside
			}
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			want := securefile.ErrUnsafePath
			if replacement == "directory" {
				want = securefile.ErrNotRegular
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlink creation unavailable: %v", err)
			}
			assertSimilarityReadRejected(t, root, nodes, want)
		})
	}
}

func TestSimilarity_RegularSourceProducesCloneEdges(t *testing.T) {
	root := securityPhysicalTempDir(t)
	const rel = "source.go"
	body := "(x int) int {\n a := x + 1\n b := a * 2\n c := b - 3\n d := c * c\n e := d + a\n f := e - b\n return f * 10 + a - c\n}\n"
	writeSecurityFile(t, root, rel, "package symbols\nfunc alpha"+body+"func beta"+body)
	nodes, _, err := ExtractDefinitionsChecked("p", SourceFile{AbsPath: filepath.Join(root, rel), RelPath: rel, Lang: LangGo})
	if err != nil {
		t.Fatal(err)
	}
	edges, err := ResolveSimilar("p", root, nodes)
	assertCloneEdge(t, edges, err)
	var spans []graph.FunctionSpan
	for _, node := range nodes {
		if node.Label == graph.LabelFunction {
			spans = append(spans, graph.FunctionSpan{QualifiedName: node.QualifiedName, FilePath: node.FilePath, StartLine: node.StartLine, EndLine: node.EndLine})
		}
	}
	edges, coverage, err := resolveSimilarFromSpans(context.Background(), "p", root, spans)
	assertCloneEdge(t, edges, err)
	if coverage.Status != "complete" || coverage.Docs != 2 {
		t.Fatalf("coverage=%+v, want complete with two docs", coverage)
	}
}

func assertCloneEdge(t *testing.T, edges []graph.Edge, err error) {
	t.Helper()
	if err != nil || len(edges) != 1 || edges[0].SourceQN != "p:source.go.alpha" || edges[0].TargetQN != "p:source.go.beta" || edges[0].Type != graph.EdgeSimilarTo {
		t.Fatalf("clone edges=%+v err=%v, want alpha SIMILAR_TO beta", edges, err)
	}
}

func assertSimilarityReadRejected(t *testing.T, root string, nodes []graph.Node, want error) {
	t.Helper()
	var spans []graph.FunctionSpan
	for _, node := range nodes {
		if node.Label == graph.LabelFunction || node.Label == graph.LabelMethod {
			spans = append(spans, graph.FunctionSpan{QualifiedName: node.QualifiedName, FilePath: node.FilePath, StartLine: node.StartLine, EndLine: node.EndLine})
		}
	}
	if len(spans) != 1 {
		t.Fatalf("fixture spans=%+v, want one real method", spans)
	}
	edges, err := ResolveSimilar("p", root, nodes)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), spans[0].FilePath) || len(edges) != 0 {
		t.Fatalf("ResolveSimilar edges=%+v err=%v, want %v", edges, err, want)
	}
	edges, coverage, err := resolveSimilarFromSpans(context.Background(), "p", root, spans)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), spans[0].FilePath) || len(edges) != 0 || coverage.Status == "complete" {
		t.Fatalf("span similarity edges=%+v coverage=%+v err=%v, want %v", edges, coverage, err, want)
	}
}
