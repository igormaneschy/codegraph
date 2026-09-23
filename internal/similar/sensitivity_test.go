package similar

import (
	"strconv"
	"testing"
)

// TestThresholdSensitivity pins why the 0.7 threshold behaves the way it
// does: trigram shingles amplify small edits (each changed token kills up to
// 3 shingles), so bodies differing in a few constants score far below
// threshold while a rename-only clone scores near 1. The clone-bomb fixtures
// must therefore differ (almost) only by symbol name.
func TestThresholdSensitivity(t *testing.T) {
	renameOnly := func(name string) []string {
		return Tokenize("func " + name + "(v int) int {\n  v += 1\n  v += 2\n  v += 3\n  v += 4\n  return v\n}\n")
	}
	constsDiffer := func(k int) []string {
		body := "func clone(v int) int {\n"
		for i := 0; i < 4; i++ {
			body += "  v += " + strconv.Itoa(k+i) + "\n"
		}
		return Tokenize(body + "  return v\n}\n")
	}
	sig := func(toks []string) []uint64 { return Signature(toks, 3, 128) }

	renameScore := EstJaccard(sig(renameOnly("clone0")), sig(renameOnly("clone1")))
	if renameScore < 0.7 {
		t.Errorf("rename-only clone scores %f, want >= 0.7 (threshold)", renameScore)
	}
	constScore := EstJaccard(sig(constsDiffer(0)), sig(constsDiffer(4)))
	if constScore >= 0.7 {
		t.Errorf("constant-differing bodies score %f, want < 0.7", constScore)
	}
	t.Logf("rename-only=%.3f constants-differ=%.3f", renameScore, constScore)
}
