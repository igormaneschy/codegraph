package index

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestTSInvalidation_AllSourceTransitionsInvalidateEveryScope(t *testing.T) {
	dirs := []string{"a", "b", "c"}
	for _, transition := range []struct {
		name    string
		changes Changes
	}{
		{"modified dependency chain", Changes{Changed: []string{"c/source.ts"}}},
		{"modified alias importer", Changes{Changed: []string{"a/alias.ts"}}},
		{"add only", Changes{Added: []string{"b/new.ts"}}},
		{"delete only", Changes{Deleted: []string{"b/source.ts"}}},
		{"root loose", Changes{Changed: []string{"loose.ts"}}},
		{"JavaScript", Changes{Added: []string{"b/source.js"}}},
		{"JSX", Changes{Changed: []string{"b/source.jsx"}}},
		{"TSX", Changes{Deleted: []string{"b/source.tsx"}}},
		{"MJS", Changes{Changed: []string{"b/source.mjs"}}},
		{"CJS", Changes{Added: []string{"b/source.cjs"}}},
	} {
		t.Run(transition.name, func(t *testing.T) {
			changed, err := changedResolverScopes(context.Background(), transition.changes, dirs)
			if err != nil {
				t.Fatal(err)
			}
			if !changed[allTSCallScopesMarker] {
				t.Fatalf("missing all-TS marker: %v", changed)
			}
			for _, dir := range dirs {
				if !changed[dir] {
					t.Errorf("scope %q not invalidated: %v", dir, changed)
				}
			}
			if changed["go"] || changed["ruby"] {
				t.Errorf("TS-only transition invalidated unrelated language: %v", changed)
			}
		})
	}
}

func TestTSInvalidation_NoTSTransitionPreservesOtherScopeDecisions(t *testing.T) {
	changed, err := changedResolverScopes(context.Background(),
		Changes{Changed: []string{"main.go"}, Added: []string{"source.rb"}}, []string{"a", "b"})
	if err != nil || !changed["go"] || !changed["ruby"] || changed[allTSCallScopesMarker] || changed["a"] || changed["b"] {
		t.Fatalf("changed=%v err=%v, want only Go and Ruby", changed, err)
	}
}

func TestTSInvalidation_CancellationCannotCertifyScopeReuse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	changed, err := changedResolverScopes(ctx, Changes{Added: []string{"new.ts"}}, []string{"a"})
	if !errors.Is(err, context.Canceled) || changed != nil {
		t.Fatalf("changed=%v err=%v, want cancellation without a decision", changed, err)
	}
}

func TestTSInvalidation_PolicyChangeRebuildsOldCertifiedIndex(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "source.ts", "export function run() {}\n")
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.TSInvalidationVersion = "ts-refs-imports-v1"
	if err := writeManifestFile(ManifestPath(dbPath), manifest); err != nil {
		t.Fatal(err)
	}
	result, err := RunAtomic(dbPath, root)
	if err != nil || result.Reused || result.Status != StatusHealthy {
		t.Fatalf("old policy result=%+v err=%v, want healthy rebuild", result, err)
	}
	manifest, err = ReadManifest(dbPath)
	if err != nil || manifest.TSInvalidationVersion != tsInvalidationPolicy {
		t.Fatalf("manifest policy=%s err=%v, want %s", manifest.TSInvalidationVersion, err, tsInvalidationPolicy)
	}
}
