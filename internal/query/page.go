package query

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/similar"
)

// P2 paged answers. Every ref page ends with one `#` trailer line carrying
// has_more + cursor + generation, so a truncated answer never looks
// exhaustive. The trailer lives outside the TSV references: existing TSV
// consumers keep parsing lines, and the metadata is documented here, in the
// tool schemas, and in docs/ARCHITECTURE.md.

// RefPage is one page of TSV references plus its continuation state.
type RefPage struct {
	Refs       []Ref
	HasMore    bool
	Cursor     string // "-" when !HasMore
	Generation string // short manifest digest ("none" when unmanifested)
	Notice     string // similarity coverage for this page's served generation
}

// WireText renders the product wire format: compact TSV refs plus the trailer.
func (p RefPage) WireText() string {
	var b strings.Builder
	b.WriteString(CompactRefs(p.Refs))
	fmt.Fprintf(&b, "# has_more=%v cursor=%s generation=%s shown=%d\n",
		p.HasMore, p.Cursor, p.Generation, len(p.Refs))
	return b.String()
}

// SnippetPage is one page of source text plus its continuation state.
type SnippetPage struct {
	Text       string
	HasMore    bool
	Cursor     string // "-" when !HasMore
	Generation string
	FirstLine  int
	LastLine   int
	LongLine   bool
}

// WireText renders source text plus the trailer (range covered, long-line
// marker when a single line exceeded the byte budget).
func (p SnippetPage) WireText() string {
	var b strings.Builder
	b.WriteString(p.Text)
	if p.Text != "" {
		b.WriteByte('\n')
	}
	extra := ""
	if p.LongLine {
		extra = " long_line=true"
	}
	fmt.Fprintf(&b, "# has_more=%v cursor=%s generation=%s lines=%d-%d%s\n",
		p.HasMore, p.Cursor, p.Generation, p.FirstLine, p.LastLine, extra)
	return b.String()
}

// generation reports the manifest captured with this engine's database handle.
// An external writer may replace the path, but an open handle still serves its
// previous graph until Reopen; cursors must bind to that served generation.
func (e *Engine) generation() string {
	if e.manifestErr != nil {
		return "none"
	}
	return shortDigest(e.manifest.GraphContentDigest)
}

// refFingerprint binds a cursor to its exact question.
func refFingerprint(tool, qn, dir, edgeType string, limit int) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%d", tool, qn, dir, edgeType, limit)
	return strconv.FormatUint(h.Sum64(), 16)
}

// fetchRefPage runs the shared page assembly: fetch limit+1 rows, emit under
// the byte budget (at least one ref; an oversize single ref goes whole so a
// qualified_name is never cut into an invalid reference), and derive has_more
// without a COUNT.
func (e *Engine) fetchRefPage(tool, qn, dir, edgeType, rawQuery, label string, limit int, cursor string, fetch func(limit, offset int) ([]Ref, error)) (RefPage, error) {
	var out RefPage
	pageSize, err := checkRefLimit(limit)
	if err != nil {
		return out, err
	}
	gen := e.generation()
	out.Generation = gen
	fp := refFingerprint(tool, qn, dir, edgeType+rawQuery+label, pageSize)
	offset := 0
	if cursor != "" {
		c, err := decodeRefCursor(cursor)
		if err != nil {
			return out, err
		}
		if c.Gen != gen {
			return out, fmt.Errorf("cursor is from generation %s but the served graph is %s: restart the query from its first page", shortDigest(c.Gen), shortDigest(gen))
		}
		if c.Fp != fp {
			return out, fmt.Errorf("cursor belongs to a different query: restart the query from its first page")
		}
		offset = c.Off
	}
	rows, err := fetch(pageSize+1, offset)
	if err != nil {
		return out, err
	}
	probe := len(rows) == pageSize+1 // an extra row exists beyond this page
	if probe {
		rows = rows[:pageSize]
	}
	emit := 0
	acc := 0
	for i, r := range rows {
		line := compactRefLine(r)
		add := len(line) + 1 // the joined newline
		if acc+add > MaxPageBytes && i > 0 {
			break // budget cut: row i starts the next page
		}
		acc += add
		emit = i + 1
	}
	out.Refs = rows[:emit]
	more := probe || emit < len(rows)
	out.HasMore = more
	if more {
		out.Cursor = encodeCursor(pageCursor{V: 1, Gen: gen, Fp: fp, Off: offset + emit})
	} else {
		out.Cursor = "-"
	}
	return out, nil
}

func compactRefLine(r Ref) string {
	return r.Label + "\t" + r.Name + "\t" + r.File + ":" + strconv.Itoa(r.StartLine) + "\t" + StripProjectPrefix(r.QualifiedName)
}

// SearchPage: ranked BM25 search, paged. limit 0 = default; cursor continues
// an earlier page of the same query and generation.
func (e *Engine) SearchPage(q, label string, limit int, cursor string) (RefPage, error) {
	return e.fetchRefPage("search", q, "", "", q, label, limit, cursor,
		func(lim, off int) ([]Ref, error) {
			hits, err := e.store.SearchPage(e.project, q, label, lim, off)
			if err != nil {
				return nil, err
			}
			refs := make([]Ref, 0, len(hits))
			for _, h := range hits {
				refs = append(refs, refOf(h.Node))
			}
			return refs, nil
		})
}

// neighborsPage runs one CALLS/IMPORTS/SIMILAR_TO page over the normalized qn.
func (e *Engine) neighborsPage(tool, qn, dir, edgeType string, limit int, cursor string) (RefPage, error) {
	nqn := e.normalizeQN(qn)
	return e.fetchRefPage(tool, nqn, dir, edgeType, "", "", limit, cursor,
		func(lim, off int) ([]Ref, error) {
			ns, err := e.store.NeighborsPage(e.project, nqn, dir, edgeType, lim, off)
			if err != nil {
				return nil, err
			}
			refs := make([]Ref, 0, len(ns))
			for _, n := range ns {
				refs = append(refs, refOf(n))
			}
			return refs, nil
		})
}

// CallersPage: who calls this symbol, paged. Accepts a qualified_name with or
// without the project prefix (aliases normalize before the cursor binds, so a
// page started with an alias continues with the canonical form).
func (e *Engine) CallersPage(qualifiedName string, limit int, cursor string) (RefPage, error) {
	return e.neighborsPage("callers", qualifiedName, "in", "CALLS", limit, cursor)
}

// CalleesPage: what this symbol calls, paged.
func (e *Engine) CalleesPage(qualifiedName string, limit int, cursor string) (RefPage, error) {
	return e.neighborsPage("callees", qualifiedName, "out", "CALLS", limit, cursor)
}

// NeighborsPage: all related nodes, any edge type, both directions, paged.
// Dedup across directions/types is preserved (UNION, not UNION ALL).
func (e *Engine) NeighborsPage(qualifiedName string, limit int, cursor string) (RefPage, error) {
	return e.neighborsPage("neighbors", qualifiedName, "both", "", limit, cursor)
}

// SimilarPage: near-clone symbols, paged.
func (e *Engine) SimilarPage(qualifiedName string, limit int, cursor string) (RefPage, error) {
	page, err := e.neighborsPage("similar", qualifiedName, "both", "SIMILAR_TO", limit, cursor)
	if err != nil {
		return page, err
	}
	page.Notice = e.SimilarNotice()
	return page, nil
}

// SimilarCoverage reports how much of the SIMILAR_TO pass produced the served
// graph (complete/partial/omitted), read from the committed manifest — so it
// survives restarts and no-op reuse without re-running the pass.
func (e *Engine) SimilarCoverage() (similar.Coverage, error) {
	if e.manifestErr != nil {
		return similar.Coverage{}, e.manifestErr
	}
	return e.manifest.Similar, nil
}

// SimilarNotice returns the actionable context for `similar` answers when
// coverage is incomplete, or "" when the clone index is complete. An
// unreadable manifest yields no notice (no manifest, no claim either way).
func (e *Engine) SimilarNotice() string {
	cov, err := e.SimilarCoverage()
	if err != nil {
		return ""
	}
	return cov.Notice()
}

// DeadCodePage lists private Function/Method nodes the graph sees no caller for:
// zero inbound CALLS, minus the entry points whose callers can't be in-graph by
// design — exported symbols (public API), decorated members (framework-invoked),
// main/init, and test functions.
//
// It is a candidate list to investigate, NOT a delete list. Precision is bounded
// by CALLS recall: a real caller the resolver missed, or an indirect reference
// (function value, interface dispatch, reflection), makes a live function look
// dead. The agent must confirm each (e.g. grep the name) before acting.
//
// The candidate set is still materialized before paging (streaming is P5);
// memory here depends on the total, the page only bounds the answer.
// deadCodeRawBatch bounds one raw candidate batch pulled from the store while
// assembling a dead-code page. The page stops at pageSize+1 filtered refs, so
// memory depends on the batch — never on the candidate total — while an
// entry-point-heavy repo just costs extra cheap indexed batches, never a
// false end of results.
const deadCodeRawBatch = 256

func (e *Engine) DeadCodePage(limit int, cursor string) (RefPage, error) {
	var out RefPage
	pageSize, err := checkRefLimit(limit)
	if err != nil {
		return out, err
	}
	gen := e.generation()
	out.Generation = gen
	fp := refFingerprint("dead_code", "", "", "", pageSize)
	offset := 0
	rawStart := 0
	if cursor != "" {
		c, err := decodeRefCursor(cursor)
		if err != nil {
			return out, err
		}
		if c.Gen != gen {
			return out, fmt.Errorf("cursor is from generation %s but the served graph is %s: restart the query from its first page", shortDigest(c.Gen), shortDigest(gen))
		}
		if c.Fp != fp {
			return out, fmt.Errorf("cursor belongs to a different query: restart the query from its first page")
		}
		offset = c.Off
		rawStart = c.RawOff
	}
	// Stream raw batches, skipping entry points and already-served filtered
	// items, until the page plus its continuation probe are determined. Each
	// batch is dropped before the next is pulled: at most one batch plus one
	// page of refs is ever live.
	var rows []Ref
	skipped := 0
	if rawStart > 0 {
		skipped = offset
	}
	rawOffset := rawStart
	var rawPositions []int
	for len(rows) < pageSize+1 {
		raw, err := e.store.DeadCodeCandidates(e.project, rawOffset, deadCodeRawBatch)
		if err != nil {
			return out, err
		}
		if len(raw) == 0 {
			break
		}
		batchStart := rawOffset
		rawOffset += len(raw)
		for i, n := range raw {
			if isEntryPoint(n) {
				continue
			}
			if skipped < offset {
				skipped++
				continue
			}
			rows = append(rows, refOf(n))
			rawPositions = append(rawPositions, batchStart+i)
			if len(rows) == pageSize+1 {
				break
			}
		}
		if len(raw) < deadCodeRawBatch {
			break
		}
	}
	probe := len(rows) == pageSize+1
	if probe {
		rows = rows[:pageSize]
	}
	emit := 0
	acc := 0
	for i, r := range rows {
		add := len(compactRefLine(r)) + 1
		if acc+add > MaxPageBytes && i > 0 {
			break
		}
		acc += add
		emit = i + 1
	}
	out.Refs = rows[:emit]
	// probe (limit+1 filtered row) or a byte cut short of the fetched window
	// means more.
	out.HasMore = probe || emit < len(rows)
	if out.HasMore {
		out.Cursor = encodeCursor(pageCursor{V: 1, Gen: gen, Fp: fp, Off: offset + emit, RawOff: rawPositions[emit-1] + 1})
	} else {
		out.Cursor = "-"
	}
	return out, nil
}

// SnippetPage reads one source page. On the first call pass start/end (0 end =
// EOF) and no cursor; continue with the returned cursor (start is then
// ignored, end must match). limit caps page lines (0 = default 200). The byte
// budget always prevails; a single oversize line comes back whole, marked.
func (e *Engine) SnippetPage(filePath string, start, end, limit int, cursor string) (SnippetPage, error) {
	var out SnippetPage
	gen := e.generation()
	out.Generation = gen
	maxLines, err := checkSnippetLines(limit)
	if err != nil {
		return out, err
	}
	fromLine := start
	endBound := end
	expectSize := int64(-1)
	if cursor != "" {
		c, err := decodeSnippetCursor(cursor)
		if err != nil {
			return out, err
		}
		if c.Gen != gen {
			return out, fmt.Errorf("cursor is from generation %s but the served graph is %s: restart the snippet from its first page", shortDigest(c.Gen), shortDigest(gen))
		}
		if c.File != filePath {
			return out, fmt.Errorf("cursor belongs to file %q: restart the snippet from its first page", c.File)
		}
		if end != 0 && end != c.End {
			return out, fmt.Errorf("cursor was issued for a different end line: restart the snippet from its first page")
		}
		fromLine = c.Line
		endBound = c.End
		expectSize = c.Fsize
	} else {
		if end < 0 {
			return out, fmt.Errorf("invalid end line %d: want 0 (EOF) or a line >= start", end)
		}
		if end > 0 && start > end {
			return out, fmt.Errorf("bad range %d-%d: start is past end", start, end)
		}
	}
	chunk, err := graph.SnippetPaged(e.repoRoot, filePath, fromLine, maxLines, MaxSnippetBytes, endBound, expectSize)
	if err != nil {
		return out, err
	}
	out.Text = chunk.Text
	out.FirstLine = chunk.FirstLine
	out.LastLine = chunk.LastLine
	out.LongLine = chunk.LongLine
	out.HasMore = chunk.HasMore
	if chunk.HasMore {
		out.Cursor = encodeCursor(snippetCursor{V: 1, Gen: gen, File: filePath, Line: chunk.NextLine, End: endBound, Fsize: chunk.FileSize})
	} else {
		out.Cursor = "-"
	}
	return out, nil
}
