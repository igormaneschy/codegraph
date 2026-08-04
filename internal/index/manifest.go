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
	data, err := os.ReadFile(ManifestPath(dbPath))
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
	f, err := os.Open(path)
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

// fingerprintRepository fingerprints every configuration/dependency input that
// can affect discovery or resolver output. It retains the historical helper
// shape for callers that only need a manifest, while the production preparation
// path consumes scanRepositoryContext and reuses its complete result.
func fingerprintRepository(ctx context.Context, root string) (Manifest, error) {
	scan, err := scanRepositoryContext(ctx, root)
	if err != nil {
		return Manifest{}, err
	}
	return scan.manifest, nil
}

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
		data, err := os.ReadFile(path)
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
	path := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(path)
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
		// Package extends are resolved through repository-local node_modules only.
		// A package symlink that escapes the repository is rejected by the safety
		// check instead of becoming an untracked resolver input.
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
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect config reference %q: %w", filepath.ToSlash(rel), err)
	}
	if info.IsDir() {
		return "", false, nil
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("config reference %q is not a regular file", filepath.ToSlash(rel))
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false, fmt.Errorf("resolve config reference %q: %w", filepath.ToSlash(rel), err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", false, fmt.Errorf("absolute resolved config path %q: %w", filepath.ToSlash(rel), err)
	}
	resolvedRel, err := filepath.Rel(root, filepath.Clean(resolved))
	if err != nil {
		return "", false, fmt.Errorf("relativize resolved config %q: %w", filepath.ToSlash(rel), err)
	}
	if resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) || filepath.IsAbs(resolvedRel) {
		return "", false, fmt.Errorf("config reference %q resolves outside repository", filepath.ToSlash(rel))
	}
	return filepath.ToSlash(resolvedRel), true, nil
}

func sameManifestInputs(a, b []InputFingerprint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
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
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	writeErr := error(nil)
	if _, writeErr = f.Write(data); writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		return errors.Join(writeErr, closeErr)
	}
	if err := replaceManifestPlatform(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
