package memory

import "testing"

// BenchmarkGateWithGarbage measures Gate on a heap that actually holds garbage, so
// the cost of forcing a collection is visible. P1 removed a redundant runtime.GC
// (debug.FreeOSMemory already forces one).
func BenchmarkGateWithGarbage(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = make([]byte, 8<<20)
		Gate()
	}
}

var sink []byte
