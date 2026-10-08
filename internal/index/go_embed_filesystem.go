package index

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Lordymine/codegraph/internal/securefile"
)

// embedInputFilesystem only traverses no-follow directories. Glob's generic
// algorithm ignores ReadDir errors, so fault retains security/cancellation
// failures rather than silently treating them as an empty match.
type embedInputFilesystem struct {
	ctx   context.Context
	root  string
	fault error
}

func (files *embedInputFilesystem) filename(name string) (string, error) {
	if err := files.ctx.Err(); err != nil {
		return "", err
	}
	if !fs.ValidPath(name) {
		return "", fmt.Errorf("embed path %q must be a valid package-relative path", name)
	}
	if err := resolverDependencyTargetSafe(files.root, name); err != nil {
		return "", err
	}
	return filepath.Join(files.root, filepath.FromSlash(name)), nil
}

func (files *embedInputFilesystem) remember(err error) {
	if err != nil && !errors.Is(err, fs.ErrNotExist) && files.fault == nil {
		files.fault = err
	}
}

func (files *embedInputFilesystem) Open(name string) (fs.File, error) {
	if isPrivateEnvironmentFile(name) {
		return nil, fmt.Errorf("embed input %q requires explicit private-file admission", name)
	}
	filename, err := files.filename(name)
	if err != nil {
		files.remember(err)
		return nil, err
	}
	file, err := securefile.OpenRead(filename)
	files.remember(err)
	return file, err
}

func (files *embedInputFilesystem) Stat(name string) (fs.FileInfo, error) {
	filename, err := files.filename(name)
	if err != nil {
		files.remember(err)
		return nil, err
	}
	// #nosec G703 -- name is a confined valid FS path, parents are checked,
	// and Lstat reads only metadata without following the leaf.
	info, err := os.Lstat(filename)
	files.remember(err)
	return info, err
}

func (files *embedInputFilesystem) ReadDir(name string) ([]fs.DirEntry, error) {
	filename, err := files.filename(name)
	if err != nil {
		files.remember(err)
		return nil, err
	}
	entries, err := securefile.ReadDir(filename)
	files.remember(err)
	return entries, err
}
