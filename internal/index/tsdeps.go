package index

import "context"

// Relative IMPORTS and project references do not model all TypeScript bindings
// (aliases, packages, reexports, and includes). Until dependencies are proven
// complete, every TS/JS source transition invalidates every TS/JS resolver.
// Changing the policy invalidates older, selectively certified manifests once.
const tsInvalidationPolicy = "ts-conservative-all-v2"

func changedResolverScopes(ctx context.Context, changes Changes, tsdirs []string) (map[string]bool, error) {
	if err := nonNilContext(ctx).Err(); err != nil {
		return nil, err
	}
	changed := changedScopes(changes, tsdirs)
	if !hasTSSourceChanges(changes) {
		return changed, nil
	}
	changed[allTSCallScopesMarker] = true
	for _, dir := range tsdirs {
		changed[dir] = true
	}
	return changed, nil
}

func hasTSSourceChanges(changes Changes) bool {
	for _, group := range [][]string{changes.Changed, changes.Added, changes.Deleted} {
		for _, rel := range group {
			if isTSSourcePath(rel) {
				return true
			}
		}
	}
	return false
}
