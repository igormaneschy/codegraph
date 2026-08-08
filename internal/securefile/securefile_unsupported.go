//go:build !windows && !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly

package securefile

import "os"

func OpenRead(path string) (*os.File, error) {
	return nil, &os.PathError{Op: "open", Path: path, Err: ErrUnsupported}
}

func MkdirAllPrivate(path string) error {
	return &os.PathError{Op: "mkdir", Path: path, Err: ErrUnsupported}
}

func WritePrivate(path string, _ []byte) error {
	return &os.PathError{Op: "write", Path: path, Err: ErrUnsupported}
}

func MkdirTempPrivate(parent, _ string) (*PrivateDirectory, error) {
	return nil, &os.PathError{Op: "mkdir", Path: parent, Err: ErrUnsupported}
}

func SymlinkPrivate(_, path string) error {
	return &os.PathError{Op: "symlink", Path: path, Err: ErrUnsupported}
}

func (d *PrivateDirectory) Verify() error {
	if d == nil {
		return nil
	}
	return &os.PathError{Op: "verify", Path: d.Path(), Err: ErrUnsupported}
}

func (d *PrivateDirectory) Cleanup() error {
	if d == nil {
		return nil
	}
	return &os.PathError{Op: "remove", Path: d.Path(), Err: ErrUnsupported}
}
