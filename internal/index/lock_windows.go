//go:build windows

package index

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lockFile(file *os.File, mode lockMode) error {
	var overlapped windows.Overlapped
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if mode == lockExclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		flags,
		0, 1, 0, &overlapped,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return ErrIndexLocked
		}
		return err
	}
	return nil
}

func unlockFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}

// Windows permissions are inherited from the cache directory; os.Chmod does
// not provide Unix-style private ACLs there and is intentionally not used.
func secureIndexLockFile(string) error { return nil }
