package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/Lordymine/codegraph/internal/gocalls"
	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/scip"
	"github.com/Lordymine/codegraph/internal/securefile"
)

const (
	manifestVersion        = 2
	graphSchemaVersion     = "nodes-edges-fts5-v1"
	analysisVersion        = "analysis-v1"
	discoveryRuleVersion   = "discovery-v2"
	manifestFileSuffix     = ".manifest.json"
	manifestBuildingSuffix = ".manifest.building"
)

// InputFingerprint is one deterministic graph-affecting repository input.
// Paths are repository-relative and sorted before they are serialized.
type InputFingerprint struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Manifest is the sidecar freshness contract for one graph file. The graph
// tables remain unchanged; this file records the analysis identity, the exact
// configuration inputs, and the logical graph content that produced the adjacent
// database.
type Manifest struct {
	ManifestVersion      int                `json:"manifest_version"`
	SchemaVersion        string             `json:"schema_version"`
	AnalysisVersion      string             `json:"analysis_version"`
	CanonicalRoot        string             `json:"canonical_root"`
	DiscoveryRuleVersion string             `json:"discovery_rule_version"`
	RubyResolverVersion  string             `json:"ruby_resolver_version"`
	SCIPResolverVersion  string             `json:"scip_resolver_version"`
	GoResolverVersion    string             `json:"go_resolver_version"`
	Inputs               []InputFingerprint `json:"inputs"`
	GraphIdentity        string             `json:"graph_identity"`
	GraphContentDigest   string             `json:"graph_content_digest"`
	Status               IndexStatus        `json:"status"`
	Resolver             ResolverReport     `json:"resolver"`
}

// ManifestPath returns the sidecar path adjacent to dbPath.
func ManifestPath(dbPath string) string { return dbPath + manifestFileSuffix }

func manifestBuildingPath(dbPath string) string { return dbPath + manifestBuildingSuffix }

// ReadManifest reads and validates the sidecar. A missing, malformed, or
// incomplete manifest is intentionally an ordinary freshness miss to callers.
func ReadManifest(dbPath string) (Manifest, error) {
	data, err := securefile.ReadFile(ManifestPath(dbPath))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	identity, err := graphIdentity(dbPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect graph for manifest: %w", err)
	}
	if identity != manifest.GraphIdentity {
		return Manifest{}, errors.New("manifest graph identity does not match adjacent graph")
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.ManifestVersion != manifestVersion {
		return fmt.Errorf("unsupported manifest version %d", manifest.ManifestVersion)
	}
	if manifest.SchemaVersion == "" || manifest.AnalysisVersion == "" || manifest.CanonicalRoot == "" ||
		manifest.DiscoveryRuleVersion == "" || manifest.RubyResolverVersion == "" ||
		manifest.SCIPResolverVersion == "" || manifest.GoResolverVersion == "" {
		return errors.New("manifest is missing analysis identity")
	}
	if manifest.Status != StatusHealthy && manifest.Status != StatusDegraded {
		return fmt.Errorf("invalid manifest status %q", manifest.Status)
	}
	if manifest.GraphIdentity == "" {
		return errors.New("manifest is missing graph identity")
	}
	if !validSHA256(manifest.GraphContentDigest) {
		return errors.New("manifest is missing or has invalid graph content digest")
	}
	if err := validateResolverStatus(manifest.Status, manifest.Resolver); err != nil {
		return fmt.Errorf("invalid resolver report/status: %w", err)
	}
	last := ""
	for _, input := range manifest.Inputs {
		if input.Path == "" || !validSHA256(input.SHA256) {
			return fmt.Errorf("invalid manifest input %q", input.Path)
		}
		if last != "" && input.Path <= last {
			return errors.New("manifest inputs are not strictly sorted")
		}
		last = input.Path
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hashFile(path string) (string, error) {
	f, err := securefile.OpenRead(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func newManifest(root string, inputs []InputFingerprint) Manifest {
	return Manifest{
		ManifestVersion:      manifestVersion,
		SchemaVersion:        graphSchemaVersion,
		AnalysisVersion:      analysisVersion,
		CanonicalRoot:        root,
		DiscoveryRuleVersion: discoveryRuleVersion,
		RubyResolverVersion:  fmt.Sprintf("ruby-analysis-%d", rubyAnalysisVersion),
		SCIPResolverVersion:  scip.ResolverVersion(),
		GoResolverVersion:    gocalls.ResolverVersion(),
		Inputs:               inputs,
	}
}

// repositoryScan is the single repository observation used by preparation. The
// source list, resolver scopes, and manifest inputs come from one walk so the
// pipeline does not rediscover the same repository after freshness has already
// been checked.
type repositoryScan struct {
	files              []SourceFile
	sourceObservations []sourceObservation
	sourceDigest       string
	tsdirs             []string
	manifest           Manifest
}

type sourceObservation struct {
	path string
	hash string
}

// resolverSnapshotForScan creates the only repository path handed to the Go
// and SCIP resolvers. Source/config bytes are copied through securefile into a
// private sibling directory, so a later replacement of an original path cannot
// change what the external resolver observes. The sibling placement preserves
// relative module/workspace paths such as a Go replace to ../shared. Dependency
// symlinks that escape the repository are rejected; links that target an
// in-repository file or directory are copied and rewritten to a snapshot-local
// relative target. The private directory retains its parent and child
// descriptors for identity verification and descriptor-relative cleanup; its
// lexical path is never EvalSymlinks'ed.
func resolverSnapshotForScan(ctx context.Context, scan repositoryScan) (snapshotRoot string, verify func() error, cleanup func() error, err error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return "", nil, nil, err
	}
	root := scan.manifest.CanonicalRoot
	if !resolverNeedsSnapshot(root, scan.files, scan.tsdirs) {
		return root, nil, nil, nil
	}
	// Keep the snapshot beside the canonical root rather than under an unrelated
	// system temp directory. Go replace directives and workspace references are
	// resolved relative to the module root; sibling placement preserves those
	// documented relative paths without rewriting go.mod/tsconfig contents.
	private, err := securefile.MkdirTempPrivate(filepath.Dir(root), ".codegraph-resolver-")
	if err != nil {
		return "", nil, nil, fmt.Errorf("create private resolver snapshot: %w", err)
	}
	snapshot := private.Path()
	snapshotVerify := private.Verify
	snapshotCleanup := private.Cleanup
	failed := true
	defer func() {
		if failed {
			if cleanupErr := snapshotCleanup(); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("cleanup failed private resolver snapshot: %w", cleanupErr))
			}
		}
	}()
	if err := snapshotVerify(); err != nil {
		return "", nil, nil, fmt.Errorf("verify private resolver snapshot: %w", err)
	}

	copied := make(map[string]bool, len(scan.files)+len(scan.manifest.Inputs))
	expectedHashes := make(map[string]string, len(scan.sourceObservations)+len(scan.manifest.Inputs))
	for _, observation := range scan.sourceObservations {
		expectedHashes[observation.path] = observation.hash
	}
	for _, input := range scan.manifest.Inputs {
		expectedHashes[input.Path] = input.SHA256
	}
	for _, file := range scan.files {
		if err := ctx.Err(); err != nil {
			return "", nil, nil, err
		}
		if err := snapshotVerify(); err != nil {
			return "", nil, nil, fmt.Errorf("verify resolver snapshot before source staging: %w", err)
		}
		if err := copyResolverFile(ctx, root, snapshot, file.RelPath, expectedHashes[file.RelPath]); err != nil {
			return "", nil, nil, fmt.Errorf("snapshot source %q: %w", file.RelPath, err)
		}
		copied[file.RelPath] = true
	}
	for _, input := range scan.manifest.Inputs {
		if err := ctx.Err(); err != nil {
			return "", nil, nil, err
		}
		if err := snapshotVerify(); err != nil {
			return "", nil, nil, fmt.Errorf("verify resolver snapshot before input staging: %w", err)
		}
		if copied[input.Path] {
			continue
		}
		if err := copyResolverFile(ctx, root, snapshot, input.Path, expectedHashes[input.Path]); err != nil {
			return "", nil, nil, fmt.Errorf("snapshot resolver input %q: %w", input.Path, err)
		}
		copied[input.Path] = true
	}
	for _, dependencyRoot := range []string{"node_modules", "vendor"} {
		if err := ctx.Err(); err != nil {
			return "", nil, nil, err
		}
		if err := snapshotVerify(); err != nil {
			return "", nil, nil, fmt.Errorf("verify resolver snapshot before dependency staging: %w", err)
		}
		if err := copyResolverDependencyTree(ctx, root, snapshot, dependencyRoot, copied); err != nil {
			return "", nil, nil, err
		}
	}
	if err := snapshotVerify(); err != nil {
		return "", nil, nil, fmt.Errorf("verify private resolver snapshot after staging: %w", err)
	}
	failed = false
	return snapshot, snapshotVerify, snapshotCleanup, nil
}

func resolverSnapshotForRoot(ctx context.Context, root string) (string, func() error, func() error, error) {
	scan, err := scanRepositoryContext(ctx, root)
	if err != nil {
		return "", nil, nil, err
	}
	return resolverSnapshotForScan(ctx, scan)
}

// sourceFilesAtResolverRoot keeps repository-relative identity unchanged while
// moving every disk consumer onto the staged bytes. Qualified names and graph
// file paths continue to use RelPath; only the private handoff path changes.
func sourceFilesAtResolverRoot(root string, files []SourceFile) []SourceFile {
	if root == "" {
		return files
	}
	out := make([]SourceFile, len(files))
	copy(out, files)
	for i := range out {
		out[i].AbsPath = filepath.Join(root, filepath.FromSlash(out[i].RelPath))
	}
	return out
}

func resolverNeedsSnapshot(root string, files []SourceFile, tsdirs []string) bool {
	// A snapshot is required only when a path-based external resolver will walk
	// the repository. Local parser/import/fingerprint consumers use securefile's
	// descriptor-stable reads directly; keeping the no-resolver path streaming is
	// important for the bounded-memory large-corpus pipeline.
	return len(tsdirs) > 0 || (hasGo(files) && hasGoResolverConfig(root))
}

func copyResolverFile(ctx context.Context, root, snapshot, rel, expectedHash string) error {
	rel, err := cleanResolverRelativePath(rel)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	src := filepath.Join(root, filepath.FromSlash(rel))
	data, err := securefile.ReadFile(src)
	if err != nil {
		return err
	}
	if expectedHash != "" && hashBytes(data) != expectedHash {
		return fmt.Errorf("repository input %q changed while staging resolver snapshot", rel)
	}
	dst := filepath.Join(snapshot, filepath.FromSlash(rel))
	if err := securefile.MkdirAllPrivate(filepath.Dir(dst)); err != nil {
		return err
	}
	return securefile.WritePrivate(dst, data)
}

func cleanResolverRelativePath(rel string) (string, error) {
	rel = filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if rel == "." || rel == "" || filepath.IsAbs(filepath.FromSlash(rel)) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("resolver path %q is not repository-relative", rel)
	}
	return rel, nil
}

func copyResolverDependencyTree(ctx context.Context, root, snapshot, rel string, copied map[string]bool) error {
	return copyResolverDependencyTreeVisited(ctx, root, snapshot, rel, copied, make(map[string]bool))
}

func copyResolverDependencyTreeVisited(ctx context.Context, root, snapshot, rel string, copied, visiting map[string]bool) error {
	if copied[rel] {
		return nil
	}
	if visiting[rel] {
		return fmt.Errorf("cyclic dependency symlink at %q", rel)
	}
	visiting[rel] = true
	defer delete(visiting, rel)

	source := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect dependency root %q: %w", rel, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if err := copyResolverDependencySymlink(ctx, root, snapshot, source, rel, copied, visiting); err != nil {
			return err
		}
		copied[rel] = true
		return nil
	}
	if !info.IsDir() {
		return fmt.Errorf("dependency root %q is not a directory", rel)
	}
	err = filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			return walkErr
		}
		entryRel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entryRel = filepath.ToSlash(entryRel)
		if entry.Type()&os.ModeSymlink != 0 {
			if copied[entryRel] {
				return nil
			}
			if err := copyResolverDependencySymlink(ctx, root, snapshot, path, entryRel, copied, visiting); err != nil {
				return err
			}
			copied[entryRel] = true
			return nil
		}
		dst := filepath.Join(snapshot, filepath.FromSlash(entryRel))
		if entry.IsDir() {
			return securefile.MkdirAllPrivate(dst)
		}
		if copied[entryRel] {
			return nil
		}
		if err := copyResolverFile(ctx, root, snapshot, entryRel, ""); err != nil {
			return fmt.Errorf("copy dependency %q: %w", entryRel, err)
		}
		copied[entryRel] = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("snapshot dependency tree %q: %w", rel, err)
	}
	return nil
}

func copyResolverDependencySymlink(ctx context.Context, root, snapshot, source, rel string, copied, visiting map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// #nosec G703 -- source is built from the validated repository root and a
	// repository-relative dependency entry; Lstat does not follow the symlink
	// leaf, and the after-check below rejects identity replacement.
	before, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect dependency symlink %q: %w", rel, err)
	}
	if before.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("dependency path %q is no longer a symlink", rel)
	}
	target, err := os.Readlink(source)
	if err != nil {
		return fmt.Errorf("read dependency symlink %q: %w", rel, err)
	}
	// #nosec G703 -- the same confined path is rechecked with no-follow Lstat;
	// this must remain an identity check rather than a path-following read.
	after, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("reinspect dependency symlink %q: %w", rel, err)
	}
	if !os.SameFile(before, after) || after.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%w: dependency symlink %q changed while staging", securefile.ErrUnsafePath, rel)
	}
	targetPath := filepath.Clean(target)
	if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(filepath.Dir(source), targetPath)
	}
	targetPath, err = filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("absolute dependency symlink target %q: %w", rel, err)
	}
	targetPath = filepath.Clean(targetPath)
	targetRel, ok := resolverPathWithin(root, targetPath)
	if !ok || targetRel == "." || targetRel == "" {
		return fmt.Errorf("%w: dependency symlink %q targets outside repository: %q", securefile.ErrUnsafePath, rel, target)
	}
	if err := resolverDependencyTargetSafe(root, targetRel); err != nil {
		return fmt.Errorf("dependency symlink %q target %q: %w", rel, targetRel, err)
	}
	dst := filepath.Join(snapshot, filepath.FromSlash(rel))
	if err := securefile.MkdirAllPrivate(filepath.Dir(dst)); err != nil {
		return err
	}
	if !copied[targetRel] {
		info, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(targetRel)))
		if statErr != nil {
			return fmt.Errorf("inspect in-repository dependency target %q: %w", targetRel, statErr)
		}
		switch {
		case info.IsDir(), info.Mode()&os.ModeSymlink != 0:
			if err := copyResolverDependencyTreeVisited(ctx, root, snapshot, targetRel, copied, visiting); err != nil {
				return fmt.Errorf("copy in-repository dependency target %q: %w", targetRel, err)
			}
		case info.Mode().IsRegular():
			if err := copyResolverFile(ctx, root, snapshot, targetRel, ""); err != nil {
				return fmt.Errorf("copy in-repository dependency target %q: %w", targetRel, err)
			}
			copied[targetRel] = true
		default:
			return fmt.Errorf("in-repository dependency target %q is not a regular file, directory, or symlink", targetRel)
		}
	}
	targetSnapshotPath := filepath.Join(snapshot, filepath.FromSlash(targetRel))
	linkTarget, err := filepath.Rel(filepath.Dir(dst), targetSnapshotPath)
	if err != nil {
		return fmt.Errorf("relativize dependency symlink %q: %w", rel, err)
	}
	if err := securefile.SymlinkPrivate(linkTarget, dst); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("dependency symlink destination %q already exists", rel)
		}
		return fmt.Errorf("write dependency symlink %q: %w", rel, err)
	}
	return nil
}

func resolverDependencyTargetSafe(root, rel string) error {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	current := root
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: invalid dependency target path %q", securefile.ErrUnsafePath, rel)
		}
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect dependency target %q: %w", rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 && i != len(parts)-1 {
			return fmt.Errorf("%w: dependency target %q contains a symlink component", securefile.ErrUnsafePath, rel)
		}
	}
	return nil
}

func resolverPathWithin(root, candidate string) (string, bool) {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// manifestWalkDir is a narrow deterministic test seam for walk failures. The
// production value is filepath.WalkDir; tests can inject a synthetic callback
// error without relying on platform-specific filesystem permissions.
var manifestWalkDir = filepath.WalkDir

// validateStoreIntegrity is kept behind a package-local seam so freshness tests
// can prove which production phase paid for the exact validator. The production
// implementation remains Store.ValidateIntegrity; no integrity check is cached
// across preparations or database connections.
var validateStoreIntegrity = func(store *graph.Store) error {
	return store.ValidateIntegrity()
}

// freshManifestBeforeIntegrityHook is nil in production. It lets the freshness
// regression test perform a deterministic graph mutation in the interval before
// the one exact no-op gate, proving that deduplication does not hide a mutation.
var freshManifestBeforeIntegrityHook func()

// scanRepositoryContext validates discoverable source files, enumerates usable
// TypeScript scopes, and fingerprints manifest inputs in one candidate-aware
// walk. Soft-ignored directories remain traversable for recognized manifest
// inputs, but their source files are excluded exactly as discovery excludes
// them. Explicit TS config references are expanded after the walk.
func scanRepositoryContext(ctx context.Context, root string) (repositoryScan, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return repositoryScan{}, err
	}
	canonicalRoot, err := ValidateRepositoryRoot(root)
	if err != nil {
		return repositoryScan{}, err
	}
	root = canonicalRoot
	ignore, err := loadIgnore(root)
	if err != nil {
		return repositoryScan{}, err
	}
	inputsByPath := make(map[string]InputFingerprint)
	var files []SourceFile
	var sourceObservations []sourceObservation
	var tsconfigDirs []string
	rootHasTSConfig := false
	err = manifestWalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			return classifyManifestWalkError(root, path, entry, walkErr)
		}
		// Never hash, fingerprint, or read an unrecognized symlink entry: it may
		// point outside the repository root. Recognized manifest/resolver inputs
		// are different: silently skipping one would remove a resolver scope or
		// turn an attempted input change into a false fresh/no-op result.
		if entry.Type()&os.ModeSymlink != 0 {
			rel := filepath.ToSlash(mustRel(root, path))
			if isManifestInput(rel) || rel == ".gitignore" || rel == ".cbmignore" {
				return fmt.Errorf("unsafe manifest/resolver input %q: %w", rel, securefile.ErrUnsafePath)
			}
			return nil
		}
		if entry.IsDir() {
			// Soft ignore rules cannot prune this walk: a supported manifest input
			// may live below an ignored directory and still change resolver
			// topology. Hard ignores and hidden directories remain pruned so source
			// files are never indexed or fingerprinted through this walk.
			if shouldSkipManifestDirectory(entry.Name(), filepath.ToSlash(mustRel(root, path))) {
				return filepath.SkipDir
			}
			return nil
		}
		rel := filepath.ToSlash(mustRel(root, path))
		ignoredByDiscovery := ignore.matchFile(rel) || underIgnoredDirectory(rel, ignore)
		lang, isSource := langByExt[strings.ToLower(filepath.Ext(path))]
		isIgnoreInput := rel == ".gitignore" || rel == ".cbmignore"
		isAnalysisInput := isManifestInput(rel)
		if isSource && !ignoredByDiscovery {
			hash, err := hashFile(path)
			if err != nil {
				return fmt.Errorf("read discovered source %q: %w", rel, err)
			}
			files = append(files, SourceFile{AbsPath: path, RelPath: rel, Lang: lang})
			sourceObservations = append(sourceObservations, sourceObservation{path: rel, hash: hash})
		}
		// Manifest inputs are graph-affecting even when a repository ignore rule
		// also names them. In particular, workspace topology files can change the
		// resolver scopes without changing any discovered source file.
		if !isIgnoreInput && !isAnalysisInput && ignore.matchFile(rel) {
			return nil
		}
		if !isAnalysisInput && !isIgnoreInput {
			return nil
		}
		data, err := securefile.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read analysis input %q: %w", rel, err)
		}
		inputsByPath[rel] = InputFingerprint{Path: rel, SHA256: hashBytes(data)}
		if entry.Name() == "tsconfig.json" && !ignoredByDiscovery {
			dir, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return fmt.Errorf("relativize TypeScript config %q: %w", rel, err)
			}
			dir = filepath.ToSlash(dir)
			if dir == "." {
				rootHasTSConfig = true
			} else {
				tsconfigDirs = append(tsconfigDirs, dir)
			}
		}
		return nil
	})
	if err != nil {
		return repositoryScan{}, err
	}
	sort.Strings(tsconfigDirs)
	if len(tsconfigDirs) == 0 && rootHasTSConfig {
		tsconfigDirs = []string{""}
	} else if rootHasTSConfig {
		for _, file := range files {
			if file.Lang != LangTS && file.Lang != LangTSX && file.Lang != LangJS {
				continue
			}
			insideChild := false
			for _, dir := range tsconfigDirs {
				if file.RelPath == dir || strings.HasPrefix(file.RelPath, dir+"/") {
					insideChild = true
					break
				}
			}
			if !insideChild {
				tsconfigDirs = append(tsconfigDirs, "")
				break
			}
		}
	}
	sort.Strings(tsconfigDirs)
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelPath < files[j].RelPath
	})
	sort.Slice(sourceObservations, func(i, j int) bool {
		return sourceObservations[i].path < sourceObservations[j].path
	})
	sourceDigest := sourceObservationDigest(sourceObservations)
	if err := addReferencedTSConfigInputs(ctx, root, inputsByPath); err != nil {
		return repositoryScan{}, err
	}
	inputs := make([]InputFingerprint, 0, len(inputsByPath))
	for _, input := range inputsByPath {
		inputs = append(inputs, input)
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Path < inputs[j].Path })
	return repositoryScan{
		files:              files,
		sourceObservations: sourceObservations,
		sourceDigest:       sourceDigest,
		tsdirs:             tsconfigDirs,
		manifest:           newManifest(root, inputs),
	}, nil
}

func sourceObservationDigest(observations []sourceObservation) string {
	h := sha256.New()
	separator := []byte{0}
	for _, observation := range observations {
		_, _ = io.WriteString(h, observation.path)
		_, _ = h.Write(separator)
		_, _ = io.WriteString(h, observation.hash)
		_, _ = h.Write(separator)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func shouldSkipManifestDirectory(name, rel string) bool {
	if rel == "." || rel == "" {
		return false
	}
	return hardIgnoreDir[name] || strings.HasPrefix(name, ".")
}

func mustRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Base(path)
	}
	return rel
}

func isManifestInput(rel string) bool {
	base := strings.ToLower(filepath.Base(rel))
	if strings.HasPrefix(base, "tsconfig") && strings.HasSuffix(base, ".json") {
		return true
	}
	if strings.HasPrefix(base, "jsconfig") && strings.HasSuffix(base, ".json") {
		return true
	}
	switch base {
	case "go.mod", "go.sum", "go.work", "go.work.sum",
		"package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock",
		"pnpm-lock.yaml", "bun.lock", "bun.lockb", "gemfile", "gemfile.lock",
		".ruby-version", "pnpm-workspace.yaml", "pnpm-workspace.yml",
		"turbo.json", "nx.json", "lerna.json", "rush.json", "workspace.json":
		return true
	default:
		return false
	}
}

// addReferencedTSConfigInputs expands the manifest's input set from the entry
// config allowlist to the config graph that TypeScript can actually consume.
// Explicit references are never ignored: a missing, unreadable, or malformed
// referenced config is a freshness error, so an old graph cannot be certified
// as current under an incomplete resolver environment.
func addReferencedTSConfigInputs(ctx context.Context, root string, inputs map[string]InputFingerprint) error {
	ctx = nonNilContext(ctx)
	var roots []string
	for rel := range inputs {
		base := strings.ToLower(filepath.Base(rel))
		if base == "tsconfig.json" || base == "jsconfig.json" {
			roots = append(roots, rel)
		}
	}
	sort.Strings(roots)
	visited := make(map[string]bool)
	visiting := make(map[string]bool)
	var visit func(string) error
	visit = func(rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if visited[rel] {
			return nil
		}
		if visiting[rel] {
			return fmt.Errorf("cyclic TypeScript config reference at %q", rel)
		}
		visiting[rel] = true
		defer delete(visiting, rel)

		data, err := readTSConfigInput(root, rel, inputs)
		if err != nil {
			return err
		}
		config, err := parseTSConfig(data)
		if err != nil {
			return fmt.Errorf("parse TypeScript config %q: %w", rel, err)
		}
		for _, spec := range config.Extends {
			referenced, err := resolveTSConfigReference(root, rel, spec)
			if err != nil {
				return fmt.Errorf("resolve extends %q from %q: %w", spec, rel, err)
			}
			if err := visit(referenced); err != nil {
				return err
			}
		}
		for _, spec := range config.References {
			referenced, err := resolveTSConfigReference(root, rel, spec)
			if err != nil {
				return fmt.Errorf("resolve project reference %q from %q: %w", spec, rel, err)
			}
			if err := visit(referenced); err != nil {
				return err
			}
		}
		visited[rel] = true
		return nil
	}
	for _, rel := range roots {
		if err := visit(rel); err != nil {
			return err
		}
	}
	return nil
}

type tsConfigReferences struct {
	Extends    []string
	References []string
}

// readTSConfigInput reads an explicit config and records it exactly once in the
// manifest input set. Referenced configs may live below node_modules or another
// ignored directory; explicit resolver inputs still get fingerprinted.
func readTSConfigInput(root, rel string, inputs map[string]InputFingerprint) ([]byte, error) {
	canonicalRoot, err := ValidateRepositoryRoot(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root for referenced TypeScript config %q: %w", rel, err)
	}
	root = canonicalRoot
	path := filepath.Join(root, filepath.FromSlash(rel))
	data, err := securefile.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read referenced TypeScript config %q: %w", rel, err)
	}
	inputs[rel] = InputFingerprint{Path: rel, SHA256: hashBytes(data)}
	return data, nil
}

func parseTSConfig(data []byte) (tsConfigReferences, error) {
	normalized, err := normalizeTSConfigJSON(data)
	if err != nil {
		return tsConfigReferences{}, err
	}
	var raw struct {
		Extends    json.RawMessage `json:"extends"`
		References json.RawMessage `json:"references"`
	}
	if err := json.Unmarshal(normalized, &raw); err != nil {
		return tsConfigReferences{}, err
	}
	var out tsConfigReferences
	if value := bytes.TrimSpace(raw.Extends); len(value) > 0 && !bytes.Equal(value, []byte("null")) {
		var spec string
		if err := json.Unmarshal(value, &spec); err != nil || strings.TrimSpace(spec) == "" {
			if err == nil {
				err = errors.New("extends must be a non-empty string")
			}
			return tsConfigReferences{}, err
		}
		out.Extends = append(out.Extends, strings.TrimSpace(spec))
	}
	if value := bytes.TrimSpace(raw.References); len(value) > 0 && !bytes.Equal(value, []byte("null")) {
		var refs []struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(value, &refs); err != nil {
			return tsConfigReferences{}, fmt.Errorf("references must be an array of objects: %w", err)
		}
		for i, ref := range refs {
			if strings.TrimSpace(ref.Path) == "" {
				return tsConfigReferences{}, fmt.Errorf("references[%d].path must be a non-empty string", i)
			}
			out.References = append(out.References, strings.TrimSpace(ref.Path))
		}
	}
	return out, nil
}

// normalizeTSConfigJSON accepts the JSON-with-comments/trailing-commas form
// TypeScript config files commonly use, while leaving actual JSON syntax errors
// for encoding/json to reject.
func normalizeTSConfigJSON(data []byte) ([]byte, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	out := make([]byte, 0, len(data))
	inString, escaped, lineComment, blockComment := false, false, false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if lineComment {
			if c == '\n' {
				lineComment = false
				out = append(out, c)
			}
			continue
		}
		if blockComment {
			if c == '*' && i+1 < len(data) && data[i+1] == '/' {
				blockComment = false
				i++
				continue
			}
			if c == '\n' {
				out = append(out, c)
			}
			continue
		}
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			out = append(out, c)
		case '/':
			if i+1 < len(data) && data[i+1] == '/' {
				lineComment = true
				i++
			} else if i+1 < len(data) && data[i+1] == '*' {
				blockComment = true
				i++
			} else {
				out = append(out, c)
			}
		default:
			out = append(out, c)
		}
	}
	if blockComment {
		return nil, errors.New("unterminated block comment")
	}
	return stripTrailingJSONCommas(out), nil
}

func stripTrailingJSONCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString, escaped := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(data) && isJSONWhitespace(data[j]) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func isJSONWhitespace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t'
}

func resolveTSConfigReference(root, fromRel, spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", errors.New("empty config reference")
	}
	fromAbs := filepath.Join(root, filepath.FromSlash(fromRel))
	var bases []string
	specPath := filepath.FromSlash(spec)
	switch {
	case filepath.IsAbs(specPath):
		bases = append(bases, filepath.Clean(specPath))
	case strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") || spec == "." || spec == "..":
		bases = append(bases, filepath.Join(filepath.Dir(fromAbs), specPath))
	default:
		// First try a repository-relative path. TypeScript project references are
		// commonly written as "packages/app" (without "./"), while package
		// extends still fall through to repository-local node_modules when that
		// path does not exist. A package symlink that escapes the repository is
		// rejected by the safety check instead of becoming an untracked input.
		bases = append(bases, filepath.Join(filepath.Dir(fromAbs), specPath))
		for dir := filepath.Dir(fromAbs); ; dir = filepath.Dir(dir) {
			bases = append(bases, filepath.Join(dir, "node_modules", specPath))
			if dir == root {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
	}
	for _, base := range bases {
		for _, candidate := range configReferenceCandidates(base) {
			rel, exists, err := safeConfigCandidate(root, candidate)
			if err != nil {
				return "", err
			}
			if exists {
				return rel, nil
			}
		}
	}
	return "", fmt.Errorf("config file not found inside repository")
}

func configReferenceCandidates(base string) []string {
	var candidates []string
	seen := make(map[string]bool)
	add := func(candidate string) {
		candidate = filepath.Clean(candidate)
		if !seen[candidate] {
			seen[candidate] = true
			candidates = append(candidates, candidate)
		}
	}
	add(base)
	if strings.ToLower(filepath.Ext(base)) != ".json" {
		add(base + ".json")
	}
	add(filepath.Join(base, "tsconfig.json"))
	add(filepath.Join(base, "jsconfig.json"))
	return candidates
}

func safeConfigCandidate(root, candidate string) (string, bool, error) {
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", false, fmt.Errorf("absolute config path %q: %w", candidate, err)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", false, fmt.Errorf("relativize config path %q: %w", candidate, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false, fmt.Errorf("config reference %q escapes repository root", candidate)
	}
	f, err := securefile.OpenRead(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		if errors.Is(err, securefile.ErrNotRegular) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect config reference %q: %w", filepath.ToSlash(rel), err)
	}
	if err := f.Close(); err != nil {
		return "", false, fmt.Errorf("close config reference %q: %w", filepath.ToSlash(rel), err)
	}
	return filepath.ToSlash(rel), true, nil
}

func sameManifestInputs(a, b []InputFingerprint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		// #nosec G602 -- the equal-length guard immediately above proves the
		// corresponding index exists in both slices.
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameRepositoryScan(a, b repositoryScan) bool {
	if !sameManifestFingerprint(a.manifest, b.manifest) || !sameStringSlice(a.tsdirs, b.tsdirs) {
		return false
	}
	if len(a.files) != len(b.files) {
		return false
	}
	for i := range a.files {
		if a.files[i] != b.files[i] {
			return false
		}
	}
	if len(a.sourceObservations) != len(b.sourceObservations) {
		return false
	}
	for i := range a.sourceObservations {
		if a.sourceObservations[i] != b.sourceObservations[i] {
			return false
		}
	}
	return a.sourceDigest == b.sourceDigest
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		// #nosec G602 -- the equal-length guard immediately above proves the
		// corresponding index exists in both slices.
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// classifyManifestWalkError is deliberately stricter than ordinary discovery:
// a soft-ignored subtree may contain a manifest input and therefore cannot be
// silently pruned after WalkDir reports an error. Only a confirmed hard-ignore
// or hidden directory is safe to prune without an error.
func classifyManifestWalkError(root, path string, entry os.DirEntry, walkErr error) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("scan repository %q: %w", path, walkErr)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return fmt.Errorf("scan repository root: %w", walkErr)
	}
	if entry != nil && entry.IsDir() && hasHardOrHiddenDirectory(rel) {
		return filepath.SkipDir
	}
	if entry == nil {
		// #nosec G703 -- path comes from the manifest WalkDir under the validated
		// root; stat only classifies the walk error for skip/error routing.
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() && hasHardOrHiddenDirectory(rel) {
			return filepath.SkipDir
		}
	}
	return fmt.Errorf("scan repository %q: %w", rel, walkErr)
}

func hasHardOrHiddenDirectory(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		if hardIgnoreDir[part] || strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func sameManifestFingerprint(stored, current Manifest) bool {
	return stored.ManifestVersion == current.ManifestVersion &&
		stored.SchemaVersion == current.SchemaVersion &&
		stored.AnalysisVersion == current.AnalysisVersion &&
		stored.CanonicalRoot == current.CanonicalRoot &&
		stored.DiscoveryRuleVersion == current.DiscoveryRuleVersion &&
		stored.RubyResolverVersion == current.RubyResolverVersion &&
		stored.SCIPResolverVersion == current.SCIPResolverVersion &&
		stored.GoResolverVersion == current.GoResolverVersion &&
		sameManifestInputs(stored.Inputs, current.Inputs)
}

// freshManifestFor returns the persisted manifest only when its analysis/input
// identity, graph file identity, SQLite integrity, and logical graph content
// agree. Both healthy and explicitly degraded committed graphs are reusable;
// ReadManifest rejects a healthy manifest that records failed resolver scopes.
func freshManifestFor(store *graph.Store, project string, current Manifest) (Manifest, bool) {
	stored, ok := manifestFingerprintFor(store, current)
	if !ok {
		return Manifest{}, false
	}
	digest, err := store.LogicalGraphDigest(project)
	if err != nil || digest != stored.GraphContentDigest {
		return Manifest{}, false
	}
	// Keep the exact validator as the last operation before no-op certification.
	// This avoids a second validation in prepareIndexingContext and ensures a
	// mutation observed between the digest phase and this final gate cannot be
	// silently certified as fresh.
	if freshManifestBeforeIntegrityHook != nil {
		freshManifestBeforeIntegrityHook()
	}
	if err := validateStoreIntegrity(store); err != nil {
		return Manifest{}, false
	}
	return stored, true
}

func manifestFingerprintFor(store *graph.Store, current Manifest) (Manifest, bool) {
	if store == nil {
		return Manifest{}, false
	}
	stored, err := ReadManifest(store.DBPath())
	if err != nil || !sameManifestFingerprint(stored, current) {
		return Manifest{}, false
	}
	identity, err := graphIdentity(store.DBPath())
	if err != nil || identity != stored.GraphIdentity {
		return Manifest{}, false
	}
	return stored, true
}

// graphIdentity changes when RunAtomic installs a different database inode,
// while ordinary SQLite WAL/checkpoint updates keep the identity stable. This
// lets the manifest detect a graph/sidecar commit mismatch without turning a
// caller's normal WAL write into a false freshness failure.
func graphIdentity(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	var parts []string
	sys := info.Sys()
	if sys != nil {
		value := reflect.ValueOf(sys)
		if value.Kind() == reflect.Pointer && !value.IsNil() {
			value = value.Elem()
		}
		if value.IsValid() && value.Kind() == reflect.Struct {
			for _, name := range []string{"Dev", "Ino", "FileIndexHigh", "FileIndexLow"} {
				field := value.FieldByName(name)
				if field.IsValid() && field.CanInterface() {
					parts = append(parts, name+"="+fmt.Sprint(field.Interface()))
				}
			}
		}
	}
	if len(parts) == 0 {
		parts = append(parts,
			fmt.Sprintf("mode=%o", info.Mode()),
			fmt.Sprintf("size=%d", info.Size()),
			"mtime="+fmt.Sprint(info.ModTime().UnixNano()),
		)
	}
	return strings.Join(parts, ";"), nil
}

// writeManifestFile writes a manifest through a temporary file and a single
// destination rename. The destination is never partially written. The graph
// commit uses a separate adjacent building path, so a crash between graph and
// manifest installation is detected by the graph identity on the next prepare
// pass. The identity is deliberately the atomic file identity, not a WAL hash;
// logical row content is covered separately by GraphContentDigest.
func writeManifestFile(path string, manifest Manifest) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	// securefile owns the same-directory temporary entry, Sync, no-follow
	// parent traversal, temporary identity check, and atomic destination
	// replacement. In particular, a sidecar symlink is replaced as a directory
	// entry rather than followed, and a temporary-entry substitution fails closed.
	return securefile.WritePrivate(path, data)
}
