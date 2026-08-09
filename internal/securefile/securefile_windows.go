//go:build windows

package securefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsFileShareAll = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

type fileAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

// OpenRead opens a regular file with reparse-point opening enabled, rejects
// every pre-existing reparse component, and reads only from the accepted
// handle. The final-handle physical path check closes the race between the
// component checks and the final open.
func OpenRead(path string) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, windowsPathError(path, err)
	}
	abs = filepath.Clean(abs)
	expected, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, windowsPathError(path, err)
	}
	expected = filepath.Clean(expected)
	root, components, err := windowsPathComponents(abs)
	if err != nil {
		return nil, windowsPathError(path, err)
	}
	current := root
	for i := 0; i+1 < len(components); i++ {
		current = filepath.Join(current, components[i])
		h, openErr := openReparsePoint(current, windows.FILE_READ_ATTRIBUTES)
		if openErr != nil {
			return nil, windowsPathError(path, openErr)
		}
		attrs, attrErr := readFileAttributes(h)
		closeErr := windows.CloseHandle(h)
		if attrErr != nil {
			return nil, windowsPathError(path, errors.Join(attrErr, closeErr))
		}
		if closeErr != nil {
			return nil, windowsPathError(path, closeErr)
		}
		if attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || attrs.ReparseTag != 0 {
			return nil, windowsUnsafePathError(path)
		}
		if attrs.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
		}
	}

	name, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return nil, windowsPathError(path, err)
	}
	h, err := windows.CreateFile(
		name,
		windows.GENERIC_READ,
		windowsFileShareAll,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_SEQUENTIAL_SCAN,
		0,
	)
	if err != nil {
		return nil, windowsPathError(path, err)
	}
	attrs, err := readFileAttributes(h)
	if err != nil {
		return nil, errors.Join(windowsPathError(path, err), windows.CloseHandle(h))
	}
	if attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || attrs.ReparseTag != 0 {
		return nil, errors.Join(windowsUnsafePathError(path), windows.CloseHandle(h))
	}
	if attrs.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return nil, errors.Join(&os.PathError{Op: "open", Path: path, Err: ErrNotRegular}, windows.CloseHandle(h))
	}
	actual, err := finalPath(h)
	if err != nil {
		return nil, errors.Join(windowsPathError(path, err), windows.CloseHandle(h))
	}
	if !sameWindowsPath(actual, expected) {
		return nil, errors.Join(windowsUnsafePathError(path), windows.CloseHandle(h))
	}
	f := os.NewFile(uintptr(h), abs)
	if f == nil {
		_ = windows.CloseHandle(h)
		return nil, windowsPathError(path, errors.New("create file descriptor"))
	}
	info, statErr := f.Stat()
	if statErr != nil {
		return nil, errors.Join(statErr, f.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, &os.PathError{Op: "open", Path: path, Err: errors.Join(ErrNotRegular, f.Close())}
	}
	return f, nil
}

// MkdirAllPrivate and WritePrivate fail closed on Windows. os.Chmod(0600) does
// not establish an owner-only ACL, and the portable Go API has no
// handle-relative create/replace primitive that closes the parent reparse-point
// and temporary-entry races. Callers must surface ErrUnsupported rather than
// claiming a private artifact was installed.
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

func openReparsePoint(path string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(
		name,
		access,
		windowsFileShareAll,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
}

func readFileAttributes(handle windows.Handle) (fileAttributeTagInfo, error) {
	var info fileAttributeTagInfo
	err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	return info, err
}

func finalPath(handle windows.Handle) (string, error) {
	for size := uint32(256); ; size *= 2 {
		buf := make([]uint16, size)
		n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], size, 0)
		if err != nil {
			return "", err
		}
		if n < size-1 {
			return windows.UTF16ToString(buf[:n]), nil
		}
		if size > 1<<15 {
			return "", fmt.Errorf("final path exceeds supported length")
		}
	}
}

func windowsPathComponents(abs string) (string, []string, error) {
	volume := filepath.VolumeName(abs)
	if volume == "" {
		return "", nil, errors.New("absolute path has no Windows volume")
	}
	root := volume + string(filepath.Separator)
	rest := strings.TrimPrefix(abs, root)
	if rest == "" {
		return root, nil, nil
	}
	components := strings.Split(rest, string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return "", nil, errors.New("path contains an invalid component")
		}
	}
	return root, components, nil
}

func normalizeWindowsPath(path string) string {
	if strings.HasPrefix(path, `\\?\UNC\`) {
		path = `\\` + strings.TrimPrefix(path, `\\?\UNC\`)
	} else {
		path = strings.TrimPrefix(path, `\\?\`)
	}
	return filepath.Clean(path)
}

func sameWindowsPath(a, b string) bool {
	return strings.EqualFold(normalizeWindowsPath(a), normalizeWindowsPath(b))
}

func windowsPathError(path string, err error) error {
	return &os.PathError{Op: "open", Path: path, Err: err}
}

func windowsUnsafePathError(path string) error {
	return &os.PathError{Op: "open", Path: path, Err: ErrUnsafePath}
}
