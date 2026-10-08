package memory

import "testing"

func TestNodeHeapMB_Default(t *testing.T) {
	if NodeHeapMB() < 256 {
		t.Fatalf("NodeHeapMB too small: %d", NodeHeapMB())
	}
}

func TestSystemRAMBytes_Platform(t *testing.T) {
	// On Windows/Linux with working probes, RAM should be non-zero.
	ram := systemRAMBytes()
	if ram > 0 && ram < 256*1024*1024 {
		t.Fatalf("suspicious RAM reading: %d", ram)
	}
}

// TestOperatorPinnedMemoryLimit pins P7: an explicit GOMEMLIMIT is an operator
// decision that auto-tuning must not overwrite; empty/"off" leaves it unpinned.
func TestOperatorPinnedMemoryLimit(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"off", false},
		{"OFF", false},
		{"   ", false},
		{"512MiB", true},
		{"1073741824", true},
	} {
		t.Setenv("GOMEMLIMIT", tc.value)
		if got := operatorPinnedMemoryLimit(); got != tc.want {
			t.Errorf("GOMEMLIMIT=%q: pinned=%v, want %v", tc.value, got, tc.want)
		}
	}
}
