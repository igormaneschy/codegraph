//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package securefile

import "os"

func openReadDirectory(path string) (*os.File, error) {
	fd, err := openUnixDirectory(path, path)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
