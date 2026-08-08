//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package securefile

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	privateDirectoryMode = 0o700
	privateFileMode      = 0o600
	privateCreateTries   = 128
)

type privateDirectoryUnix struct {
	parentFD int
	dirFD    int
	name     string
}

// MkdirTempPrivate creates a randomly named owner-only directory with the
// directory entry and its parent retained by descriptor. The returned path is
// the path created by this invocation; it is never resolved through
// EvalSymlinks.
func MkdirTempPrivate(parent, pattern string) (*PrivateDirectory, error) {
	if parent == "" {
		parent = os.TempDir()
	}
	absParent, err := absoluteClean(parent)
	if err != nil {
		return nil, &os.PathError{Op: "mkdir", Path: parent, Err: err}
	}
	parentFD, err := openUnixDirectory(absParent, parent)
	if err != nil {
		return nil, err
	}
	if err := verifyPrivateParentFD(parentFD, absParent); err != nil {
		_ = unix.Close(parentFD)
		return nil, &os.PathError{Op: "mkdir", Path: parent, Err: err}
	}

	for attempt := 0; attempt < privateCreateTries; attempt++ {
		name, nameErr := privateDirectoryName(pattern)
		if nameErr != nil {
			_ = unix.Close(parentFD)
			return nil, &os.PathError{Op: "mkdir", Path: parent, Err: nameErr}
		}
		if mkdirErr := unix.Mkdirat(parentFD, name, privateDirectoryMode); mkdirErr != nil {
			if errors.Is(mkdirErr, unix.EEXIST) {
				continue
			}
			_ = unix.Close(parentFD)
			return nil, unixOpenError(parent, mkdirErr)
		}
		dirFD, openErr := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
			_ = unix.Close(parentFD)
			return nil, unixOpenError(parent, openErr)
		}
		private := &PrivateDirectory{
			path:  filepath.Join(absParent, name),
			state: &privateDirectoryUnix{parentFD: parentFD, dirFD: dirFD, name: name},
		}
		if verifyErr := private.verifyLocked(); verifyErr != nil {
			_ = unix.Close(dirFD)
			_ = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
			_ = unix.Close(parentFD)
			return nil, &os.PathError{Op: "mkdir", Path: private.path, Err: verifyErr}
		}
		return private, nil
	}
	_ = unix.Close(parentFD)
	return nil, &os.PathError{Op: "mkdir", Path: parent, Err: errors.New("could not create a unique private directory")}
}

// SymlinkPrivate creates a link through the already-validated parent
// descriptor. Snapshot dependency links are only written through this
// primitive; a plain os.Symlink would reopen a mutable parent path.
func SymlinkPrivate(target, path string) error {
	abs, err := absoluteClean(path)
	if err != nil {
		return &os.PathError{Op: "symlink", Path: path, Err: err}
	}
	parentFD, err := openUnixDirectory(filepath.Dir(abs), path)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(parentFD) }()
	if err := verifyPrivateDirectoryFD(parentFD, filepath.Dir(abs)); err != nil {
		return &os.PathError{Op: "symlink", Path: path, Err: err}
	}
	if err := unix.Symlinkat(target, parentFD, filepath.Base(abs)); err != nil {
		return unixOpenError(path, err)
	}
	return nil
}

// Verify proves that the path still names the directory created by this
// invocation. A replacement is reported rather than trusted.
func (d *PrivateDirectory) Verify() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.verifyLocked()
}

func (d *PrivateDirectory) verifyLocked() error {
	state, ok := d.state.(*privateDirectoryUnix)
	if !ok || state == nil {
		return fmt.Errorf("%w: private directory is closed", ErrUnsafePath)
	}
	return verifyPrivateDirectoryIdentity(state.parentFD, state.name, state.dirFD, d.path)
}

// Cleanup removes the snapshot through retained directory descriptors. If its
// directory entry was replaced, the original is left in place (or quarantined
// under a name chosen by the replacer) and the replacement is never removed.
func (d *PrivateDirectory) Cleanup() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	state, ok := d.state.(*privateDirectoryUnix)
	if !ok || state == nil {
		return nil
	}
	var errs []error
	if err := d.verifyLocked(); err != nil {
		errs = append(errs, err)
	} else if err := removePrivateDirectoryContents(state.dirFD); err != nil {
		errs = append(errs, err)
	} else if err := d.verifyLocked(); err != nil {
		errs = append(errs, err)
	} else if err := unix.Unlinkat(state.parentFD, state.name, unix.AT_REMOVEDIR); err != nil {
		errs = append(errs, unixOpenError(d.path, err))
	}
	closeDirErr := unix.Close(state.dirFD)
	closeParentErr := unix.Close(state.parentFD)
	d.state = nil
	if closeDirErr != nil {
		errs = append(errs, closeDirErr)
	}
	if closeParentErr != nil {
		errs = append(errs, closeParentErr)
	}
	return errors.Join(errs...)
}

func privateDirectoryName(pattern string) (string, error) {
	if pattern == "" {
		pattern = ".codegraph-private-"
	}
	if filepath.Base(pattern) != pattern || strings.ContainsRune(pattern, filepath.Separator) {
		return "", errors.New("private directory pattern contains a path separator")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	suffix := fmt.Sprintf("%x", random[:])
	if i := strings.LastIndexByte(pattern, '*'); i >= 0 {
		return pattern[:i] + suffix + pattern[i+1:], nil
	}
	return pattern + suffix, nil
}

func verifyPrivateDirectoryIdentity(parentFD int, name string, dirFD int, path string) error {
	var held, entry unix.Stat_t
	if err := unix.Fstat(dirFD, &held); err != nil {
		return fmt.Errorf("%w: inspect private directory descriptor %q: %v", ErrUnsafePath, path, err)
	}
	if err := verifyPrivateDirectoryStat(&held, path); err != nil {
		return err
	}
	if err := unix.Fstatat(parentFD, name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("%w: inspect private directory entry %q: %v", ErrUnsafePath, path, err)
	}
	if err := verifyPrivateDirectoryStat(&entry, path); err != nil {
		return err
	}
	if held.Dev != entry.Dev || held.Ino != entry.Ino {
		return fmt.Errorf("%w: private directory entry %q was replaced", ErrUnsafePath, path)
	}
	return nil
}

func verifyPrivateDirectoryStat(info *unix.Stat_t, path string) error {
	if info.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: private resolver path %q is not a directory", ErrUnsafePath, path)
	}
	if info.Mode&0o777 != privateDirectoryMode || !isCurrentUID(info.Uid) {
		return fmt.Errorf("%w: private directory %q is not owner-only (mode=%o uid=%d owner=%d)", ErrUnsafePath, path, info.Mode&0o7777, info.Uid, unix.Getuid())
	}
	return nil
}

func verifyPrivateParentFD(fd int, path string) error {
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return fmt.Errorf("%w: inspect private parent %q: %v", ErrUnsafePath, path, err)
	}
	if info.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: private parent %q is not a directory", ErrUnsafePath, path)
	}
	perm := info.Mode & 0o7777
	if perm&0o22 != 0 && !trustedStickyDirectory(path, uint32(perm)) {
		return fmt.Errorf("%w: private parent %q is group/world-writable", ErrUnsafePath, path)
	}
	return nil
}

func verifyPrivateDirectoryFD(fd int, path string) error {
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return fmt.Errorf("%w: inspect destination directory %q: %v", ErrUnsafePath, path, err)
	}
	if err := verifyTrustedDestinationDirectoryStat(&info, path); err != nil {
		return err
	}
	return nil
}

func verifyTrustedDestinationDirectoryStat(info *unix.Stat_t, path string) error {
	if info.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: destination %q is not a directory", ErrUnsafePath, path)
	}
	// Existing destination directories may be the normal 0755 directories
	// created by a repository or test harness. They are trusted only when the
	// current owner controls them and no group/world writer can replace a
	// private artifact. Newly created directories are still exactly 0700.
	perm := info.Mode & 0o7777
	if perm&0o22 != 0 {
		if !trustedStickyDirectory(path, uint32(perm)) {
			return fmt.Errorf("%w: destination directory %q is group/world-writable (mode=%o)", ErrUnsafePath, path, perm)
		}
		return nil
	}
	if !isCurrentUID(info.Uid) {
		return fmt.Errorf("%w: destination directory %q is not owner-controlled (mode=%o uid=%d owner=%d)", ErrUnsafePath, path, info.Mode&0o7777, info.Uid, unix.Getuid())
	}
	return nil
}

// isCurrentUID widens both values before comparing them. Stat_t stores Uid as
// uint32, while Getuid returns int; int64 can represent every value of both
// types on the supported Unix targets without a lossy signed-to-unsigned cast.
func isCurrentUID(uid uint32) bool {
	return int64(uid) == int64(unix.Getuid())
}

func trustedStickyDirectory(path string, mode uint32) bool {
	if mode&0o1000 == 0 {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	return abs == filepath.Clean(os.TempDir()) || abs == "/tmp" || abs == "/var/tmp" || abs == "/private/tmp"
}

func removePrivateDirectoryContents(dirFD int) error {
	dupFD, err := unix.Dup(dirFD)
	if err != nil {
		return fmt.Errorf("%w: duplicate private directory descriptor: %v", ErrUnsafePath, err)
	}
	dir := os.NewFile(uintptr(dupFD), "private-directory")
	if dir == nil {
		_ = unix.Close(dupFD)
		return fmt.Errorf("%w: create private directory reader", ErrUnsafePath)
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	for _, entry := range entries {
		if err := removePrivateEntry(dirFD, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func removePrivateEntry(parentFD int, name string) error {
	var expected unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &expected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("%w: inspect private entry %q: %v", ErrUnsafePath, name, err)
	}
	if expected.Mode&unix.S_IFMT == unix.S_IFDIR {
		childFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("%w: open private child %q: %v", ErrUnsafePath, name, err)
		}
		var opened unix.Stat_t
		if statErr := unix.Fstat(childFD, &opened); statErr != nil {
			_ = unix.Close(childFD)
			return fmt.Errorf("%w: inspect private child %q: %v", ErrUnsafePath, name, statErr)
		}
		if opened.Dev != expected.Dev || opened.Ino != expected.Ino {
			_ = unix.Close(childFD)
			return fmt.Errorf("%w: private child %q was replaced", ErrUnsafePath, name)
		}
		removeErr := removePrivateDirectoryContents(childFD)
		closeErr := unix.Close(childFD)
		if removeErr != nil || closeErr != nil {
			return errors.Join(removeErr, closeErr)
		}
		var beforeRemove unix.Stat_t
		if statErr := unix.Fstatat(parentFD, name, &beforeRemove, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
			return fmt.Errorf("%w: inspect private child before removal %q: %v", ErrUnsafePath, name, statErr)
		}
		if beforeRemove.Dev != expected.Dev || beforeRemove.Ino != expected.Ino {
			return fmt.Errorf("%w: private child %q was replaced", ErrUnsafePath, name)
		}
		if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil {
			return fmt.Errorf("%w: remove private child %q: %v", ErrUnsafePath, name, err)
		}
		return nil
	}
	var beforeRemove unix.Stat_t
	if statErr := unix.Fstatat(parentFD, name, &beforeRemove, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
		return fmt.Errorf("%w: inspect private entry before removal %q: %v", ErrUnsafePath, name, statErr)
	}
	if beforeRemove.Dev != expected.Dev || beforeRemove.Ino != expected.Ino {
		return fmt.Errorf("%w: private entry %q was replaced", ErrUnsafePath, name)
	}
	if err := unix.Unlinkat(parentFD, name, 0); err != nil {
		return fmt.Errorf("%w: remove private entry %q: %v", ErrUnsafePath, name, err)
	}
	return nil
}

// OpenRead opens a regular file without following any path component that
// names a symlink. Directory descriptors keep the traversal anchored while the
// final descriptor makes the subsequent read immune to path replacement.
func OpenRead(path string) (*os.File, error) {
	abs, err := absoluteClean(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	components := strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator))
	if len(components) == 1 && components[0] == "" {
		components = nil
	}

	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, unixOpenError(path, err)
	}
	for i, component := range components {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i < len(components)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, openErr := unix.Openat(fd, component, flags, 0)
		closeErr := unix.Close(fd)
		if openErr != nil {
			if closeErr != nil {
				openErr = errors.Join(openErr, closeErr)
			}
			return nil, unixOpenError(path, openErr)
		}
		if closeErr != nil {
			_ = unix.Close(next)
			return nil, unixOpenError(path, closeErr)
		}
		fd = next
	}

	f := os.NewFile(uintptr(fd), abs)
	if f == nil {
		_ = unix.Close(fd)
		return nil, unixOpenError(path, errors.New("create file descriptor"))
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

// MkdirAllPrivate creates path without following an existing symlink in any
// component. The opened directory descriptors keep each parent anchored while
// a missing component is created, so a parent replacement cannot redirect a
// later component into another tree.
func MkdirAllPrivate(path string) error {
	abs, err := absoluteClean(path)
	if err != nil {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	components := unixPathComponents(abs)
	fd, err := openUnixRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	current := string(filepath.Separator)
	for _, component := range components {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil && errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(fd, component, privateDirectoryMode); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				return unixOpenError(path, mkdirErr)
			}
			next, openErr = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			return unixOpenError(path, openErr)
		}
		current = filepath.Join(current, component)
		if err := verifyPrivateTraversalDirectoryFD(next, current); err != nil {
			_ = unix.Close(next)
			return &os.PathError{Op: "mkdir", Path: path, Err: err}
		}
		if closeErr := unix.Close(fd); closeErr != nil {
			_ = unix.Close(next)
			return unixOpenError(path, closeErr)
		}
		fd = next
	}
	if err := verifyPrivateDirectoryFD(fd, abs); err != nil {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

// WritePrivate writes through a same-directory temporary file and renames the
// completed regular file over the destination. Unix rename replaces a
// destination symlink itself, never the file named by that symlink. The parent
// descriptor and renameat keep the destination directory stable; the temporary
// entry is checked against the still-open file descriptor immediately before
// the replacement, and a substitution fails closed.
func WritePrivate(path string, data []byte) error {
	abs, err := absoluteClean(path)
	if err != nil {
		return &os.PathError{Op: "write", Path: path, Err: err}
	}
	base := filepath.Base(abs)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return &os.PathError{Op: "write", Path: path, Err: errors.New("destination is not a file")}
	}
	parentFD, err := openUnixDirectory(filepath.Dir(abs), path)
	if err != nil {
		return err
	}
	if err := verifyPrivateDirectoryFD(parentFD, filepath.Dir(abs)); err != nil {
		_ = unix.Close(parentFD)
		return &os.PathError{Op: "write", Path: path, Err: err}
	}
	defer unix.Close(parentFD)
	tmpName, err := privateTempName()
	if err != nil {
		return &os.PathError{Op: "write", Path: path, Err: err}
	}
	fd, err := unix.Openat(parentFD, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, privateFileMode)
	if err != nil {
		return unixOpenError(path, err)
	}
	tmp := os.NewFile(uintptr(fd), filepath.Join(filepath.Dir(abs), tmpName))
	if tmp == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(parentFD, tmpName, 0)
		return unixOpenError(path, errors.New("create temporary file descriptor"))
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = unix.Unlinkat(parentFD, tmpName, 0)
		}
	}()

	if writeErr := writeAll(tmp, data); writeErr != nil {
		return errors.Join(writeErr, tmp.Close())
	}
	if err := tmp.Chmod(privateFileMode); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := verifyPrivateTemp(parentFD, tmpName, tmp); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if beforePrivateRenameHook != nil {
		beforePrivateRenameHook(filepath.Join(filepath.Dir(abs), tmpName))
	}
	if err := verifyPrivateTemp(parentFD, tmpName, tmp); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := unix.Renameat(parentFD, tmpName, parentFD, base); err != nil {
		return errors.Join(unixOpenError(path, err), tmp.Close())
	}
	removeTemp = false
	if err := verifyPrivateEntry(parentFD, base, tmp); err != nil {
		cleanupErr := unix.Unlinkat(parentFD, base, 0)
		return errors.Join(err, cleanupErr, tmp.Close())
	}
	return tmp.Close()
}

func absoluteClean(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if runtime.GOOS != "darwin" {
		return abs, nil
	}
	// macOS exposes /var and /tmp as OS-owned aliases to /private. Translate
	// only these fixed system spellings; all user-controlled components are still
	// traversed with O_NOFOLLOW below, with no validation-then-reopen step.
	for _, alias := range []struct{ lexical, physical string }{
		{lexical: "/var", physical: "/private/var"},
		{lexical: "/tmp", physical: "/private/tmp"},
	} {
		if abs == alias.lexical || strings.HasPrefix(abs, alias.lexical+string(filepath.Separator)) {
			return alias.physical + strings.TrimPrefix(abs, alias.lexical), nil
		}
	}
	return abs, nil
}

func unixPathComponents(abs string) []string {
	trimmed := strings.TrimPrefix(abs, string(filepath.Separator))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, string(filepath.Separator))
}

func openUnixRoot(path string) (int, error) {
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, unixOpenError(path, err)
	}
	return fd, nil
}

func openUnixDirectory(path, original string) (int, error) {
	abs, err := absoluteClean(path)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: original, Err: err}
	}
	fd, err := openUnixRoot(original)
	if err != nil {
		return -1, err
	}
	for _, component := range unixPathComponents(abs) {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = unix.Close(fd)
			return -1, unixOpenError(original, openErr)
		}
		if closeErr := unix.Close(fd); closeErr != nil {
			_ = unix.Close(next)
			return -1, unixOpenError(original, closeErr)
		}
		fd = next
	}
	return fd, nil
}

func verifyPrivateTraversalDirectoryFD(fd int, path string) error {
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return fmt.Errorf("%w: inspect directory %q: %v", ErrUnsafePath, path, err)
	}
	if info.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: path %q is not a directory", ErrUnsafePath, path)
	}
	perm := info.Mode & 0o7777
	if perm&0o22 != 0 && !trustedStickyDirectory(path, uint32(perm)) {
		return fmt.Errorf("%w: directory %q is group/world-writable", ErrUnsafePath, path)
	}
	return nil
}

func privateTempName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return ".codegraph-private-" + fmt.Sprintf("%x", random[:]), nil
}

func writeAll(f *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := f.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func verifyPrivateTemp(parentFD int, name string, tmp *os.File) error {
	return verifyPrivateEntry(parentFD, name, tmp)
}

// verifyPrivateEntry compares the still-open temporary descriptor with the
// directory entry through the already-open parent descriptor. Using fstatat(2)
// with AT_SYMLINK_NOFOLLOW avoids reopening the parent path and makes a
// temporary-entry substitution observable even when the path is concurrently
// replaced by a symlink.
func verifyPrivateEntry(parentFD int, name string, tmp *os.File) error {
	var descriptorInfo, entryInfo unix.Stat_t
	if err := unix.Fstat(int(tmp.Fd()), &descriptorInfo); err != nil {
		return fmt.Errorf("%w: inspect temporary file descriptor: %v", ErrUnsafePath, err)
	}
	if descriptorInfo.Mode&unix.S_IFMT != unix.S_IFREG || descriptorInfo.Mode&0o777 != privateFileMode {
		return fmt.Errorf("%w: temporary artifact is not a regular owner-only file", ErrUnsafePath)
	}
	if err := unix.Fstatat(parentFD, name, &entryInfo, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("%w: inspect temporary directory entry: %v", ErrUnsafePath, err)
	}
	if entryInfo.Mode&unix.S_IFMT != unix.S_IFREG || entryInfo.Mode&0o777 != privateFileMode ||
		descriptorInfo.Dev != entryInfo.Dev || descriptorInfo.Ino != entryInfo.Ino {
		return fmt.Errorf("%w: temporary directory entry changed", ErrUnsafePath)
	}
	return nil
}

func unixOpenError(path string, err error) error {
	pathErr := &os.PathError{Op: "open", Path: path, Err: err}
	// Darwin reports a symlink encountered with O_NOFOLLOW|O_DIRECTORY as
	// ENOTDIR, while Linux generally reports ELOOP. Both are a rejected path
	// component at this boundary, not an ordinary missing/non-directory input.
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return fmt.Errorf("%w: %w", ErrUnsafePath, pathErr)
	}
	return pathErr
}
