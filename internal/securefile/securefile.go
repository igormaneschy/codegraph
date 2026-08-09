package securefile

import (
	"errors"
	"io"
	"sync"
)

// ErrNotRegular identifies a path that is not a regular file. Repository
// inputs are content files, not directories, devices, FIFOs, or sockets.
var ErrNotRegular = errors.New("not a regular file")

// ErrUnsafePath identifies a symlink/reparse component or a path whose opened
// descriptor does not match the path that was validated.
var ErrUnsafePath = errors.New("unsafe file path")

// ErrUnsupported is returned on targets for which this package has no
// descriptor-stable, no-follow implementation. Callers must not fall back to
// an ordinary path-based read or write when they receive it.
var ErrUnsupported = errors.New("secure file operations are unsupported on this platform")

// PrivateDirectory is a descriptor/identity-anchored directory created by
// MkdirTempPrivate. Its Path is suitable for APIs that only accept a path (for
// example go/packages or an external SCIP process), while Verify and Cleanup
// keep the created directory entry tied to the descriptor retained by this
// value. Callers must not replace this with os.MkdirTemp plus os.RemoveAll.
type PrivateDirectory struct {
	path  string
	state any
	mu    sync.Mutex
}

// Path returns the exact lexical path created for the private directory. It is
// intentionally not canonicalized after creation: resolving a mutable path
// after the fact would reintroduce the resolver handoff TOCTOU.
func (d *PrivateDirectory) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

// beforePrivateRenameHook is a package-local test seam used to substitute the
// temporary directory entry between its first identity check and the final
// replacement check. Production leaves it nil.
var beforePrivateRenameHook func(string)

// ReadFile opens path through the platform safe-open primitive and reads from
// the returned descriptor. The path is never reopened after validation.
func ReadFile(path string) ([]byte, error) {
	f, err := OpenRead(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(f)
	closeErr := f.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	return data, nil
}
