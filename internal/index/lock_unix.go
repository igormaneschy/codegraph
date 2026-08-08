//go:build !windows

package index

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(file *os.File, mode lockMode) error {
	flags := unix.LOCK_NB
	if mode == lockExclusive {
		flags |= unix.LOCK_EX
	} else {
		flags |= unix.LOCK_SH
	}
	if err := unix.Flock(int(file.Fd()), flags); err != nil {
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			return ErrIndexLocked
		}
		return err
	}
	return nil
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func secureIndexLockFile(path string) error {
	// #nosec G703 -- path is the canonicalized database path (normalizeIndexPath,
	// abs + symlink-resolved) plus the fixed lockSuffix; chmod secures the exact
	// lock file the caller requested.
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
