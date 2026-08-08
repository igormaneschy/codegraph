package index

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lordymine/codegraph/internal/securefile"
)

// Lang is a detected source language for a file.
type Lang string

const (
	LangGo   Lang = "go"
	LangTS   Lang = "ts"
	LangTSX  Lang = "tsx"
	LangJS   Lang = "js"
	LangRuby Lang = "ruby"
)

// SourceFile is a discovered file worth indexing.
type SourceFile struct {
	AbsPath string
	RelPath string
	Lang    Lang
}

var langByExt = map[string]Lang{
	".go":   LangGo,
	".ts":   LangTS,
	".tsx":  LangTSX,
	".js":   LangJS,
	".jsx":  LangJS,
	".mjs":  LangJS,
	".cjs":  LangJS,
	".rb":   LangRuby,
	".rake": LangRuby,
	".ru":   LangRuby,
	".rbi":  LangRuby,
}

// hardcoded ignores, same spirit as upstream (.git, node_modules, build dirs).
var hardIgnoreDir = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true,
	".next": true, ".expo": true, "coverage": true, "vendor": true,
	".cache": true, "_upstream": true, "android": true, "ios": true,
}

// Discover walks root and returns indexable source files. It honors directory
// hard-ignores plus the repo's .gitignore and .cbmignore — so a repo's vendored
// deps and build artifacts (e.g. a Go module cache under tmp/) don't flood the
// graph. Common-case ignore semantics only: directory/name patterns, globs, and
// root-anchored paths; negation (`!`) and nested .gitignore files are not honored.
func Discover(root string) ([]SourceFile, error) {
	var err error
	root, err = ValidateRepositoryRoot(root)
	if err != nil {
		return nil, err
	}
	return discoverCanonical(root)
}

// discoverCanonical is the pipeline-internal form used after the public entry
// point has already validated and canonicalized the repository. Keeping the
// validation boundary explicit avoids repeatedly scanning the same physical
// ancestor directories during one index run.
func discoverCanonical(root string) ([]SourceFile, error) {
	return discoverCanonicalContext(context.Background(), root)
}

// discoverCanonicalContext checks cancellation at every WalkDir entry. A
// canceled large discovery returns its partial result and context error rather
// than making shutdown wait for the entire repository walk.
func discoverCanonicalContext(ctx context.Context, root string) ([]SourceFile, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canonicalRoot, err := ValidateRepositoryRoot(root)
	if err != nil {
		return nil, err
	}
	root = canonicalRoot
	ignore, err := loadIgnore(root)
	if err != nil {
		return nil, err
	}

	var files []SourceFile
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			return classifyWalkError(root, path, ignore, walkErr)
		}
		// Never index or open symlink entries: a symlinked source can point
		// outside the repository root, and following it would read an untrusted
		// external file. WalkDir does not descend into symlinked directories,
		// so skipping the entry covers file and directory symlinks alike.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if shouldSkipDirectory(d.Name(), rel, ignore) {
				return filepath.SkipDir
			}
			return nil
		}
		lang, ok := langByExt[strings.ToLower(filepath.Ext(path))]
		if !ok {
			return nil
		}
		if ignore.matchFile(rel) {
			return nil
		}
		if err := verifyReadableSource(path); err != nil {
			return fmt.Errorf("read discovered source %q: %w", rel, err)
		}
		files = append(files, SourceFile{AbsPath: path, RelPath: rel, Lang: lang})
		return nil
	})
	if err == nil {
		err = ctx.Err()
	}
	return files, err
}

type ignoreSet struct{ patterns []string }

// loadIgnore reads the repo's .gitignore and .cbmignore into one matcher.
func loadIgnore(root string) (ignoreSet, error) {
	var pats []string
	gitignore, err := readIgnoreFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return ignoreSet{}, err
	}
	pats = append(pats, gitignore...)
	cbmignore, err := readIgnoreFile(filepath.Join(root, ".cbmignore"))
	if err != nil {
		return ignoreSet{}, err
	}
	pats = append(pats, cbmignore...)
	return ignoreSet{patterns: pats}, nil
}

func readIgnoreFile(file string) ([]string, error) {
	// #nosec G703 -- caller loadIgnore joins the validated repository root with
	// the fixed basenames ".gitignore"/".cbmignore"; no user-controlled component.
	f, err := securefile.OpenRead(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read ignore file %q: %w", file, err)
	}
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue // blank, comment, or unsupported negation
		}
		out = append(out, filepath.ToSlash(line))
	}
	scanErr := sc.Err()
	closeErr := f.Close()
	if scanErr != nil {
		return nil, fmt.Errorf("read ignore file %q: %w", file, scanErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close ignore file %q: %w", file, closeErr)
	}
	return out, nil
}

func shouldSkipDirectory(name, rel string, ignore ignoreSet) bool {
	if rel == "." || rel == "" {
		return false
	}
	return hardIgnoreDir[name] || strings.HasPrefix(name, ".") || ignore.matchDir(rel)
}

// classifyWalkError keeps ignored directories ignorable, but turns every other
// walk failure into a visible rebuild error. Returning nil for an unreadable
// source would make it look deleted/unchanged and silently corrupt freshness.
func classifyWalkError(root, path string, ignore ignoreSet, walkErr error) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("discover %q: %w", path, walkErr)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return fmt.Errorf("discover repository root: %w", walkErr)
	}
	// #nosec G703 -- path is supplied by filepath.WalkDir under the validated
	// repository root; stat only classifies the walk error for skip/error routing.
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() && shouldSkipDirectory(filepath.Base(path), rel, ignore) {
		return filepath.SkipDir
	}
	if underIgnoredDirectory(rel, ignore) {
		return filepath.SkipDir
	}
	if ignore.matchFile(rel) {
		return nil
	}
	return fmt.Errorf("discover %q: %w", rel, walkErr)
}

func underIgnoredDirectory(rel string, ignore ignoreSet) bool {
	parts := strings.Split(rel, "/")
	for i := 1; i <= len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		if prefix == rel {
			break
		}
		name := parts[i-1]
		if hardIgnoreDir[name] || strings.HasPrefix(name, ".") || ignore.matchDir(prefix) {
			return true
		}
	}
	return false
}

func verifyReadableSource(path string) error {
	// #nosec G703 -- path is a WalkDir-discovered source under the validated
	// repository root; open only probes readability before indexing.
	f, err := securefile.OpenRead(path)
	if err != nil {
		return err
	}
	_, readErr := io.Copy(io.Discard, f)
	closeErr := f.Close()
	return errors.Join(readErr, closeErr)
}

func (ig ignoreSet) matchDir(rel string) bool  { return ig.match(rel) }
func (ig ignoreSet) matchFile(rel string) bool { return ig.match(rel) }

// match applies common-case .gitignore semantics: a pattern with no slash matches
// that basename at any depth (file or dir); a pattern with a slash is anchored to
// the repo root (exact, directory-prefix, or glob over the full relative path).
func (ig ignoreSet) match(rel string) bool {
	base := rel
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		base = rel[i+1:]
	}
	for _, p := range ig.patterns {
		p = strings.TrimPrefix(p, "**/")
		trimmed := strings.TrimSuffix(p, "/")
		if trimmed == "" {
			continue
		}
		name := strings.TrimPrefix(trimmed, "/")
		anchored := strings.HasPrefix(trimmed, "/") || strings.Contains(name, "/")
		if anchored {
			if rel == name || strings.HasPrefix(rel, name+"/") {
				return true
			}
			if ok, _ := filepath.Match(name, rel); ok {
				return true
			}
			continue
		}
		if ok, _ := filepath.Match(name, base); ok {
			return true
		}
	}
	return false
}
