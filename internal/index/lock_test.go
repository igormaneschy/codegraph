package index

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIndexLockCreatesMissingParent(t *testing.T) {
	for _, reader := range []bool{false, true} {
		name := "writer"
		if reader {
			name = "reader"
		}
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cache", "codegraph")
			dbPath := filepath.Join(dir, "graph.db")
			for attempt := 0; attempt < 2; attempt++ {
				acquire := AcquireExclusiveLock
				if reader {
					acquire = AcquireReaderLock
				}
				lock, err := acquire(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				contender, err := AcquireExclusiveLock(dbPath)
				if contender != nil {
					_ = contender.Release()
				}
				if !errors.Is(err, ErrIndexLocked) {
					_ = lock.Release()
					t.Fatalf("expected contention, got %v", err)
				}
				if err := lock.Release(); err != nil {
					t.Fatal(err)
				}
				// Simulate cache cleanup between lock acquisitions.
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
