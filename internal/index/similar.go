package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/memory"
	"github.com/Lordymine/codegraph/internal/similar"
)

// similarThreshold is the minimum estimated Jaccard for a SIMILAR_TO edge. 0.7 catches
// real near-clones (copy-paste, then rename + a small edit lands ~0.78) while still
// being high similarity over token trigrams — random functions sit near 0, so the
// false-positive risk is low. Tunable; a precision refinement (body-only tokenization,
// identifier normalization) could let it go higher.
const similarThreshold = 0.7

// similarReadFile is a narrow test seam for the source-read boundary. Production
// always uses os.ReadFile; failures are returned to the atomic pipeline instead of
// silently dropping similarity evidence.
var similarReadFile = os.ReadFile

// ResolveSimilar emits SIMILAR_TO edges between near-clone functions/methods. It reads
// each file once, tokenizes every function body, and runs the MinHash + LSH pass
// (internal/similar). Cross-file by nature, so it always runs on the full node set.
// Prefer resolveSimilarFromSpans during indexing — reuses CALLS spans and keeps
// only MinHash signatures in RAM, not tokenized bodies for every function.
func ResolveSimilar(project, root string, nodes []graph.Node) ([]graph.Edge, error) {
	byFile := map[string][]graph.Node{}
	for _, n := range nodes {
		if n.Label == graph.LabelFunction || n.Label == graph.LabelMethod {
			byFile[n.FilePath] = append(byFile[n.FilePath], n)
		}
	}
	return similarEdgesFromFiles(project, root, byFile)
}

// resolveSimilarFromSpans runs the SIMILAR_TO pass on spans already loaded for CALLS.
// Each file is read once; only MinHash signatures are retained.
func resolveSimilarFromSpans(ctx context.Context, project, root string, spans []graph.FunctionSpan) ([]graph.Edge, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	byFile := map[string][]graph.FunctionSpan{}
	for _, sp := range spans {
		byFile[sp.FilePath] = append(byFile[sp.FilePath], sp)
	}
	var sigDocs []similar.SigDoc
	for file, fns := range byFile {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := similarReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			return nil, fmt.Errorf("read source for similarity %q: %w", file, err)
		}
		lines := strings.Split(string(data), "\n")
		data = nil
		for _, sp := range fns {
			span, err := linesOf(lines, sp.StartLine, sp.EndLine)
			if err != nil {
				return nil, fmt.Errorf("read function span for similarity %q (%s:%d-%d): %w",
					sp.QualifiedName, file, sp.StartLine, sp.EndLine, err)
			}
			toks := similar.Tokenize(span)
			if len(toks) >= 3 {
				sigDocs = append(sigDocs, similar.SigDoc{
					QN:  sp.QualifiedName,
					Sig: similar.Signature(toks, 3, 128),
				})
			}
		}
		lines = nil
		memory.Gate()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return similar.EdgesFromSignatures(project, sigDocs, similarThreshold), nil
}

func similarEdgesFromFiles(project, root string, byFile map[string][]graph.Node) ([]graph.Edge, error) {
	var docs []similar.Doc
	for file, fns := range byFile {
		data, err := similarReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			return nil, fmt.Errorf("read source for similarity %q: %w", file, err)
		}
		lines := strings.Split(string(data), "\n")
		for _, n := range fns {
			span, err := linesOf(lines, n.StartLine, n.EndLine)
			if err != nil {
				return nil, fmt.Errorf("read function span for similarity %q (%s:%d-%d): %w",
					n.QualifiedName, file, n.StartLine, n.EndLine, err)
			}
			docs = append(docs, similar.Doc{
				QN:     n.QualifiedName,
				Tokens: similar.Tokenize(span),
			})
		}
	}
	return similar.Edges(project, docs, similarThreshold), nil
}

// linesOf returns the 1-based inclusive line range [start,end] joined by newlines.
func linesOf(lines []string, start, end int) (string, error) {
	if start < 1 || end < start || end > len(lines) {
		return "", fmt.Errorf("invalid line range %d-%d for %d-line source", start, end, len(lines))
	}
	return strings.Join(lines[start-1:end], "\n"), nil
}
