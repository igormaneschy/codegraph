package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Lordymine/codegraph/internal/securefile"
)

const resolverInputVersion = "resolver-inputs-v4"

// ResolverInputPlan describes admitted bytes and topology, not a live tree to
// rediscover during staging. NoReuseReasons makes incomplete input coverage
// explicit instead of treating lockfiles as proof of the installed program.
type ResolverInputPlan struct {
	Version        string              `json:"version"`
	Files          []InputFingerprint  `json:"files"`
	Directories    []string            `json:"directories"`
	Links          []ResolverInputLink `json:"links"`
	NoReuseReasons []string            `json:"no_reuse_reasons,omitempty"`
}

type ResolverInputLink struct {
	Path   string `json:"path"`
	Target string `json:"target"` // effective repository-relative target
}

type resolverInputObserver struct {
	root     string
	files    map[string]InputFingerprint
	dirs     map[string]bool
	links    map[string]ResolverInputLink
	visiting map[string]bool
}

func observeResolverInputs(ctx context.Context, root string, dependencyRoots, auxiliaryPaths []string) (ResolverInputPlan, error) {
	observer := resolverInputObserver{root: root, files: make(map[string]InputFingerprint), dirs: make(map[string]bool),
		links: make(map[string]ResolverInputLink), visiting: make(map[string]bool)}
	for _, rel := range dependencyRoots {
		if err := observer.observeTree(ctx, rel); err != nil {
			return ResolverInputPlan{}, err
		}
	}
	for _, rel := range auxiliaryPaths {
		if err := observer.observeFile(ctx, rel); err != nil {
			return ResolverInputPlan{}, err
		}
	}
	return observer.plan(), nil
}

func (observer *resolverInputObserver) observeFile(ctx context.Context, rel string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if isPrivateEnvironmentFile(rel) {
		return fmt.Errorf("resolver input %q is a private environment file; explicit admission is required", rel)
	}
	hash, err := hashFile(filepath.Join(observer.root, filepath.FromSlash(rel)))
	if err != nil {
		return fmt.Errorf("observe resolver input %q: %w", rel, err)
	}
	if previous, ok := observer.files[rel]; ok && previous.SHA256 != hash {
		return fmt.Errorf("resolver input %q changed during observation", rel)
	}
	observer.files[rel] = InputFingerprint{Path: rel, SHA256: hash}
	return nil
}

func (observer *resolverInputObserver) observeTree(ctx context.Context, rel string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if observer.visiting[rel] {
		return fmt.Errorf("cyclic dependency symlink at %q", rel)
	}
	if observer.dirs[rel] || observer.links[rel].Path != "" {
		return nil
	}
	observer.visiting[rel] = true
	defer delete(observer.visiting, rel)
	if err := validatePlannedPath(rel); err != nil {
		return err
	}
	if err := resolverDependencyTargetSafe(observer.root, rel); err != nil {
		return err
	}
	path := filepath.Join(observer.root, filepath.FromSlash(rel))
	// #nosec G703 -- rel is canonical/confined and parents were checked above;
	// WalkDir never follows leaf links. Content reads also use securefile.
	return filepath.WalkDir(path, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("observe dependency %q: %w", rel, walkErr)
		}
		return observer.observeEntry(ctx, path, entry)
	})
}

func (observer *resolverInputObserver) observeEntry(ctx context.Context, path string, entry os.DirEntry) error {
	rel, ok := resolverPathWithin(observer.root, path)
	if !ok || rel == "." {
		return fmt.Errorf("dependency path %q is not confined to the repository", path)
	}
	if err := resolverDependencyTargetSafe(observer.root, rel); err != nil {
		return err
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return observer.observeLink(ctx, rel)
	}
	if entry.IsDir() {
		observer.dirs[rel] = true
		return nil
	}
	return observer.observeFile(ctx, rel)
}

func (observer *resolverInputObserver) observeLink(ctx context.Context, rel string) error {
	target, err := observeDependencyLink(observer.root, rel)
	if err != nil {
		return err
	}
	if err := observer.observeTree(ctx, target); err != nil {
		return err
	}
	observer.links[rel] = ResolverInputLink{Path: rel, Target: target}
	return nil
}

func observeDependencyLink(root, rel string) (string, error) {
	if err := resolverDependencyTargetSafe(root, rel); err != nil {
		return "", err
	}
	source := filepath.Join(root, filepath.FromSlash(rel))
	// #nosec G703 -- the confined path's parents were checked above; Lstat
	// does not follow the leaf. Its identity is rechecked after Readlink.
	before, err := os.Lstat(source)
	if err != nil {
		return "", err
	}
	if before.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("%w: dependency %q is no longer a symlink", securefile.ErrUnsafePath, rel)
	}
	target, err := os.Readlink(source)
	if err != nil {
		return "", err
	}
	// #nosec G703 -- same confined no-follow metadata inspection as above;
	// following the link here would invalidate the identity check.
	after, err := os.Lstat(source)
	if err != nil || !os.SameFile(before, after) {
		return "", fmt.Errorf("%w: dependency link %q changed during observation", securefile.ErrUnsafePath, rel)
	}
	return confinedDependencyTarget(root, rel, target)
}

func confinedDependencyTarget(root, rel, target string) (string, error) {
	path := filepath.Clean(target)
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, filepath.Dir(filepath.FromSlash(rel)), path)
	}
	targetRel, ok := resolverPathWithin(root, path)
	if !ok || targetRel == "." || targetRel == "" {
		return "", fmt.Errorf("%w: dependency link %q targets outside repository", securefile.ErrUnsafePath, rel)
	}
	if err := resolverDependencyTargetSafe(root, targetRel); err != nil {
		return "", err
	}
	return targetRel, nil
}

func (observer *resolverInputObserver) plan() ResolverInputPlan {
	plan := ResolverInputPlan{Version: resolverInputVersion}
	for _, file := range observer.files {
		plan.Files = append(plan.Files, file)
	}
	for dir := range observer.dirs {
		plan.Directories = append(plan.Directories, dir)
	}
	for _, link := range observer.links {
		plan.Links = append(plan.Links, link)
	}
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].Path < plan.Files[j].Path })
	sort.Strings(plan.Directories)
	sort.Slice(plan.Links, func(i, j int) bool { return plan.Links[i].Path < plan.Links[j].Path })
	return plan
}

func isGoAuxiliaryInput(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx", ".m", ".mm", ".f", ".f90", ".s", ".syso", ".swig", ".swigcxx":
		return true
	default:
		return false
	}
}

func isPrivateEnvironmentFile(rel string) bool {
	base := strings.ToLower(filepath.Base(rel))
	return base == ".env" || strings.HasPrefix(base, ".env.")
}
