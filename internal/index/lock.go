package index

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const lockSuffix = ".lock"

const maxReaderRecoveryAttempts = 3

type lockMode uint8

const (
	lockShared lockMode = iota
	lockExclusive
)

// Lock is the per-database OS lock used to coordinate graph readers and the
// atomic index writer. The lock is deliberately separate from graph.Open so
// low-level graph tests and callers that already own a lock do not re-enter it.
type Lock struct {
	file *os.File
}

// AcquireSharedLock obtains the non-blocking shared/read lock for dbPath.
// Readers hold this lock for the full lifetime of their readable Store.
func AcquireSharedLock(dbPath string) (*Lock, error) {
	return acquireIndexLockMode(dbPath, lockShared)
}

// AcquireExclusiveLock obtains the non-blocking exclusive/write lock for
// dbPath. RunAtomic holds it through checkpoint, replacement, and cleanup.
func AcquireExclusiveLock(dbPath string) (*Lock, error) {
	return acquireIndexLockMode(dbPath, lockExclusive)
}

// AcquireReaderLock obtains a shared lock and closes the small recovery race
// around interrupted replacement. The shared lock is acquired before checking
// for a backup, so a writer cannot create a new backup between the check and a
// later graph.Open. If a stale backup is present, the shared lock is released,
// recovery runs exclusively, and the reader lock is reacquired.
func AcquireReaderLock(dbPath string) (*Lock, error) {
	normalized, err := normalizeIndexPath(dbPath)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < maxReaderRecoveryAttempts; attempt++ {
		reader, err := acquireIndexLockModeNormalized(normalized, lockShared)
		if err != nil {
			return nil, err
		}
		needed, err := indexRecoveryNeeded(normalized)
		if err != nil {
			_ = reader.Release()
			return nil, err
		}
		if !needed {
			return reader, nil
		}
		if err := reader.Release(); err != nil {
			return nil, fmt.Errorf("release reader for replacement recovery: %w", err)
		}
		// RecoverIndex acquires the exclusive lock itself; never call it while
		// retaining this shared lock or recovery would deadlock.
		if err := RecoverIndex(normalized); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("replacement recovery remains pending for %q", normalized)
}

func acquireIndexLock(dbPath string) (*Lock, error) {
	return acquireIndexLockMode(dbPath, lockExclusive)
}

func acquireIndexLockMode(dbPath string, mode lockMode) (*Lock, error) {
	normalized, err := normalizeIndexPath(dbPath)
	if err != nil {
		return nil, err
	}
	return acquireIndexLockModeNormalized(normalized, mode)
}

func acquireIndexLockModeNormalized(dbPath string, mode lockMode) (*Lock, error) {
	lockPath := filepath.Clean(dbPath) + lockSuffix
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open index lock %q: %w", lockPath, err)
	}
	if err := secureIndexLockFile(lockPath); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("secure index lock %q: %w", lockPath, err)
	}
	if err := lockFile(f, mode); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrIndexLocked) {
			return nil, fmt.Errorf("%w: %s", ErrIndexLocked, dbPath)
		}
		return nil, fmt.Errorf("lock index %q: %w", dbPath, err)
	}
	return &Lock{file: f}, nil
}

func normalizeIndexPath(dbPath string) (string, error) {
	normalized, err := CanonicalPath(dbPath)
	if err != nil {
		return "", fmt.Errorf("normalize database path %q: %w", dbPath, err)
	}
	return filepath.Clean(normalized), nil
}

// Release unlocks and closes the lock file. It is idempotent.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil && closeErr != nil {
		return fmt.Errorf("unlock: %v; close: %w", unlockErr, closeErr)
	}
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
