package scip

import (
	"bytes"
	"strings"
	"testing"
)

// TestTailBuffer_KeepsOnlyTheTail pins P7: the captured resolver output is bounded,
// so a chatty child cannot hold megabytes that are only used for a short error tail.
func TestTailBuffer_KeepsOnlyTheTail(t *testing.T) {
	const limit = 64
	buf := newTailBuffer(limit)
	// Write more than the limit in several pieces, including one oversized write.
	for i := 0; i < 100; i++ {
		if _, err := buf.Write([]byte("0123456789")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := buf.Write(bytes.Repeat([]byte("x"), limit*3)); err != nil {
		t.Fatal(err)
	}
	if len(buf.Bytes()) > limit {
		t.Fatalf("buffer grew to %d bytes, want <= %d", len(buf.Bytes()), limit)
	}
	if got := buf.Bytes(); !bytes.Equal(got, bytes.Repeat([]byte("x"), limit)) {
		t.Fatalf("tail = %q, want the last oversized write", truncateForError(got))
	}
	if !strings.Contains(tail(buf.Bytes(), 500), "x") {
		t.Fatal("the tail must remain readable by tail()")
	}
}

func truncateForError(b []byte) string {
	if len(b) > 32 {
		return string(b[:32]) + "..."
	}
	return string(b)
}

// TestTailBuffer_KeepsLastPartialWrites pins the sliding window across partial
// writes so the tail is the most recent bytes, not the first.
func TestTailBuffer_KeepsLastPartialWrites(t *testing.T) {
	buf := newTailBuffer(8)
	for _, s := range []string{"aaaa", "bbbb", "cccc"} {
		if _, err := buf.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if got := string(buf.Bytes()); got != "bbbbcccc" {
		t.Fatalf("tail = %q, want bbbbcccc", got)
	}
}
