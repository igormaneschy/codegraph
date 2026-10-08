//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly

package securefile

import "os"

func openReadDirectory(path string) (*os.File, error) {
	return nil, &os.PathError{Op: "readdir", Path: path, Err: ErrUnsupported}
}
