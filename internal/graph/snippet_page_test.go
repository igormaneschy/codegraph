package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	next, size := 1, int64(-1)
	pages := 0
	for {
		c, err := SnippetPaged(repo, "a.go", next, 2, 6, 0, size)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if sb.Len() > 0 && c.Text != "" {
			sb.WriteByte('\n')
		}
		sb.WriteString(c.Text)
		size = c.FileSize
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
	c, err := SnippetPaged(repo, "a.go", 1, 200, 32, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	// "short" (5) + newline + 1000-byte line exceeds 32: page holds line 1 only.
	if c.Text != "short" || c.HasMore != true || c.NextLine != 2 {
		t.Fatalf("first page = %+v, want line 1 with continuation", c)
	}
	c2, err := SnippetPaged(repo, "a.go", c.NextLine, 200, 32, 0, c.FileSize)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Text != long || !c2.LongLine {
		t.Fatalf("oversize line must come back whole and marked: len=%d long=%v", len(c2.Text), c2.LongLine)
	}
	if c2.NextLine != 3 {
		t.Fatalf("page after a long line must resume at line 3, got %d", c2.NextLine)
	}
	c3, err := SnippetPaged(repo, "a.go", c2.NextLine, 200, 32, 0, c2.FileSize)
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
	c, err := SnippetPaged(repo, "a.go", 1, 1, 32*1024, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasMore {
		t.Fatal("single-line page must continue")
	}
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("one\nTWO\nEXTRA\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SnippetPaged(repo, "a.go", c.NextLine, 1, 32*1024, 0, c.FileSize); err == nil ||
		!strings.Contains(err.Error(), "changed between pages") {
		t.Fatalf("modified file must fail the next page, err=%v", err)
	}
}

// TestSnippetPaged_EndBoundCompletion pins that a page cut exactly at the end
// bound reports no continuation (no phantom extra round-trip), and that a past-
// EOF start returns an empty final page rather than an error.
func TestSnippetPaged_EndBoundCompletion(t *testing.T) {
	repo := writePagedRepo(t, "a.go", "one\ntwo\nthree\n")
	c, err := SnippetPaged(repo, "a.go", 1, 2, 32*1024, 2, -1)
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != "one\ntwo" || c.HasMore {
		t.Fatalf("page ending at the bound must complete: %+v", c)
	}
	c2, err := SnippetPaged(repo, "a.go", 99, 10, 32*1024, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Text != "" || c2.HasMore {
		t.Fatalf("past-EOF start must be an empty final page: %+v", c2)
	}
}
