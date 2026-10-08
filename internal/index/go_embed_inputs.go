package index

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/module"

	"github.com/Lordymine/codegraph/internal/securefile"
)

// goEmbedInputPaths conservatively admits the union of local directives, even
// in inactive source files. It does not certify build selection/program closure.
func goEmbedInputPaths(ctx context.Context, scan repositoryScan) ([]string, error) {
	expected := sourceObservationHashes(scan.sourceObservations)
	selected := make(map[string]bool)
	for _, file := range scan.files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if file.Lang != LangGo {
			continue
		}
		content, err := securefile.ReadFile(file.AbsPath)
		if err != nil {
			return nil, err
		}
		if hashBytes(content) != expected[file.RelPath] {
			return nil, fmt.Errorf("go source %q changed during embed observation", file.RelPath)
		}
		if err := selectGoEmbedPatterns(ctx, scan.manifest.CanonicalRoot, file, goEmbedPatterns(file.RelPath, content), selected); err != nil {
			return nil, err
		}
	}
	var paths []string
	for rel := range selected {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	return paths, nil
}

func selectGoEmbedPatterns(ctx context.Context, root string, file SourceFile, patterns []string, selected map[string]bool) error {
	packageDir := path.Dir(file.RelPath)
	files := &embedInputFilesystem{ctx: ctx, root: filepath.Join(root, filepath.FromSlash(packageDir))}
	for _, pattern := range patterns {
		glob, all := strings.CutPrefix(pattern, "all:")
		// Invalid/empty patterns remain compiler errors, not guessed inputs.
		if _, err := path.Match(glob, ""); err != nil || glob == "." || !fs.ValidPath(glob) {
			continue
		}
		matches, err := fs.Glob(files, glob)
		if err != nil || files.fault != nil {
			return fmt.Errorf("observe embed pattern %q in %q: %w", pattern, file.RelPath, errors.Join(err, files.fault))
		}
		for _, match := range matches {
			if err := selectGoEmbedMatch(files, packageDir, match, all, selected); err != nil {
				return fmt.Errorf("observe embed pattern %q in %q: %w", pattern, file.RelPath, err)
			}
		}
	}
	return nil
}

func selectGoEmbedMatch(files *embedInputFilesystem, packageDir, match string, all bool, selected map[string]bool) error {
	info, err := files.Stat(match)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: embed match %q is a symlink", securefile.ErrUnsafePath, match)
	}
	allowed, err := goEmbedMatchAllowed(files, match)
	if err != nil || !allowed {
		return err
	}
	if info.Mode().IsRegular() {
		selected[path.Join(packageDir, match)] = true
		return nil
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: embed match %q must be a regular file or directory", securefile.ErrNotRegular, match)
	}
	return walkGoEmbedDirectory(files, packageDir, match, all, selected)
}

func goEmbedMatchAllowed(files *embedInputFilesystem, match string) (bool, error) {
	for dir := match; dir != "."; dir = path.Dir(dir) {
		if badGoEmbedName(path.Base(dir)) {
			return false, fmt.Errorf("embed match %q has an invalid path component %q", match, dir)
		}
		info, err := files.Stat(dir)
		if err != nil {
			return false, err
		}
		if !info.IsDir() {
			continue
		}
		boundary, err := goEmbedModuleBoundary(files, dir)
		if err != nil {
			return false, err
		}
		if boundary {
			return false, fmt.Errorf("embed match %q crosses module boundary %q", match, dir)
		}
	}
	return true, nil
}

func walkGoEmbedDirectory(files *embedInputFilesystem, packageDir, start string, all bool, selected map[string]bool) error {
	return fs.WalkDir(files, start, func(name string, entry fs.DirEntry, walkErr error) error {
		if err := files.ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		base := entry.Name()
		hidden := strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_")
		if name != start && (badGoEmbedName(base) || !all && hidden) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			if hidden {
				return nil
			}
			return fmt.Errorf("embed entry %q has an invalid filename", name)
		}
		return selectGoEmbedDirectoryEntry(files, packageDir, name, entry, selected)
	})
}

func selectGoEmbedDirectoryEntry(files *embedInputFilesystem, packageDir, name string, entry fs.DirEntry, selected map[string]bool) error {
	if entry.IsDir() {
		boundary, err := goEmbedModuleBoundary(files, name)
		if err != nil {
			return err
		}
		if boundary {
			return fs.SkipDir
		}
		return nil
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: selected embed entry %q is a symlink", securefile.ErrUnsafePath, name)
	}
	if entry.Type().IsRegular() {
		selected[path.Join(packageDir, name)] = true
	}
	return nil
}

func goEmbedModuleBoundary(files *embedInputFilesystem, dir string) (bool, error) {
	_, err := files.Stat(path.Join(dir, "go.mod"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func badGoEmbedName(name string) bool {
	if module.CheckFilePath(name) != nil {
		return true
	}
	switch name {
	case "vendor", ".git", ".hg", ".svn", ".bzr":
		return true
	default:
		return false
	}
}
