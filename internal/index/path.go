package index

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalPath returns an absolute, cleaned path with every existing symlink
// component resolved and existing directory components rewritten to their
// physical directory-entry spelling. This makes platform aliases such as
// macOS /var and /private/var share an identity. It deliberately does not
// case-fold paths: on a case-sensitive filesystem, two differently cased
// entries are distinct.
//
// A missing leaf is allowed so first-time database creation still works. Its
// existing ancestors are canonicalized and the missing suffix is retained as
// supplied. If an existing component has more than one directory entry that
// names the same physical file, canonicalization fails instead of guessing.
func CanonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}
	abs = filepath.Clean(abs)

	existing, missing, err := nearestExistingPath(abs)
	if err != nil {
		return "", err
	}
	resolved, err := resolveRequestedRootSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks for %q: %w", existing, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("absolute resolved path: %w", err)
	}
	canonical, err := canonicalExistingSpelling(filepath.Clean(resolved))
	if err != nil {
		return "", err
	}
	if len(missing) == 0 {
		return canonical, nil
	}
	parts := append([]string{canonical}, missing...)
	return filepath.Clean(filepath.Join(parts...)), nil
}

// ValidateRepositoryRoot resolves root and requires the resolved path to be an
// existing directory. CanonicalPath intentionally permits a missing leaf for
// database and lock paths; repository entry points must use this stricter
// contract so a typo cannot become a successful empty graph.
func ValidateRepositoryRoot(root string) (string, error) {
	canonical, err := CanonicalPath(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root %q: %w", root, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect repository root %q: %w", canonical, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository root %q is not a directory", canonical)
	}
	return canonical, nil
}

func resolveRequestedRootSymlinks(path string) (string, error) {
	// EvalSymlinks resolves ancestor entries as well as a symlink at the
	// requested leaf. Resolving only the leaf leaves aliases such as /var and
	// /private/var with different project identities.
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func nearestExistingPath(path string) (string, []string, error) {
	current := path
	var missing []string
	for {
		if _, err := os.Stat(current); err == nil {
			return current, missing, nil
		} else if !os.IsNotExist(err) {
			return "", nil, fmt.Errorf("inspect path %q: %w", current, err)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, fmt.Errorf("no existing ancestor for %q", path)
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
}

func canonicalExistingSpelling(path string) (string, error) {
	root, err := absolutePathRoot(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("relativize canonical path %q: %w", path, err)
	}
	if rel == "." {
		return root, nil
	}

	current := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		entry, err := canonicalDirectoryEntry(current, component)
		if err != nil {
			return "", err
		}
		current = filepath.Join(current, entry)
	}
	return filepath.Clean(current), nil
}

func absolutePathRoot(path string) (string, error) {
	volume := filepath.VolumeName(path)
	if volume == "" {
		if !filepath.IsAbs(path) {
			return "", fmt.Errorf("path root is not absolute: %q", path)
		}
		return string(filepath.Separator), nil
	}
	root := volume
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return filepath.Clean(root), nil
}

func canonicalDirectoryEntry(parent, name string) (string, error) {
	target := filepath.Join(parent, name)
	targetInfo, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("inspect directory entry %q: %w", target, err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", fmt.Errorf("read directory %q: %w", parent, err)
	}

	matches := make([]string, 0, 1)
	for _, entry := range entries {
		candidate := filepath.Join(parent, entry.Name())
		entryInfo, err := os.Lstat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("inspect directory entry %q: %w", candidate, err)
		}
		// Symlink aliases in the same directory are not competing physical
		// spellings for the requested root.
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("inspect directory entry %q: %w", candidate, err)
		}
		if os.SameFile(targetInfo, info) {
			matches = append(matches, entry.Name())
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("cannot establish physical spelling for %q", target)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous physical aliases for %q: %s", target, strings.Join(matches, ", "))
	}
	return matches[0], nil
}
