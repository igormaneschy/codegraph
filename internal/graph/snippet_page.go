package graph

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Lordymine/codegraph/internal/securefile"
)

// snippetBeforeOpenHook is a test seam for replacing a resolved path before open.
var snippetBeforeOpenHook func()

// SnippetChunk is one streamed page of a source file. Pages always cover whole
// lines: a single line longer than maxBytes is emitted whole with LongLine set
// (never split mid-line), so continuation is lossless by construction.
type SnippetChunk struct {
	Text      string // page lines joined by "\n", no trailing newline
	FirstLine int    // 1-based first line covered (0 when Text is empty)
	LastLine  int    // 1-based last line covered (0 when Text is empty)
	NextLine  int    // line the next page resumes at
	HasMore   bool   // more lines remain (within the end bound, if any)
	LongLine  bool   // the page is one line longer than the byte budget
	FileSize  int64  // observed file size, for cross-page change detection
}

// SnippetPaged streams one page of filePath from fromLine (1-based), stopping
// at endLine (0 = EOF), after maxLines lines, or before the first line that
// would push the payload past maxBytes — whichever comes first. Memory stays
// proportional to the page, not the file: lines are read incrementally and at
// most one line is held as lookahead. When expectSize >= 0 a size mismatch
// fails the page instead of silently shifting lines (the file changed between
// pages); pass -1 on the first page to record the size.
//
// The byte budget counts payload bytes (line content plus the joined
// newlines). A page holds at least one line: an oversize first line is
// returned whole with LongLine set and the next page resumes after it.
func SnippetPaged(repoRoot, filePath string, fromLine, maxLines, maxBytes, endLine int, expectSize int64) (SnippetChunk, error) {
	var out SnippetChunk
	if fromLine < 1 {
		fromLine = 1
	}
	if maxLines <= 0 {
		return out, fmt.Errorf("invalid line budget %d: want a positive page size", maxLines)
	}
	if maxBytes <= 0 {
		return out, fmt.Errorf("invalid byte budget %d: want a positive page size", maxBytes)
	}
	if endLine < 0 {
		return out, fmt.Errorf("invalid end line %d: want 0 (EOF) or a line >= start", endLine)
	}
	abs, err := resolveRepoFile(repoRoot, filePath)
	if err != nil {
		return out, err
	}
	if snippetBeforeOpenHook != nil {
		snippetBeforeOpenHook()
	}
	f, err := securefile.OpenRead(abs)
	if err != nil {
		return out, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return out, err
	}
	out.FileSize = info.Size()
	if expectSize >= 0 && out.FileSize != expectSize {
		return out, fmt.Errorf("file %q changed between pages (size %d, was %d): restart the snippet from its first page", filePath, out.FileSize, expectSize)
	}

	r := bufio.NewReaderSize(f, 64*1024)
	lineNo := 0
	var lines []string
	acc := 0
	out.NextLine = fromLine
	for {
		raw, atEnd, readErr := readSnippetLine(r, filePath)
		if readErr != nil {
			return out, readErr
		}
		if len(raw) == 0 && atEnd {
			break // clean EOF: nothing remains
		}
		lineNo++
		line := strings.TrimSuffix(raw, "\n")
		if lineNo < fromLine {
			if atEnd {
				break // skipped past EOF
			}
			continue
		}
		if endLine > 0 && lineNo > endLine {
			break // bound reached: no continuation within this query
		}
		if len(lines) >= maxLines {
			// Page full; lineNo is already read as the 1-line lookahead that
			// proves more remains — unless it sits past the end bound, in
			// which case the query is complete.
			out.NextLine = lineNo
			out.HasMore = endLine <= 0 || lineNo <= endLine
			break
		}
		add := len(line)
		if len(lines) > 0 {
			add++ // the joined newline
		}
		if acc+add > maxBytes && len(lines) > 0 {
			// Budget cut before lineNo: it starts the next page (same end-bound
			// caveat as above).
			out.NextLine = lineNo
			out.HasMore = endLine <= 0 || lineNo <= endLine
			break
		}
		lines = append(lines, line)
		acc += add
		out.NextLine = lineNo + 1
		if len(lines) == 1 && acc > maxBytes {
			out.LongLine = true // oversize single line, emitted whole
		}
		if atEnd {
			break // last line (no trailing newline) consumed
		}
	}
	out.Text = strings.Join(lines, "\n")
	if len(lines) > 0 {
		out.FirstLine = fromLine
		out.LastLine = out.NextLine - 1
	}
	// Loop exits either set HasMore (budget/maxLines cut with the cut line as
	// 1-line lookahead) or hit EOF/end (nothing remains), so no probe is
	// needed: a page boundary exactly at EOF reports HasMore=false.
	return out, nil
}

func readSnippetLine(r *bufio.Reader, filePath string) (string, bool, error) {
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, fmt.Errorf("read snippet %q: %w", filePath, err)
	}
	return line, errors.Is(err, io.EOF), nil
}
