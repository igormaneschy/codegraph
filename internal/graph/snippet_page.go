package graph

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Lordymine/codegraph/internal/securefile"
)

// snippetBeforeOpenHook is a test seam for replacing a resolved path before open.
var snippetBeforeOpenHook func()

// MaxSnippetLineBytes is the absolute ceiling for one source line in a snippet
// page. The page byte budget is normally far smaller, and a line between the two
// is still emitted whole (LongLine); a line beyond this ceiling is an actionable
// error instead of an unbounded allocation — including when the line is only
// skipped before the range or held as the one-line lookahead.
const MaxSnippetLineBytes = 1 << 20

// SnippetChunk is one streamed page of a source file. Pages always cover whole
// lines: a single line longer than maxBytes is emitted whole with LongLine set
// (never split mid-line), so continuation is lossless by construction.
type SnippetChunk struct {
	Text       string // page lines joined by "\n", no trailing newline
	FirstLine  int    // 1-based first line covered (0 when Text is empty)
	LastLine   int    // 1-based last line covered (0 when Text is empty)
	NextLine   int    // line the next page resumes at
	HasMore    bool   // more lines remain (within the end bound, if any)
	LongLine   bool   // the page is one line longer than the byte budget
	FileSize   int64  // observed file size, for diagnostics
	FileDigest string // sha256 of the whole file as read, the continuation's proof
}

// SnippetPaged streams one page of filePath from fromLine (1-based), stopping
// at endLine (0 = EOF), after maxLines lines, or before the first line that
// would push the payload past maxBytes — whichever comes first. Memory stays
// proportional to the page, not the file: lines are read incrementally and at
// most one line is held as lookahead.
//
// Continuation is bound to content, not to size: the page returns the sha256 of
// the whole file it read, and a continuing page must pass that digest back so it
// can prove it is serving the same bytes. A same-size edit, a line-break shift,
// or a rename-replacement fails with an actionable error instead of silently
// shifting lines; size/inode/mtime alone are not proof of content. The digest is
// computed in the single read pass (a tee into the hasher) and the file is
// drained to EOF, so the hash covers the whole file.
//
// The byte budget counts payload bytes (line content plus the joined
// newlines). A page holds at least one line: an oversize first line is
// returned whole with LongLine set and the next page resumes after it. A line
// beyond MaxSnippetLineBytes is an actionable error, never an unbounded
// allocation — including when it is only skipped or held as lookahead.
func SnippetPaged(repoRoot, filePath string, fromLine, maxLines, maxBytes, endLine int, expectDigest string) (SnippetChunk, error) {
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

	hasher := sha256.New()
	r := bufio.NewReaderSize(io.TeeReader(f, hasher), 64*1024)
	// Fast-forward to the resume line without materializing the skipped lines: a
	// deep page used to allocate one string per skipped line just to count them
	// (P2). The bytes still pass through the hasher, so the whole-file digest is
	// unchanged. io.EOF means the file has fewer lines than fromLine-1.
	lineNo := fromLine - 1
	var lines []string
	acc := 0
	out.NextLine = fromLine
	skippedPastEOF := false
	if fromLine > 1 {
		if err := skipSnippetLines(r, fromLine-1, filePath); err != nil {
			if !errors.Is(err, io.EOF) {
				return out, err
			}
			skippedPastEOF = true
		}
	}
	for !skippedPastEOF {
		raw, atEnd, readErr := readSnippetLine(r, filePath)
		if readErr != nil {
			return out, readErr
		}
		if len(raw) == 0 && atEnd {
			break // clean EOF: nothing remains
		}
		lineNo++
		line := strings.TrimSuffix(raw, "\n")
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
	// Drain to EOF so the digest covers the whole file, not just the page.
	if _, err := io.Copy(io.Discard, r); err != nil {
		return out, fmt.Errorf("read snippet %q: %w", filePath, err)
	}
	out.FileDigest = hex.EncodeToString(hasher.Sum(nil))
	if expectDigest != "" && out.FileDigest != expectDigest {
		return out, fmt.Errorf("file %q changed between pages (digest %s, was %s): restart the snippet from its first page", filePath, shortHash(out.FileDigest), shortHash(expectDigest))
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

func shortHash(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

// skipSnippetLines consumes n complete lines from r without building a string per
// line, so a deep page does not allocate for lines it only skips. It enforces the
// same absolute line ceiling as readSnippetLine (a skipped line must not be an
// unbounded read either) and returns io.EOF when the file ends first.
func skipSnippetLines(r *bufio.Reader, n int, filePath string) error {
	for i := 0; i < n; i++ {
		lineLen := 0
		for {
			frag, err := r.ReadSlice('\n')
			lineLen += len(frag)
			if lineLen > MaxSnippetLineBytes {
				return fmt.Errorf("line in %q exceeds the %d-byte snippet line limit: request a narrower line range or read the file directly", filePath, MaxSnippetLineBytes)
			}
			switch {
			case err == nil:
				// Complete line consumed.
			case errors.Is(err, bufio.ErrBufferFull):
				continue // line continues past the reader buffer
			case errors.Is(err, io.EOF):
				return io.EOF // consumed the final partial line, fewer than n lines
			default:
				return fmt.Errorf("read snippet %q: %w", filePath, err)
			}
			break
		}
	}
	return nil
}

func readSnippetLine(r *bufio.Reader, filePath string) (string, bool, error) {
	var line []byte
	for {
		frag, err := r.ReadSlice('\n')
		if len(line)+len(frag) > MaxSnippetLineBytes {
			return "", false, fmt.Errorf("line in %q exceeds the %d-byte snippet line limit: request a narrower line range or read the file directly", filePath, MaxSnippetLineBytes)
		}
		line = append(line, frag...)
		switch {
		case err == nil:
			return string(line), false, nil
		case errors.Is(err, io.EOF):
			return string(line), true, nil
		case errors.Is(err, bufio.ErrBufferFull):
			// Line continues past the reader buffer; keep accumulating up to the ceiling.
		default:
			return "", false, fmt.Errorf("read snippet %q: %w", filePath, err)
		}
	}
}
