package index

import (
	"context"
	"path/filepath"
	"sort"

	"github.com/Lordymine/codegraph/internal/graph"
)

// tsInvalidationPolicy versions the TypeScript invalidation rule carried by
// the manifest fingerprint. ts-refs-imports-v1 narrows Modified-only
// .ts transitions to owning scopes plus proven dependents (project-reference
// dependents, transitively closed, plus direct observed importers); every
// other transition keeps the historical invalidate-all-TS-scopes behavior.
// Old graphs (built under invalidate-all) stay content-correct, but the
// version documents which rule certified reuse — and forces one rebuild so a
// narrowed certification never inherits an unversioned lineage.
const tsInvalidationPolicy = "ts-refs-imports-v1"

// inputsByPath indexes manifest input fingerprints by repo-relative path for
// the ownership model's hash cross-checks.
func inputsByPath(inputs []InputFingerprint) map[string]InputFingerprint {
	out := make(map[string]InputFingerprint, len(inputs))
	for _, in := range inputs {
		out[in.Path] = in
	}
	return out
}

// tsReferenceGraph maps each scope dir to the scope dirs it directly
// references (tsconfig "references" only — "extends" is config inheritance,
// and a changed base config is a config transition, which invalidates broadly
// via the manifest gate). Every config is parsed and hash-verified against the
// observed manifest inputs: anything unparseable, unresolvable, or changed
// since observation fails the model and the caller falls back to
// invalidate-all. A reference resolving outside the known scope set is the
// same fallback — unknown scope coupling is never guessed.
func tsReferenceGraph(root string, tsdirs []string, inputs map[string]InputFingerprint) (map[string][]string, error) {
	inScope := make(map[string]bool, len(tsdirs))
	for _, d := range tsdirs {
		inScope[d] = true
	}
	out := make(map[string][]string, len(tsdirs))
	for _, dir := range tsdirs {
		rel := dir + "/tsconfig.json"
		fp, ok := inputs[rel]
		if !ok {
			return nil, &tsModelError{what: "tsconfig input missing for scope " + dir}
		}
		data, err := readTSConfigInput(root, rel, map[string]InputFingerprint{rel: fp})
		if err != nil {
			return nil, &tsModelError{what: "read tsconfig for scope " + dir + ": " + err.Error()}
		}
		if hashBytes(data) != fp.SHA256 {
			return nil, &tsModelError{what: "tsconfig changed since observation: " + rel}
		}
		config, err := parseTSConfig(data)
		if err != nil {
			return nil, &tsModelError{what: "parse tsconfig " + rel + ": " + err.Error()}
		}
		var refs []string
		for _, spec := range config.References {
			target, err := resolveTSConfigReference(root, rel, spec)
			if err != nil {
				return nil, &tsModelError{what: "resolve reference " + spec + ": " + err.Error()}
			}
			targetDir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(target)))
			if targetDir == "." {
				targetDir = ""
			}
			if targetDir == dir {
				continue // self-reference: no edge
			}
			if !inScope[targetDir] {
				return nil, &tsModelError{what: "reference resolves outside known scopes: " + spec}
			}
			refs = append(refs, targetDir)
		}
		sort.Strings(refs)
		out[dir] = refs
	}
	return out, nil
}

// tsModelError marks an ownership-model failure: the caller must fall back to
// invalidate-all, never to a guessed scope set.
type tsModelError struct{ what string }

func (e *tsModelError) Error() string { return "ts ownership model: " + e.what }

// tsDependents returns dir plus every scope transitively depending on it
// through project references. Transitive (not just direct): a dependent's
// emitted declarations derive from its inputs, so any transitive input change
// conservatively re-resolves the whole downstream chain. The graph is tiny
// (one node per tsconfig), so closure is free.
func tsDependents(refGraph map[string][]string, dir string) map[string]bool {
	out := map[string]bool{dir: true}
	// Reverse the edges once: referenced -> referrers.
	referrers := map[string][]string{}
	for from, tos := range refGraph {
		for _, to := range tos {
			referrers[to] = append(referrers[to], from)
		}
	}
	queue := []string{dir}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, r := range referrers[cur] {
			if !out[r] {
				out[r] = true
				queue = append(queue, r)
			}
		}
	}
	return out
}

// selectiveTSInvalidation narrows Modified-only .ts transitions to owning
// scopes plus proven dependents. It returns ok=false whenever the model
// cannot prove the narrowed set — the caller then invalidates all TS scopes
// exactly as before. Soundness notes:
//
//   - Only Modified transitions qualify. Added/Deleted change scope
//     membership and import resolvability; configs change resolution itself —
//     those invalidate broadly via the manifest gate and explicit fallback.
//   - A Modified file cannot change import resolvability (same file set, same
//     configs), so imports unresolved in the old graph stay unresolved: the
//     observed IMPORTS edges are the complete coupling for this transition.
//     Any scope importing a changed file re-resolves (single hop suffices:
//     a reusing scope's pairs derive from unchanged endpoint bodies).
//   - Files owned by no scope (root-loose files, jsconfig trees) fall back:
//     the root scip program can include anything.
//   - Non-TS importers are ignored (a .rb cannot import a .ts); a TS importer
//     owned by no scope falls back instead of being silently skipped.
func selectiveTSInvalidation(ctx context.Context, store *graph.Store, project, root string, inputs map[string]InputFingerprint, modifiedTS []string, tsdirs []string) (map[string]bool, bool) {
	if store == nil || len(modifiedTS) == 0 {
		return nil, false
	}
	owners := map[string]bool{}
	for _, rel := range modifiedTS {
		owner := scopeOf(rel, tsdirs)
		if owner == "" {
			return nil, false
		}
		owners[owner] = true
	}
	refGraph, err := tsReferenceGraph(root, tsdirs, inputs)
	if err != nil {
		return nil, false
	}
	seeds := map[string]bool{}
	for owner := range owners {
		for dep := range tsDependents(refGraph, owner) {
			seeds[dep] = true
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false
	}
	importers, err := store.ImportSourcesOfFiles(project, modifiedTS)
	if err != nil {
		return nil, false
	}
	for _, src := range importers {
		if !isTSSourcePath(src) {
			continue
		}
		owner := scopeOf(src, tsdirs)
		if owner == "" {
			return nil, false
		}
		seeds[owner] = true
	}
	return seeds, true
}
