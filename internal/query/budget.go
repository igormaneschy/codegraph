package query

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// P2 shared response budget. One definition serves CLI and MCP so both entry
// points page identically; byte budgets always prevail over counts. The numbers
// below are the plan's validated starting point (P0: a 600-caller hub answer is
// ~28 KiB, so 500 refs sit under the byte cap; a whole-file snippet reached
// 488 KiB, so the snippet cap is the binding constraint there).
const (
	// DefaultPageRefs is the page size when the caller passes limit 0.
	DefaultPageRefs = 500
	// MaxPageRefs clamps explicit giant limits before any allocation, so a
	// request cannot size buffers proportionally to itself. Clamping is not
	// silent truncation: the page carries has_more + a cursor.
	MaxPageRefs = 2000
	// MaxPageBytes caps the encoded TSV payload of one ref page. The page
	// stops before the first line that would exceed it — except a single
	// oversize ref, which is emitted whole (never split: a cut
	// qualified_name would be an invalid reference).
	MaxPageBytes = 32 * 1024

	// DefaultSnippetLines / MaxSnippetLines bound one snippet page by lines.
	DefaultSnippetLines = 200
	MaxSnippetLines     = 2000
	// MaxSnippetBytes caps one snippet page by bytes, prevailing over lines.
	MaxSnippetBytes = 32 * 1024
)

// checkRefLimit validates a caller-supplied ref limit before any allocation:
// 0 means "default", negatives are an actionable error, giants clamp to
// MaxPageRefs (the cursor keeps the answer verifiable).
func checkRefLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultPageRefs, nil
	}
	if limit < 0 {
		return 0, fmt.Errorf("invalid limit %d: want 0 (default) or a positive page size up to %d", limit, MaxPageRefs)
	}
	if limit > MaxPageRefs {
		return MaxPageRefs, nil
	}
	return limit, nil
}

// checkSnippetLines validates a snippet line budget: 0 means default,
// negatives are an actionable error, giants clamp.
func checkSnippetLines(lines int) (int, error) {
	if lines == 0 {
		return DefaultSnippetLines, nil
	}
	if lines < 0 {
		return 0, fmt.Errorf("invalid line budget %d: want 0 (default) or a positive page size up to %d", lines, MaxSnippetLines)
	}
	if lines > MaxSnippetLines {
		return MaxSnippetLines, nil
	}
	return lines, nil
}

// pageCursor is the opaque continuation token. gen binds the page to the
// served graph generation (a cursor from another generation is rejected with
// orientation to restart — never mixed snapshots); fp binds it to the exact
// query (tool + normalized target + direction + type + page size), so a cursor
// cannot wander into a different question; off is the row offset, exact
// because the graph is immutable within a generation.
type pageCursor struct {
	V   int    `json:"v"`
	Gen string `json:"gen"`
	Fp  string `json:"fp"`
	Off int    `json:"off"`
}

// snippetCursor continues a snippet page losslessly: line is the 1-based next
// line, end the caller's end_line bound (0 = EOF), fsize the file size observed
// on page one (a concurrent modification fails the next page with an
// actionable error instead of silently shifting lines). Pages always resume at
// a line start: a single line longer than the byte budget is emitted whole
// (marked long_line) rather than split, so a cut qualified reference can never
// strand half a line.
type snippetCursor struct {
	V     int    `json:"v"`
	Gen   string `json:"gen"`
	File  string `json:"file"`
	Line  int    `json:"line"`
	End   int    `json:"end"`
	Fsize int64  `json:"fsize"`
}

func encodeCursor(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "-"
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// shortDigest renders a generation id: 12 hex chars, or "none".
func shortDigest(d string) string {
	if d == "" {
		return "none"
	}
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

func decodeRefCursor(token string) (pageCursor, error) {
	var c pageCursor
	if token == "" || token == "-" {
		return c, fmt.Errorf("empty cursor: restart the query from its first page")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return c, fmt.Errorf("malformed cursor: restart the query from its first page")
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("malformed cursor: restart the query from its first page")
	}
	if c.V != 1 || c.Off < 0 {
		return c, fmt.Errorf("unsupported cursor: restart the query from its first page")
	}
	return c, nil
}

func decodeSnippetCursor(token string) (snippetCursor, error) {
	var c snippetCursor
	if token == "" || token == "-" {
		return c, fmt.Errorf("empty cursor: restart the snippet from its first page")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return c, fmt.Errorf("malformed cursor: restart the snippet from its first page")
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("malformed cursor: restart the snippet from its first page")
	}
	if c.V != 1 || c.Line < 1 || c.Fsize < 0 {
		return c, fmt.Errorf("unsupported cursor: restart the snippet from its first page")
	}
	return c, nil
}
