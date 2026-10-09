package index

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

func TestGoStressCallsRealAtomicSelfAndCycleBoundaries(t *testing.T) {
	for _, files := range []int{1, 2} {
		t.Run(fmt.Sprintf("files-%d", files), func(t *testing.T) {
			root, _ := writeSyntheticGoModule(t, files)
			database := filepath.Join(t.TempDir(), "graph.db")
			result, err := RunAtomic(database, root)
			if err != nil {
				t.Fatal(err)
			}
			store, err := graph.Open(database)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			count, err := validateGoStressCalls(result, store, files)
			if err != nil || count != 2*files {
				t.Fatalf("real Go boundary CALLS=%d expected=%d err=%v", count, 2*files, err)
			}
		})
	}
}
