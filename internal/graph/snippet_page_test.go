package graph

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingSnippetReader struct{ err error }

func (r failingSnippetReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadSnippetLine_PropagatesIOError(t *testing.T) {
	readErr := errors.New("read failed")
	_, _, err := readSnippetLine(bufio.NewReader(failingSnippetReader{err: readErr}), "a.go")
	if !errors.Is(err, readErr) {
		t.Fatalf("read error=%v, want %v", err, readErr)
	}
}

func writePagedRepo(t *testing.T, name, body string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo
}

// TestSnippetPaged_LosslessAcrossPages pins that walking cursors (via NextLine)
// reassembles the file byte-for-byte under adversarial budgets.
func TestSnippetPaged_LosslessAcrossPages(t *testing.T) {
	body := "one\ntwo\nthree\nfour\nfive\n"
	repo := writePagedRepo(t, "a.go", body)
	var sb strings.Builder
	next, digest := 1, ""
	pages := 0
	for {
		c, err := SnippetPaged(repo, "a.go", next, 2, 6, 0, digest)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if sb.Len() > 0 && c.Text != "" {
			sb.WriteByte('\n')
		}
		sb.WriteString(c.Text)
		digest = c.FileDigest
		if !c.HasMore {
			break
		}
		next = c.NextLine
		if pages > 10 {
			t.Fatal("paging did not terminate")
		}
	}
	if got := sb.String(); got != strings.TrimSuffix(body, "\n") {
		t.Fatalf("lossless reassembly failed: got %q", got)
	}
}

// TestSnippetPaged_LongLineEmittedWhole pins that a single line longer than the
// byte budget is emitted whole and marked — never split mid-line — and the
// following page resumes after it.
func TestSnippetPaged_LongLineEmittedWhole(t *testing.T) {
	long := strings.Repeat("é", 500) // multibyte: runes must never split either
	repo := writePagedRepo(t, "a.go", "short\n"+long+"\nafter\n")
	c, err := SnippetPaged(repo, "a.go", 1, 200, 32, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	// "short" (5) + newline + 1000-byte line exceeds 32: page holds line 1 only.
	if c.Text != "short" || c.HasMore != true || c.NextLine != 2 {
		t.Fatalf("first page = %+v, want line 1 with continuation", c)
	}
	c2, err := SnippetPaged(repo, "a.go", c.NextLine, 200, 32, 0, c.FileDigest)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Text != long || !c2.LongLine {
		t.Fatalf("oversize line must come back whole and marked: len=%d long=%v", len(c2.Text), c2.LongLine)
	}
	if c2.NextLine != 3 {
		t.Fatalf("page after a long line must resume at line 3, got %d", c2.NextLine)
	}
	c3, err := SnippetPaged(repo, "a.go", c2.NextLine, 200, 32, 0, c2.FileDigest)
	if err != nil {
		t.Fatal(err)
	}
	if c3.Text != "after" || c3.HasMore {
		t.Fatalf("tail page = %+v, want final line without continuation", c3)
	}
}

// TestSnippetPaged_DetectsChangeBetweenPages pins fail-closed continuation: a
// file modified between pages errors instead of silently shifting lines.
func TestSnippetPaged_DetectsChangeBetweenPages(t *testing.T) {
	repo := writePagedRepo(t, "a.go", "one\ntwo\nthree\n")
	c, err := SnippetPaged(repo, "a.go", 1, 1, 32*1024, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasMore {
		t.Fatal("single-line page must continue")
	}
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("one\nTWO\nEXTRA\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SnippetPaged(repo, "a.go", c.NextLine, 1, 32*1024, 0, c.FileDigest); err == nil ||
		!strings.Contains(err.Error(), "changed between pages") {
		t.Fatalf("modified file must fail the next page, err=%v", err)
	}
}

// TestSnippetPaged_EndBoundCompletion pins that a page cut exactly at the end
// bound reports no continuation (no phantom extra round-trip), and that a past-
// EOF start returns an empty final page rather than an error.
func TestSnippetPaged_EndBoundCompletion(t *testing.T) {
	repo := writePagedRepo(t, "a.go", "one\ntwo\nthree\n")
	c, err := SnippetPaged(repo, "a.go", 1, 2, 32*1024, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != "one\ntwo" || c.HasMore {
		t.Fatalf("page ending at the bound must complete: %+v", c)
	}
	c2, err := SnippetPaged(repo, "a.go", 99, 10, 32*1024, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if c2.Text != "" || c2.HasMore {
		t.Fatalf("past-EOF start must be an empty final page: %+v", c2)
	}
}

func BenchmarkSnippetPaged_FirstPage(b *testing.B) { benchmarkSnippetPaged(b, 1) }

func BenchmarkSnippetPaged_DeepPage(b *testing.B) { benchmarkSnippetPaged(b, 4001) }

func benchmarkSnippetPaged(b *testing.B, fromLine int) {
	repo := b.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "large.go"), []byte(strings.Repeat("line content\n", 5000)), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := SnippetPaged(repo, "large.go", fromLine, 200, 32*1024, 0, ""); err != nil {
			b.Fatal(err)
		}
	}
}

// TestSnippetPaged_DetectsSameSizeEdit pins the content contract behind R10: a
// same-size substitution or a line-break shift before the resume line must fail
// continuation. Size is not proof of content.
func TestSnippetPaged_DetectsSameSizeEdit(t *testing.T) {
	const original = "one\ntwo\nthree\n"
	for _, tc := range []struct{ name, replaced string }{
		{"same-size substitution", "one\nTWO\nthree\n"},
		{"line-break shift", "one\ntw\nothree\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := writePagedRepo(t, "a.go", original)
			c, err := SnippetPaged(repo, "a.go", 1, 1, 32*1024, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.replaced) != len(original) {
				t.Fatalf("fixture must keep the byte length: %d vs %d", len(tc.replaced), len(original))
			}
			if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte(tc.replaced), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := SnippetPaged(repo, "a.go", c.NextLine, 1, 32*1024, 0, c.FileDigest); err == nil ||
				!strings.Contains(err.Error(), "changed between pages") {
				t.Fatalf("same-size edit must fail continuation, err=%v", err)
			}
		})
	}
}

// TestSnippetPaged_DetectsRenameReplacement pins that replacing the path (new
// inode, same size, different bytes) is detected too.
func TestSnippetPaged_DetectsRenameReplacement(t *testing.T) {
	repo := writePagedRepo(t, "a.go", "one\ntwo\nthree\n")
	c, err := SnippetPaged(repo, "a.go", 1, 1, 32*1024, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(repo, ".replacement")
	if err := os.WriteFile(replacement, []byte("one\nTWO\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filepath.Join(repo, "a.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := SnippetPaged(repo, "a.go", c.NextLine, 1, 32*1024, 0, c.FileDigest); err == nil ||
		!strings.Contains(err.Error(), "changed between pages") {
		t.Fatalf("renamed replacement must fail continuation, err=%v", err)
	}
}

// TestSnippetPaged_RejectsOversizeLine pins the absolute line ceiling (R16): a
// line beyond MaxSnippetLineBytes is an actionable error, never an unbounded
// allocation — including when the line is only skipped or held as lookahead.
func TestSnippetPaged_RejectsOversizeLine(t *testing.T) {
	huge := strings.Repeat("a", MaxSnippetLineBytes+1)
	for _, tc := range []struct {
		name  string
		body  string
		start int
	}{
		{"served", "ok\n" + huge + "\n", 1},
		{"skipped", "ok\n" + huge + "\nafter\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := writePagedRepo(t, "a.go", tc.body)
			_, err := SnippetPaged(repo, "a.go", tc.start, 1, 32*1024, 0, "")
			if err == nil || !strings.Contains(err.Error(), "snippet line limit") {
				t.Fatalf("oversize line error=%v, want the line-limit guidance", err)
			}
		})
	}
}
