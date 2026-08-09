package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	scippb "github.com/scip-code/scip/bindings/go/scip"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/scip"
	"github.com/Lordymine/codegraph/internal/securefile"
	_ "modernc.org/sqlite"
)

func TestRunAtomic_MissingInvalidOrMismatchedManifestForcesRebuild(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "a.ts", "export const a = 1\n")
	writeFreshnessFile(t, root, ".gitignore", "ignored/\n")
	writeFreshnessFile(t, root, ".cbmignore", "generated/\n")
	writeFreshnessFile(t, root, "tsconfig.base.json", "{}\n")
	writeFreshnessFile(t, root, "package.json", "{}\n")
	writeFreshnessFile(t, root, "package-lock.json", "{}\n")
	writeFreshnessFile(t, root, "go.mod", "module example.test/freshness\ngo 1.26\n")
	writeFreshnessFile(t, root, "go.work", "go 1.26\n\nuse .\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	scipRunAndRead = func(context.Context, string, string) (*scippb.Index, scip.RunStats, error) {
		return &scippb.Index{}, scip.RunStats{}, nil
	}
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	original, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(Manifest) Manifest
	}{
		{name: "schema", mutate: func(m Manifest) Manifest { m.SchemaVersion = "different-schema"; return m }},
		{name: "manifest-version", mutate: func(m Manifest) Manifest { m.ManifestVersion = 999; return m }},
		{name: "analysis", mutate: func(m Manifest) Manifest { m.AnalysisVersion = "different-analysis"; return m }},
		{name: "root", mutate: func(m Manifest) Manifest { m.CanonicalRoot = "/different/root"; return m }},
		{name: "discovery", mutate: func(m Manifest) Manifest { m.DiscoveryRuleVersion = "different-discovery"; return m }},
		{name: "ruby-resolver", mutate: func(m Manifest) Manifest { m.RubyResolverVersion = "different-ruby"; return m }},
		{name: "scip-resolver", mutate: func(m Manifest) Manifest { m.SCIPResolverVersion = "different-scip"; return m }},
		{name: "go-resolver", mutate: func(m Manifest) Manifest { m.GoResolverVersion = "different-go"; return m }},
		{name: "input-fingerprint", mutate: func(m Manifest) Manifest {
			m.Inputs[0].SHA256 = strings.Repeat("0", 64)
			return m
		}},
		{name: "graph-content-digest", mutate: func(m Manifest) Manifest {
			m.GraphContentDigest = strings.Repeat("0", 64)
			return m
		}},
		{name: "legacy-missing-graph-content-digest", mutate: func(m Manifest) Manifest {
			m.GraphContentDigest = ""
			return m
		}},
		{name: "graph-identity", mutate: func(m Manifest) Manifest {
			m.GraphIdentity = "different-graph"
			return m
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := writeRawFreshnessManifest(dbPath, tc.mutate(original)); err != nil {
				t.Fatal(err)
			}
			insertFreshnessSentinel(t, dbPath, root)
			res, err := RunAtomic(dbPath, root)
			if err != nil {
				t.Fatalf("rebuild after %s manifest mismatch: %v", tc.name, err)
			}
			if res.Reused {
				t.Fatalf("%s manifest mismatch incorrectly took no-op path", tc.name)
			}
			assertFreshnessSentinelAbsent(t, dbPath, root)
		})
	}

	if err := os.Remove(ManifestPath(dbPath)); err != nil {
		t.Fatal(err)
	}
	insertFreshnessSentinel(t, dbPath, root)
	if res, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("rebuild after missing manifest: %v", err)
	} else if res.Reused {
		t.Fatal("missing manifest incorrectly took no-op path")
	}
	assertFreshnessSentinelAbsent(t, dbPath, root)

	if err := os.WriteFile(ManifestPath(dbPath), []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	insertFreshnessSentinel(t, dbPath, root)
	if res, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("rebuild after malformed manifest: %v", err)
	} else if res.Reused {
		t.Fatal("malformed manifest incorrectly took no-op path")
	}
	assertFreshnessSentinelAbsent(t, dbPath, root)
}

func TestManifest_FingerprintsRelevantConfigAndLockInputs(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "a.ts", "export const a = 1\n")
	inputs := []string{
		"tsconfig.json", "tsconfig.base.json", "jsconfig.json", "package.json",
		"package-lock.json", "go.mod", "go.work", ".gitignore", ".cbmignore",
		"pnpm-workspace.yaml", "pnpm-workspace.yml", "turbo.json", "nx.json",
		"lerna.json", "rush.json", "workspace.json",
	}
	for _, rel := range inputs {
		content := "initial-" + rel + "\n"
		base := strings.ToLower(filepath.Base(rel))
		if strings.HasPrefix(base, "tsconfig") || base == "jsconfig.json" {
			content = "{}\n"
		}
		if base == "package.json" {
			content = `{"workspaces":["packages/*"]}`
		}
		if base == "pnpm-workspace.yaml" || base == "pnpm-workspace.yml" {
			content = "packages:\n  - apps/*\n  - packages/*\n"
		}
		writeFreshnessFile(t, root, rel, content)
	}
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	scipRunAndRead = func(context.Context, string, string) (*scippb.Index, scip.RunStats, error) {
		return &scippb.Index{}, scip.RunStats{}, nil
	}
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range inputs {
		if !slices.ContainsFunc(manifest.Inputs, func(input InputFingerprint) bool { return input.Path == rel }) {
			t.Errorf("manifest omitted relevant input %q: %+v", rel, manifest.Inputs)
		}
	}
	for _, rel := range inputs {
		t.Run(rel, func(t *testing.T) {
			content := "changed-" + rel + "\n"
			base := strings.ToLower(filepath.Base(rel))
			if strings.HasPrefix(base, "tsconfig") || base == "jsconfig.json" {
				content = "{\"changed\":true}\n"
			}
			if base == "package.json" {
				content = `{"workspaces":["apps/*"]}`
			}
			if base == "pnpm-workspace.yaml" || base == "pnpm-workspace.yml" {
				content = "packages:\n  - changed/*\n"
			}
			writeFreshnessFile(t, root, rel, content)
			res, err := RunAtomic(dbPath, root)
			if err != nil {
				t.Fatalf("index after %s change: %v", rel, err)
			}
			if res.Reused {
				t.Fatalf("changed relevant input %q was incorrectly treated as no-op", rel)
			}
		})
	}
}

func TestManifest_FingerprintsIgnoredWorkspaceTopology(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "main.go", "package main\nfunc main() {}\n")
	writeFreshnessFile(t, root, ".gitignore", "pnpm-workspace.yaml\n")
	writeFreshnessFile(t, root, "pnpm-workspace.yaml", "packages:\n  - packages/*\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(manifest.Inputs, func(input InputFingerprint) bool {
		return input.Path == "pnpm-workspace.yaml"
	}) {
		t.Fatalf("ignored workspace topology was not fingerprinted: %+v", manifest.Inputs)
	}

	writeFreshnessFile(t, root, "pnpm-workspace.yaml", "packages:\n  - apps/*\n")
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("index after ignored topology change: %v", err)
	}
	if res.Reused {
		t.Fatal("ignored workspace topology change incorrectly took no-op path")
	}
}

func TestManifest_FingerprintsSupportedInputInsideSoftIgnoredDirectory(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "main.go", "package main\nfunc main() {}\n")
	writeFreshnessFile(t, root, ".gitignore", "ignored/\n")
	writeFreshnessFile(t, root, "ignored/package.json", `{"name":"before"}`)
	dbPath := filepath.Join(t.TempDir(), "graph.db")

	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(manifest.Inputs, func(input InputFingerprint) bool {
		return input.Path == "ignored/package.json"
	}) {
		t.Fatalf("supported input below soft-ignored directory was not fingerprinted: %+v", manifest.Inputs)
	}

	writeFreshnessFile(t, root, "ignored/package.json", `{"name":"after"}`)
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("index after ignored input change: %v", err)
	}
	if res.Reused {
		t.Fatal("supported input below soft-ignored directory was incorrectly treated as unchanged")
	}
}

func TestRunAtomic_ValidatedObservationCatchesChangesBeforeNoOpOrRebuild(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*testing.T, string)
		change func(*testing.T, string)
		check  func(*testing.T, string, string)
	}{
		{
			name: "source-added",
			setup: func(t *testing.T, root string) {
				writeFreshnessFile(t, root, "main.go", "package main\nfunc main() {}\n")
			},
			change: func(t *testing.T, root string) {
				writeFreshnessFile(t, root, "added.go", "package added\nfunc addedSymbol() {}\n")
			},
			check: func(t *testing.T, dbPath, root string) {
				store, err := graph.Open(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				hits, err := store.Search(ProjectName(root), "addedSymbol", "", 5)
				if err != nil {
					t.Fatal(err)
				}
				if len(hits) != 1 {
					t.Fatalf("source added between observations was omitted from rebuilt graph: hits=%d", len(hits))
				}
			},
		},
		{
			name: "source-mutated",
			setup: func(t *testing.T, root string) {
				writeFreshnessFile(t, root, "main.go", "package main\nfunc oldSymbol() {}\n")
			},
			change: func(t *testing.T, root string) {
				writeFreshnessFile(t, root, "main.go", "package main\nfunc newSymbol() {}\n")
			},
			check: func(t *testing.T, dbPath, root string) {
				store, err := graph.Open(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				oldHits, oldErr := store.Search(ProjectName(root), "oldSymbol", "", 5)
				newHits, newErr := store.Search(ProjectName(root), "newSymbol", "", 5)
				if oldErr != nil || newErr != nil {
					t.Fatalf("mutated source graph search: old=%v new=%v", oldErr, newErr)
				}
				if len(oldHits) != 0 || len(newHits) != 1 {
					t.Fatalf("source mutation between observations was not rebuilt: old=%d new=%d", len(oldHits), len(newHits))
				}
			},
		},
		{
			name: "source-removed",
			setup: func(t *testing.T, root string) {
				writeFreshnessFile(t, root, "keep.go", "package keep\nfunc keepSymbol() {}\n")
				writeFreshnessFile(t, root, "removed.go", "package removed\nfunc removedSymbol() {}\n")
			},
			change: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "removed.go")); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, dbPath, root string) {
				store, err := graph.Open(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				hits, err := store.Search(ProjectName(root), "removedSymbol", "", 5)
				if err != nil {
					t.Fatal(err)
				}
				if len(hits) != 0 {
					t.Fatalf("source removed between observations survived rebuilt graph: hits=%d", len(hits))
				}
			},
		},
		{
			name: "manifest-input-added",
			setup: func(t *testing.T, root string) {
				writeFreshnessFile(t, root, "main.go", "package main\nfunc main() {}\n")
			},
			change: func(t *testing.T, root string) {
				writeFreshnessFile(t, root, "package.json", `{"name":"added-after-scan"}`)
			},
			check: func(t *testing.T, dbPath, _ string) {
				manifest, err := ReadManifest(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.ContainsFunc(manifest.Inputs, func(input InputFingerprint) bool {
					return input.Path == "package.json" && input.SHA256 == hashBytes([]byte(`{"name":"added-after-scan"}`))
				}) {
					t.Fatalf("manifest input added between observations was omitted: %+v", manifest.Inputs)
				}
			},
		},
		{
			name: "manifest-input-mutated",
			setup: func(t *testing.T, root string) {
				writeFreshnessFile(t, root, "main.go", "package main\nfunc main() {}\n")
				writeFreshnessFile(t, root, "package.json", `{"name":"before"}`)
			},
			change: func(t *testing.T, root string) {
				writeFreshnessFile(t, root, "package.json", `{"name":"after"}`)
			},
			check: func(t *testing.T, dbPath, _ string) {
				manifest, err := ReadManifest(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.ContainsFunc(manifest.Inputs, func(input InputFingerprint) bool {
					return input.Path == "package.json" && input.SHA256 == hashBytes([]byte(`{"name":"after"}`))
				}) {
					t.Fatalf("manifest input mutation between observations was omitted: %+v", manifest.Inputs)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			dbPath := filepath.Join(t.TempDir(), "graph.db")
			if _, err := RunAtomic(dbPath, root); err != nil {
				t.Fatalf("initial index: %v", err)
			}

			oldHook := repositoryObservationHook
			var once sync.Once
			repositoryObservationHook = func() {
				once.Do(func() { tc.change(t, root) })
			}
			t.Cleanup(func() { repositoryObservationHook = oldHook })

			res, err := RunAtomic(dbPath, root)
			if err != nil {
				t.Fatalf("index after change between observations: %v", err)
			}
			if res.Reused {
				t.Fatal("change after first observation incorrectly took no-op path")
			}
			tc.check(t, dbPath, root)
		})
	}
}

func TestRunAtomic_StrictNoOpRunsExactIntegrityOnce(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "main.go", "package main\nfunc marker() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}

	oldValidator := validateStoreIntegrity
	t.Cleanup(func() { validateStoreIntegrity = oldValidator })
	calls := 0
	validateStoreIntegrity = func(store *graph.Store) error {
		calls++
		return store.ValidateIntegrity()
	}

	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("strict no-op: %v", err)
	}
	if !res.Reused {
		t.Fatalf("unchanged strict index rebuilt: %+v", res)
	}
	if calls != 1 {
		t.Fatalf("strict no-op ran exact integrity validation %d times, want once", calls)
	}
}

func TestRunAtomic_StrictNoOpDoesNotCertifyMutationBeforeIntegrityGate(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "main.go", "package main\nfunc marker() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	project := ProjectName(root)

	oldHook := freshManifestBeforeIntegrityHook
	t.Cleanup(func() { freshManifestBeforeIntegrityHook = oldHook })
	freshManifestBeforeIntegrityHook = func() {
		// This mutation occurs after the logical digest and immediately before
		// the single exact no-op gate. The gate must reject the no-op and force
		// an independent rebuild rather than certify the stale FTS state.
		deleteFreshnessFTSRow(t, dbPath, project+":main.go.marker")
	}

	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("rebuild after FTS mutation: %v", err)
	}
	if res.Reused {
		t.Fatal("FTS mutation immediately before the integrity gate was certified as a no-op")
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ValidateIntegrity(); err != nil {
		t.Fatalf("rebuilt graph after freshness rejection is not exact-integrity valid: %v", err)
	}
}

func TestRun_NonStrictSkipsPostBuildIntegrityValidation(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "main.go", "package main\nfunc marker() {}\n")
	store, err := graph.Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	oldValidator := validateStoreIntegrity
	t.Cleanup(func() { validateStoreIntegrity = oldValidator })
	calls := 0
	validateStoreIntegrity = func(store *graph.Store) error {
		calls++
		return store.ValidateIntegrity()
	}

	if _, err := Run(store, root); err != nil {
		t.Fatalf("non-strict run: %v", err)
	}
	if calls != 0 {
		t.Fatalf("non-strict post-build path ran exact integrity validation %d times, want zero", calls)
	}
	if err := store.ValidateIntegrity(); err != nil {
		t.Fatalf("non-strict graph failed direct integrity validation: %v", err)
	}
}

func TestRunAtomic_UnstableRepositoryObservationFailsClosed(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "main.go", "package main\nfunc oldSymbol() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}

	oldHook := repositoryObservationHook
	t.Cleanup(func() { repositoryObservationHook = oldHook })
	var generation int
	repositoryObservationHook = func() {
		generation++
		writeFreshnessFile(t, root, "main.go", fmt.Sprintf("package main\nfunc symbol%d() {}\n", generation))
	}
	_, err := RunAtomic(dbPath, root)
	if err == nil || !strings.Contains(err.Error(), "repository observation unstable") {
		t.Fatalf("unstable repository observation error=%v, want bounded fail-closed error", err)
	}
	if generation != repositoryObservationRetries {
		t.Fatalf("validation attempts=%d, want bounded retry count %d", generation, repositoryObservationRetries)
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if hits, err := store.Search(ProjectName(root), "oldSymbol", "", 5); err != nil || len(hits) != 1 {
		t.Fatalf("failed unstable observation changed live graph: hits=%d err=%v", len(hits), err)
	}
}

func TestManifest_SoftIgnoredWalkErrorsAreVisible(t *testing.T) {
	cases := []struct {
		name       string
		rel        string
		ignoreFile string
		isDir      bool
		wantError  bool
	}{
		{name: "soft-ignored-directory", rel: "ignored", ignoreFile: "ignored/", isDir: true, wantError: true},
		{name: "soft-ignored-manifest-input", rel: "ignored/package.json", ignoreFile: "ignored/", wantError: true},
		{name: "hard-ignored-directory", rel: "node_modules", isDir: true},
		{name: "hidden-directory", rel: ".hidden", isDir: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.ignoreFile != "" {
				writeFreshnessFile(t, root, ".gitignore", tc.ignoreFile+"\n")
			}
			path := filepath.Join(root, filepath.FromSlash(tc.rel))
			if tc.isDir {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFreshnessFile(t, root, tc.rel, "{}\n")
			}

			oldWalk := manifestWalkDir
			t.Cleanup(func() { manifestWalkDir = oldWalk })
			manifestWalkDir = func(scanRoot string, walkFn fs.WalkDirFunc) error {
				walkErr := walkFn(filepath.Join(scanRoot, filepath.FromSlash(tc.rel)), nil, errors.New("synthetic walk failure"))
				if errors.Is(walkErr, filepath.SkipDir) {
					return nil
				}
				return walkErr
			}

			_, err := scanRepositoryContext(context.Background(), root)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "synthetic walk failure") {
					t.Fatalf("soft-ignored walk failure was hidden: %v", err)
				}
			} else if err != nil {
				t.Fatalf("hard/hidden pruning returned error: %v", err)
			}
		})
	}
}

func TestManifest_FingerprintsReferencedTSConfigsAndRejectsBrokenReferences(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "root.ts", "export const root = 1\n")
	writeFreshnessFile(t, root, "packages/lib/lib.ts", "export const lib = 1\n")
	writeFreshnessFile(t, root, "tsconfig.json", `{
  // JSONC is accepted because TypeScript configs commonly use it.
  "extends": "./config/base.json",
  "references": [{"path": "./packages/lib"},],
}`)
	writeFreshnessFile(t, root, "config/base.json", `{
  "compilerOptions": {"strict": true,},
}`)
	writeFreshnessFile(t, root, "packages/lib/tsconfig.json", `{}`)
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	scipRunAndRead = func(context.Context, string, string) (*scippb.Index, scip.RunStats, error) {
		return &scippb.Index{}, scip.RunStats{}, nil
	}
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tsconfig.json", "config/base.json", "packages/lib/tsconfig.json"} {
		if !slices.ContainsFunc(manifest.Inputs, func(input InputFingerprint) bool { return input.Path == want }) {
			t.Fatalf("manifest omitted referenced config %q: %+v", want, manifest.Inputs)
		}
	}

	writeFreshnessFile(t, root, "config/base.json", `{"compilerOptions":{"strict":false}}`)
	if res, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("index after extends config change: %v", err)
	} else if res.Reused {
		t.Fatal("extends config change incorrectly took no-op path")
	}
	writeFreshnessFile(t, root, "packages/lib/tsconfig.json", `{"compilerOptions":{"noEmit":true}}`)
	if res, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("index after project reference config change: %v", err)
	} else if res.Reused {
		t.Fatal("project reference config change incorrectly took no-op path")
	}

	writeFreshnessFile(t, root, "tsconfig.json", `{"extends":"./config/missing.json"}`)
	if _, err := RunAtomic(dbPath, root); err == nil || !strings.Contains(err.Error(), "config/missing.json") {
		t.Fatalf("missing referenced config error=%v, want explicit reference failure", err)
	}
	writeFreshnessFile(t, root, "tsconfig.json", `{"extends":"./config/base.json"}`)
	writeFreshnessFile(t, root, "config/base.json", "not json\n")
	if _, err := RunAtomic(dbPath, root); err == nil || !strings.Contains(err.Error(), "config/base.json") {
		t.Fatalf("invalid referenced config error=%v, want explicit parse failure", err)
	}
}

func TestRepositoryInputsRejectSymlinksAtEveryReadBoundary(t *testing.T) {
	root := t.TempDir()
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeFreshnessFile(t, outside, "go.mod", "module outside.test\n")
	writeFreshnessFile(t, outside, "go.work", "go 1.26\n\nuse .\n")
	writeFreshnessFile(t, outside, "base.json", "{}\n")
	if err := os.Symlink(filepath.Join(outside, "go.mod"), filepath.Join(root, "go.mod")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "go.work"), filepath.Join(root, "go.work")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "base.json"), filepath.Join(root, "base.json")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	if hasGoResolverConfig(root) {
		t.Fatal("symlinked go.mod was accepted as resolver configuration")
	}
	if _, err := hashFile(filepath.Join(root, "go.mod")); err == nil || !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("symlinked source hash error=%v, want ErrUnsafePath", err)
	}
	inputs := make(map[string]InputFingerprint)
	if _, err := readTSConfigInput(root, "base.json", inputs); err == nil || !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("symlinked referenced config error=%v, want ErrUnsafePath", err)
	}
	writeFreshnessFile(t, root, "tsconfig.json", `{"extends":"./base.json"}`)
	if _, err := scanRepositoryContext(context.Background(), root); err == nil || !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("symlinked referenced config scan error=%v, want ErrUnsafePath", err)
	}
}

func TestTSConfigDirsRejectsSymlinkedConfig(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFreshnessFile(t, outside, "tsconfig.json", "{}\n")
	if err := os.Symlink(filepath.Join(outside, "tsconfig.json"), filepath.Join(root, "tsconfig.json")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := tsconfigDirsContext(context.Background(), root); err == nil || !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("symlinked tsconfig error=%v, want ErrUnsafePath", err)
	}
}

func TestManifest_WorkspaceMetadataChangeDoesNotReuseStaleTSCalls(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "tsconfig.json", `{}`)
	writeFreshnessFile(t, root, "packages/app/app.ts", "export function caller() {}\nexport function callee() {}\n")
	writeFreshnessFile(t, root, "pnpm-workspace.yaml", "packages:\n  - packages/*\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	scipRunAndRead = func(context.Context, string, string) (*scippb.Index, scip.RunStats, error) {
		return &scippb.Index{}, scip.RunStats{}, nil
	}
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatal(err)
	}
	project := ProjectName(root)
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertEdges([]graph.Edge{{
		Project:  project,
		SourceQN: project + ":packages/app/app.ts.caller",
		TargetQN: project + ":packages/app/app.ts.callee",
		Type:     graph.EdgeCalls,
	}}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	setFreshnessManifestDigestToCurrent(t, dbPath, project)

	writeFreshnessFile(t, root, "pnpm-workspace.yaml", "packages:\n  - apps/*\n")
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reused {
		t.Fatal("workspace metadata change incorrectly took no-op path")
	}
	store, err = graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	neighbors, err := store.Neighbors(project, project+":packages/app/app.ts.caller", "out", string(graph.EdgeCalls), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors) != 0 {
		t.Fatalf("workspace metadata change reused stale TS CALLS: %+v", neighbors)
	}
}

func TestManifest_RejectsHealthyFailedResolverAndNoOpPreservesDegradedStatus(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "go.mod", "module example.test/manifest-status\ngo 1.26\n")
	writeFreshnessFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	oldResolver := goCallEdges
	t.Cleanup(func() { goCallEdges = oldResolver })
	goCallEdges = func(context.Context, string, string, func(string) bool) ([]graph.Edge, error) {
		return nil, errors.New("Go resolver unavailable")
	}
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("initial degraded index: %v", err)
	}
	if res.Status != StatusDegraded || !res.Resolver.HasFailures() {
		t.Fatalf("initial degraded result=%+v", res)
	}
	stored, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusDegraded || !stored.Resolver.HasFailures() {
		t.Fatalf("stored degraded manifest=%+v", stored)
	}
	noOp, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("degraded no-op: %v", err)
	}
	if !noOp.Reused || noOp.Status != StatusDegraded || !noOp.Resolver.HasFailures() {
		t.Fatalf("degraded no-op result=%+v", noOp)
	}

	stored.Status = StatusHealthy
	if err := writeRawFreshnessManifest(dbPath, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(dbPath); err == nil || !strings.Contains(err.Error(), "healthy manifest contains failed resolver scope") {
		t.Fatalf("healthy failed-resolver manifest error=%v", err)
	}
}

func TestRunAtomic_ManifestCommitMismatchForcesFutureRebuild(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "go.mod", "module example.test/manifest-mismatch\ngo 1.26\n")
	writeFreshnessFile(t, root, "main.go", "package main\n\nfunc oldSymbol() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	writeFreshnessFile(t, root, "main.go", "package main\n\nfunc newSymbol() {}\n")
	replaceManifestErr = errors.New("injected manifest install failure")
	t.Cleanup(func() { replaceManifestErr = nil })
	if _, err := RunAtomic(dbPath, root); err == nil {
		t.Fatal("manifest commit failure was not surfaced")
	}
	for _, artifact := range []string{manifestBuildingPath(dbPath), manifestBuildingPath(dbPath) + ".tmp", dbPath + BuildingSuffix} {
		if _, err := os.Stat(artifact); !os.IsNotExist(err) {
			t.Fatalf("manifest failure left artifact %q: %v", artifact, err)
		}
	}
	if _, err := ReadManifest(dbPath); err != nil {
		t.Fatalf("manifest failure damaged the live graph/manifest pair: %v", err)
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	project := ProjectName(root)
	oldHits, oldErr := store.Search(project, "oldSymbol", "", 5)
	newHits, newErr := store.Search(project, "newSymbol", "", 5)
	closeErr := store.Close()
	if oldErr != nil || newErr != nil || closeErr != nil {
		t.Fatalf("live graph after manifest failure: old=%v new=%v close=%v", oldErr, newErr, closeErr)
	}
	if len(oldHits) != 1 || len(newHits) != 0 {
		t.Fatalf("manifest failure changed live graph: old hits=%d new hits=%d", len(oldHits), len(newHits))
	}
	// Once the injected failure is cleared, the next run rebuilds because the
	// repository source changed, rather than taking a false no-op.
	replaceManifestErr = nil
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("rebuild after manifest mismatch: %v", err)
	}
	if res.Reused {
		t.Fatal("graph/manifest commit mismatch incorrectly certified a fresh graph")
	}
}

func TestTSConfigScopes_KeepRootFilesAlongsideChildConfigs(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "tsconfig.json", `{"references":[{"path":"packages/app"}]}`)
	writeFreshnessFile(t, root, "packages/app/tsconfig.json", `{}`)
	writeFreshnessFile(t, root, "root.ts", "export const root = 1\n")
	writeFreshnessFile(t, root, "packages/app/app.ts", "export const app = 1\n")

	dirs, err := tsconfigDirsContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStringSet(dirs, []string{"", "packages/app"}) {
		t.Fatalf("TS scopes=%v, want root plus child scope", dirs)
	}

	store, err := graph.Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	var called []string
	scipRunAndRead = func(_ context.Context, dir, _ string) (*scippb.Index, scip.RunStats, error) {
		called = append(called, dir)
		return nil, scip.RunStats{}, errors.New("resolver unavailable")
	}
	report, err := resolveTSCalls(context.Background(), store, ProjectName(root), root, scip.BuildEnclosingFromSpans(nil), map[string]bool{"": true, "packages/app": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Scopes) != 2 || len(called) != 2 {
		t.Fatalf("root/child resolver report=%+v calls=%v", report, called)
	}
	if slices.Contains(called, root) || slices.Contains(called, filepath.Join(root, "packages/app")) {
		t.Fatalf("resolver received original repository path: %v", called)
	}
	if filepath.Base(called[0]) != filepath.Base(filepath.Dir(filepath.Dir(called[1]))) || filepath.Base(called[1]) != "app" {
		t.Fatalf("resolver did not preserve root/child snapshot scopes: %v", called)
	}
}

func TestPrepareIndexing_ConfigChangeInvalidatesAllTSScopeResolvers(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "root.ts", "export const root = 1\n")
	writeFreshnessFile(t, root, "packages/a/a.ts", "export const a = 1\n")
	writeFreshnessFile(t, root, "packages/b/b.ts", "export const b = 1\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	writeFreshnessFile(t, root, "tsconfig.json", `{}`)
	writeFreshnessFile(t, root, "packages/a/tsconfig.json", `{}`)
	writeFreshnessFile(t, root, "packages/b/tsconfig.json", `{}`)
	writeFreshnessFile(t, root, "package.json", `{"dependencies":{"changed":"1"}}`)

	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	in, reused, err := prepareIndexing(store, root)
	if err != nil {
		t.Fatal(err)
	}
	if reused != nil {
		t.Fatal("TS config/dependency change incorrectly returned no-op")
	}
	for _, scope := range []string{"", "packages/a", "packages/b"} {
		if !in.changed[scope] {
			t.Errorf("scope %q was not invalidated after TS config/dependency change: %v", scope, in.changed)
		}
	}
}

func TestChangedScopesWithTSDependenciesInvalidatesReverseImporters(t *testing.T) {
	root := t.TempDir()
	lib := filepath.Join(root, "packages/lib/lib.ts")
	app := filepath.Join(root, "apps/app/app.ts")
	writeFreshnessFile(t, root, "packages/lib/lib.ts", "export const lib = 1\n")
	writeFreshnessFile(t, root, "apps/app/app.ts", "import { lib } from '../../packages/lib/lib'; export const app = lib\n")
	ch := Changes{
		Changed: []string{"packages/lib/lib.ts"},
		files:   []SourceFile{{AbsPath: lib, RelPath: "packages/lib/lib.ts", Lang: LangTS}, {AbsPath: app, RelPath: "apps/app/app.ts", Lang: LangTS}},
	}
	changed, err := changedScopesWithTSDependencies(context.Background(), ch, []string{"apps/app", "packages/lib"})
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"packages/lib", "apps/app"} {
		if !changed[scope] {
			t.Errorf("dependency scope %q not invalidated: %v", scope, changed)
		}
	}
}

func TestChangedScopesWithTSDependencies_InvalidatesAllTSScopeForUncertainOwnership(t *testing.T) {
	changed, err := changedScopesWithTSDependencies(context.Background(), Changes{
		Changed: []string{"packages/lib/lib.ts"},
	}, []string{"apps/web", "packages/lib", "packages/shared"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed[allTSCallScopesMarker] {
		t.Fatalf("uncertain TS dependency change missing all-TS marker: %v", changed)
	}
	for _, scope := range []string{"apps/web", "packages/lib", "packages/shared"} {
		if !changed[scope] {
			t.Errorf("TS scope %q was not invalidated: %v", scope, changed)
		}
	}
}

func TestRunAtomic_ConfigScopeRemovalDoesNotReuseOldCallEdges(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "tsconfig.json", `{}`)
	writeFreshnessFile(t, root, "packages/app/tsconfig.json", `{}`)
	writeFreshnessFile(t, root, "packages/app/app.ts", "export function caller() {}\nexport function callee() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	scipRunAndRead = func(context.Context, string, string) (*scippb.Index, scip.RunStats, error) {
		return &scippb.Index{}, scip.RunStats{}, nil
	}
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	project := ProjectName(root)
	caller := project + ":packages/app/app.ts.caller"
	callee := project + ":packages/app/app.ts.callee"
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertEdges([]graph.Edge{{
		Project: project, SourceQN: caller, TargetQN: callee, Type: graph.EdgeCalls,
	}}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "packages/app/tsconfig.json")); err != nil {
		t.Fatal(err)
	}
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("index after scope removal: %v", err)
	}
	if res.Reused {
		t.Fatal("scope transition incorrectly took no-op path")
	}
	store, err = graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	neighbors, err := store.Neighbors(project, caller, "out", string(graph.EdgeCalls), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors) != 0 {
		t.Fatalf("removed TS scope reused stale CALLS edge: %+v", neighbors)
	}
}

func TestRunAtomic_ResolverFailurePreservesExistingGraphAndReportsStale(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "go.mod", "module example.test/stale\ngo 1.26\n")
	writeFreshnessFile(t, root, "main.go", "package main\n\nfunc oldSymbol() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	writeFreshnessFile(t, root, "main.go", "package main\n\nfunc newSymbol() {}\n")
	oldResolver := goCallEdges
	t.Cleanup(func() { goCallEdges = oldResolver })
	goCallEdges = func(context.Context, string, string, func(string) bool) ([]graph.Edge, error) {
		return nil, errors.New("go resolver unavailable")
	}
	res, err := RunAtomic(dbPath, root)
	if err == nil {
		t.Fatal("resolver failure unexpectedly committed a healthy replacement")
	}
	var resolverErr *ResolverFailure
	if !errors.As(err, &resolverErr) {
		t.Fatalf("error=%v, want ResolverFailure", err)
	}
	if res.Status != StatusStale || !res.Resolver.HasFailures() {
		t.Fatalf("resolver failure result=%+v", res)
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(root)
	oldHits, err := store.Search(project, "oldSymbol", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	newHits, err := store.Search(project, "newSymbol", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldHits) != 1 || len(newHits) != 0 {
		t.Fatalf("failed resolver replacement changed graph: old=%d new=%d", len(oldHits), len(newHits))
	}
}

func TestRunAtomic_TSResolverFailurePreservesExistingGraph(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "tsconfig.json", `{}`)
	writeFreshnessFile(t, root, "a.ts", "export function oldSymbol() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	scipRunAndRead = func(context.Context, string, string) (*scippb.Index, scip.RunStats, error) {
		return &scippb.Index{}, scip.RunStats{}, nil
	}
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial TS index: %v", err)
	}
	writeFreshnessFile(t, root, "a.ts", "export function newSymbol() {}\n")
	scipRunAndRead = func(context.Context, string, string) (*scippb.Index, scip.RunStats, error) {
		return nil, scip.RunStats{}, errors.New("scip resolver unavailable")
	}
	res, err := RunAtomic(dbPath, root)
	if err == nil {
		t.Fatal("TS resolver failure unexpectedly committed a replacement")
	}
	var resolverErr *ResolverFailure
	if !errors.As(err, &resolverErr) || res.Status != StatusStale {
		t.Fatalf("TS resolver failure result=%+v err=%v", res, err)
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(root)
	oldHits, err := store.Search(project, "oldSymbol", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	newHits, err := store.Search(project, "newSymbol", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldHits) != 1 || len(newHits) != 0 {
		t.Fatalf("failed TS resolver replacement changed graph: old=%d new=%d", len(oldHits), len(newHits))
	}
}

func TestRunAtomic_FirstResolverFailureCommitsExplicitStructuralDegradedGraph(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "go.mod", "module example.test/degraded\ngo 1.26\n")
	writeFreshnessFile(t, root, "main.go", "package main\n\nfunc useful() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	oldResolver := goCallEdges
	t.Cleanup(func() { goCallEdges = oldResolver })
	goCallEdges = func(context.Context, string, string, func(string) bool) ([]graph.Edge, error) {
		return nil, errors.New("go resolver unavailable")
	}
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("first degraded index failed: %v", err)
	}
	if res.Status != StatusDegraded || !res.Resolver.HasFailures() {
		t.Fatalf("degraded result=%+v", res)
	}
	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != StatusDegraded || !manifest.Resolver.HasFailures() {
		t.Fatalf("manifest did not preserve degraded resolver status: %+v", manifest)
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(root)
	if n, _, err := store.Stats(project); err != nil || n == 0 {
		t.Fatalf("structural graph missing: nodes=%d err=%v", n, err)
	}
	if edges, err := store.Neighbors(project, project+":main.go.useful", "out", string(graph.EdgeCalls), 10); err != nil {
		t.Fatal(err)
	} else if len(edges) != 0 {
		t.Fatalf("degraded structural graph contains resolver edges: %+v", edges)
	}
}

func TestRunAtomic_ReadFailuresAreVisible(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
		if entries, err := os.ReadDir(root); err == nil {
			_ = entries
			t.Skip("filesystem privileges bypass unreadable-root fixture")
		}
		_, err := RunAtomic(filepath.Join(t.TempDir(), "graph.db"), root)
		if err == nil || (!strings.Contains(err.Error(), "repository root") && !strings.Contains(err.Error(), "permission denied")) {
			t.Fatalf("unreadable root error=%v", err)
		}
	})

	t.Run("source", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "blocked.go")
		writeFreshnessFile(t, root, "blocked.go", "package blocked\n")
		if err := os.Chmod(source, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(source, 0o600) })
		if f, err := os.Open(source); err == nil {
			_ = f.Close()
			t.Skip("filesystem privileges bypass unreadable-source fixture")
		}
		_, err := RunAtomic(filepath.Join(t.TempDir(), "graph.db"), root)
		if err == nil || !strings.Contains(err.Error(), "blocked.go") {
			t.Fatalf("unreadable source error=%v", err)
		}
	})

	t.Run("config", func(t *testing.T) {
		root := t.TempDir()
		config := filepath.Join(root, "tsconfig.base.json")
		writeFreshnessFile(t, root, "tsconfig.base.json", "{}\n")
		if err := os.Chmod(config, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(config, 0o600) })
		if f, err := os.Open(config); err == nil {
			_ = f.Close()
			t.Skip("filesystem privileges bypass unreadable-config fixture")
		}
		_, err := RunAtomic(filepath.Join(t.TempDir(), "graph.db"), root)
		if err == nil || !strings.Contains(err.Error(), "tsconfig.base.json") {
			t.Fatalf("unreadable config error=%v", err)
		}
	})
}

func TestRunAtomic_SimilarityReadFailurePreservesExistingGraph(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "symbols.go")
	writeFreshnessFile(t, root, "symbols.go", "package symbols\n\nfunc oldSymbol() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	writeFreshnessFile(t, root, "symbols.go", "package symbols\n\nfunc newSymbol() {}\n")
	oldRead := similarReadFile
	oldSimilarPass := similarPassOverride
	t.Cleanup(func() {
		similarReadFile = oldRead
		similarPassOverride = oldSimilarPass
	})
	similarReadFile = func(path string) ([]byte, error) {
		if filepath.Base(path) == filepath.Base(source) {
			return nil, errors.New("similarity source unavailable")
		}
		// #nosec G703 -- test seam delegates to the explicitly supplied source
		// path; production similarity reads are outside this injected failure.
		return os.ReadFile(path)
	}
	similarPassOverride = resolveSimilarFromSpans
	if _, err := RunAtomic(dbPath, root); err == nil || !strings.Contains(err.Error(), "similarity") {
		t.Fatalf("similarity failure error=%v", err)
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(root)
	oldHits, err := store.Search(project, "oldSymbol", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	newHits, err := store.Search(project, "newSymbol", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldHits) != 1 || len(newHits) != 0 {
		t.Fatalf("similarity failure changed live graph: old=%d new=%d", len(oldHits), len(newHits))
	}
}

func TestResolveSimilarFromSpans_RejectsInvalidFunctionSpan(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "symbols.go", "package symbols\n\nfunc one() {}\n")
	_, err := resolveSimilarFromSpans(context.Background(), "project", root, []graph.FunctionSpan{{
		QualifiedName: "project:symbols.go.one",
		FilePath:      "symbols.go",
		StartLine:     2,
		EndLine:       99,
	}})
	if err == nil || !strings.Contains(err.Error(), "function span") {
		t.Fatalf("invalid function span error=%v", err)
	}
}

func TestResolveTSCalls_UsesIsolatedCleanedArtifactsConcurrently(t *testing.T) {
	roots := []string{t.TempDir(), t.TempDir()}
	for _, root := range roots {
		writeFreshnessFile(t, root, "tsconfig.json", `{}`)
	}
	stores := make([]*graph.Store, 0, len(roots))
	for range roots {
		store, err := graph.Open(filepath.Join(t.TempDir(), "graph.db"))
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, store)
	}
	t.Cleanup(func() {
		for _, store := range stores {
			_ = store.Close()
		}
	})
	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	var mu sync.Mutex
	var outputs []string
	scipRunAndRead = func(_ context.Context, _ string, out string) (*scippb.Index, scip.RunStats, error) {
		if err := os.WriteFile(out, nil, 0o600); err != nil {
			return nil, scip.RunStats{}, err
		}
		mu.Lock()
		outputs = append(outputs, out)
		mu.Unlock()
		return &scippb.Index{}, scip.RunStats{}, nil
	}
	var wg sync.WaitGroup
	for i, root := range roots {
		wg.Add(1)
		go func(i int, root string) {
			defer wg.Done()
			if report, err := resolveTSCalls(context.Background(), stores[i], ProjectName(root), root,
				scip.BuildEnclosingFromSpans(nil), map[string]bool{"": true}); err != nil {
				t.Errorf("resolve TS calls %d: %v", i, err)
			} else if report.Scopes[0].Failed {
				t.Errorf("resolve TS calls %d unexpectedly failed: %+v", i, report)
			}
		}(i, root)
	}
	wg.Wait()
	mu.Lock()
	gotOutputs := append([]string(nil), outputs...)
	mu.Unlock()
	if len(gotOutputs) != len(roots) {
		t.Fatalf("SCIP invocations=%v, want %d", gotOutputs, len(roots))
	}
	if filepath.Dir(gotOutputs[0]) == filepath.Dir(gotOutputs[1]) || gotOutputs[0] == gotOutputs[1] {
		t.Fatalf("parallel SCIP invocations shared output artifact: %v", gotOutputs)
	}
	for _, output := range gotOutputs {
		if _, err := os.Stat(filepath.Dir(output)); !os.IsNotExist(err) {
			t.Fatalf("private SCIP directory was not cleaned: %q stat=%v", filepath.Dir(output), err)
		}
	}
}

func TestRunAtomic_RubyResolverStorageFailureCommitsDegradedReport(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "lib/gateway.rb", "class Gateway\n  def self.authorize\n  end\nend\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	oldTargets := rubyCallTargets
	t.Cleanup(func() { rubyCallTargets = oldTargets })
	rubyCallTargets = func(*graph.Store, string) ([]graph.RubyCallTarget, error) {
		return nil, errors.New("Ruby target storage unavailable")
	}
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("Ruby degraded index: %v", err)
	}
	if res.Status != StatusDegraded || !res.Resolver.HasFailures() {
		t.Fatalf("Ruby degraded result=%+v", res)
	}
	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != StatusDegraded || !manifest.Resolver.HasFailures() {
		t.Fatalf("Ruby failure was not persisted in resolver report: %+v", manifest)
	}
}

func TestRunAtomic_RubyAnalysisFreshnessMissDoesNotReuseOldCallEdges(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "go.mod", "module example.test/ruby-freshness\ngo 1.26\n")
	writeFreshnessFile(t, root, "main.go", "package main\n\nfunc caller() {}\nfunc callee() {}\n")
	writeFreshnessFile(t, root, "lib/gateway.rb", "class Gateway\n  def self.authorize\n  end\nend\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")

	oldResolver := goCallEdges
	t.Cleanup(func() { goCallEdges = oldResolver })
	goCallEdges = func(context.Context, string, string, func(string) bool) ([]graph.Edge, error) {
		return nil, nil
	}
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	project := ProjectName(root)
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertEdges([]graph.Edge{{
		Project: project, SourceQN: project + ":main.go.caller", TargetQN: project + ":main.go.callee", Type: graph.EdgeCalls,
	}}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	setFreshnessManifestDigestToCurrent(t, dbPath, project)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`UPDATE nodes SET properties=? WHERE project=? AND label='File' AND file_path=?`,
		`{"lang":"ruby","ruby_analysis_version":0}`, project, "lib/gateway.rb")
	if err == nil {
		if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
			if rowsErr != nil {
				err = rowsErr
			} else {
				err = fmt.Errorf("updated Ruby file rows=%d, want one", count)
			}
		}
	}
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("mark Ruby analysis stale: update=%v close=%v", err, closeErr)
	}
	// Keep the manifest digest aligned with the deliberately stale graph so this
	// regression isolates RubyAnalysisCurrent from the separate digest guard.
	setFreshnessManifestDigestToCurrent(t, dbPath, project)

	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("rebuild after Ruby analysis mismatch: %v", err)
	}
	if res.Reused {
		t.Fatal("Ruby analysis mismatch incorrectly took no-op path")
	}
	store, err = graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	neighbors, err := store.Neighbors(project, project+":main.go.caller", "out", string(graph.EdgeCalls), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors) != 0 {
		t.Fatalf("Ruby analysis freshness miss reused stale CALLS: %+v", neighbors)
	}
}

func TestResolverProducersCheckApplicabilityBeforeReuse(t *testing.T) {
	root := t.TempDir()
	store, err := graph.Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enc := scip.BuildEnclosingFromSpans(nil)

	goEdges, goScope, err := resolveGoCalls(context.Background(), "project", root,
		[]SourceFile{{Lang: LangTS}}, enc, map[string]bool{"go": false})
	if err != nil {
		t.Fatal(err)
	}
	if len(goEdges) != 0 || goScope != (ResolverScopeStatus{}) {
		t.Fatalf("non-Go repository produced resolver scope: edges=%v scope=%+v", goEdges, goScope)
	}
	rubyEdges, rubyScope, err := resolveRubyCalls(context.Background(), store, "project",
		[]SourceFile{{Lang: LangTS}}, enc, map[string]bool{"ruby": false})
	if err != nil {
		t.Fatal(err)
	}
	if len(rubyEdges) != 0 || rubyScope != (ResolverScopeStatus{}) {
		t.Fatalf("non-Ruby repository produced resolver scope: edges=%v scope=%+v", rubyEdges, rubyScope)
	}

	writeFreshnessFile(t, root, "main.go", "package main\nfunc main() {}\n")
	goEdges, goScope, err = resolveGoCalls(context.Background(), "project", root,
		[]SourceFile{{AbsPath: filepath.Join(root, "main.go"), RelPath: "main.go", Lang: LangGo}}, enc,
		map[string]bool{"go": false})
	if err != nil {
		t.Fatal(err)
	}
	if len(goEdges) != 0 || goScope != (ResolverScopeStatus{}) {
		t.Fatalf("Go source without module config produced resolver scope: edges=%v scope=%+v", goEdges, goScope)
	}
	writeFreshnessFile(t, root, "go.mod", "module example.test/applicability\ngo 1.26\n")
	goEdges, goScope, err = resolveGoCalls(context.Background(), "project", root,
		[]SourceFile{{AbsPath: filepath.Join(root, "main.go"), RelPath: "main.go", Lang: LangGo}}, enc,
		map[string]bool{"go": false})
	if err != nil {
		t.Fatal(err)
	}
	if len(goEdges) != 0 || !goScope.Reused || goScope.Resolver != "go-vta" || goScope.Scope != "go" {
		t.Fatalf("applicable unchanged Go scope=%+v edges=%v, want Reused", goScope, goEdges)
	}

	rubyEdges, rubyScope, err = resolveRubyCalls(context.Background(), store, "project",
		[]SourceFile{{AbsPath: filepath.Join(root, "gateway.rb"), RelPath: "gateway.rb", Lang: LangRuby}}, enc,
		map[string]bool{"ruby": false})
	if err != nil {
		t.Fatal(err)
	}
	if len(rubyEdges) != 0 || !rubyScope.Reused || rubyScope.Resolver != "ruby-static" || rubyScope.Scope != "ruby" {
		t.Fatalf("applicable unchanged Ruby scope=%+v edges=%v, want Reused", rubyScope, rubyEdges)
	}
}

func writeFreshnessFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRawFreshnessManifest(dbPath string, manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ManifestPath(dbPath), data, 0o600)
}

func TestRunAtomic_GraphIntegrityAndDigestRejectNoOp(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(t *testing.T, dbPath, project string)
	}{
		{
			name: "node-loss",
			corrupt: func(t *testing.T, dbPath, project string) {
				deleteFreshnessNode(t, dbPath, project+":main.go.callee")
			},
		},
		{
			name: "call-edge-loss",
			corrupt: func(t *testing.T, dbPath, project string) {
				store, err := graph.Open(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.DeleteEdgesByType(project, graph.EdgeCalls); err != nil {
					_ = store.Close()
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "fts-row-loss",
			corrupt: func(t *testing.T, dbPath, project string) {
				deleteFreshnessFTSRow(t, dbPath, project+":main.go.callee")
			},
		},
		{
			name: "fts-content-corruption",
			corrupt: func(t *testing.T, dbPath, project string) {
				replaceFreshnessFTSRow(t, dbPath, project+":main.go.callee")
			},
		},
		{
			name: "edge-project-corruption",
			corrupt: func(t *testing.T, dbPath, project string) {
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec(`UPDATE edges SET project='corrupted-project' WHERE project=? AND type=?`, project, string(graph.EdgeCalls)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "manifest-content-digest-mismatch",
			corrupt: func(t *testing.T, dbPath, _ string) {
				manifest, err := ReadManifest(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				manifest.GraphContentDigest = strings.Repeat("0", 64)
				if err := writeRawFreshnessManifest(dbPath, manifest); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, dbPath, project := prepareFreshnessGraphWithCallEdge(t)
			setFreshnessManifestDigestToCurrent(t, dbPath, project)
			tc.corrupt(t, dbPath, project)

			res, err := RunAtomic(dbPath, root)
			if err != nil {
				t.Fatalf("rebuild after %s: %v", tc.name, err)
			}
			if res.Reused {
				t.Fatalf("%s was incorrectly certified as a fresh no-op", tc.name)
			}
			store, err := graph.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ValidateIntegrity(); err != nil {
				_ = store.Close()
				t.Fatalf("rebuilt graph integrity: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadManifest(dbPath); err != nil {
				t.Fatalf("rebuilt manifest: %v", err)
			}
		})
	}
}

func TestRunAtomic_CorruptGraphIsFreshnessMissAndFailedRebuildPreservesLivePath(t *testing.T) {
	t.Run("graph open failure", func(t *testing.T) {
		root := t.TempDir()
		writeFreshnessFile(t, root, "main.go", "package main\nfunc oldSymbol() {}\n")
		dbPath := filepath.Join(t.TempDir(), "graph.db")
		if _, err := RunAtomic(dbPath, root); err != nil {
			t.Fatal(err)
		}
		corrupted := []byte("not a SQLite database\n")
		if err := os.WriteFile(dbPath, corrupted, 0o600); err != nil {
			t.Fatal(err)
		}
		pipelinePreflightErr = errors.New("injected graph rebuild failure")
		res, err := RunAtomic(dbPath, root)
		pipelinePreflightErr = nil
		if err == nil || !strings.Contains(err.Error(), "injected graph rebuild failure") {
			t.Fatalf("corrupt graph did not reach independent rebuild: result=%+v err=%v", res, err)
		}
		got, readErr := os.ReadFile(dbPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != string(corrupted) {
			t.Fatalf("failed rebuild changed corrupt live path: got %q want %q", got, corrupted)
		}

		if _, err := RunAtomic(dbPath, root); err != nil {
			t.Fatalf("rebuild after clearing corruption: %v", err)
		}
		store, err := graph.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if hits, err := store.Search(ProjectName(root), "oldSymbol", "", 5); err != nil || len(hits) != 1 {
			t.Fatalf("rebuilt graph search: hits=%d err=%v", len(hits), err)
		}
	})

	t.Run("invalid JSON properties", func(t *testing.T) {
		root := t.TempDir()
		writeFreshnessFile(t, root, "main.go", "package main\nfunc oldSymbol() {}\n")
		dbPath := filepath.Join(t.TempDir(), "graph.db")
		if _, err := RunAtomic(dbPath, root); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`UPDATE nodes SET properties='{' WHERE qualified_name=?`, ProjectName(root)+":main.go.oldSymbol")
		closeErr := db.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("corrupt graph JSON: update=%v close=%v", err, closeErr)
		}

		pipelinePreflightErr = errors.New("injected JSON rebuild failure")
		res, err := RunAtomic(dbPath, root)
		pipelinePreflightErr = nil
		if err == nil || !strings.Contains(err.Error(), "injected JSON rebuild failure") {
			t.Fatalf("invalid graph JSON did not reach independent rebuild: result=%+v err=%v", res, err)
		}
		store, err := graph.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if hits, err := store.Search(ProjectName(root), "oldSymbol", "", 5); err != nil || len(hits) != 1 {
			t.Fatalf("live graph after failed JSON rebuild: hits=%d err=%v", len(hits), err)
		}
		if err := store.ValidateIntegrity(); err == nil {
			t.Fatal("corrupt JSON unexpectedly became healthy during failed rebuild")
		}
	})
}

func TestResolverReport_RejectsIncompleteDuplicateAndInconsistentScopes(t *testing.T) {
	cases := []struct {
		name   string
		scopes []ResolverScopeStatus
	}{
		{
			name:   "incomplete",
			scopes: []ResolverScopeStatus{{Resolver: "go-vta", Scope: "go"}},
		},
		{
			name: "duplicate",
			scopes: []ResolverScopeStatus{
				{Resolver: "go-vta", Scope: "go", Reused: true},
				{Resolver: "go-vta", Scope: "go", Reused: true},
			},
		},
		{
			name:   "attempted-and-reused",
			scopes: []ResolverScopeStatus{{Resolver: "go-vta", Scope: "go", Attempted: true, Reused: true}},
		},
		{
			name:   "succeeded-and-failed",
			scopes: []ResolverScopeStatus{{Resolver: "go-vta", Scope: "go", Attempted: true, Succeeded: true, Failed: true, Error: "resolver error"}},
		},
		{
			name:   "attempted-without-outcome",
			scopes: []ResolverScopeStatus{{Resolver: "go-vta", Scope: "go", Attempted: true}},
		},
		{
			name:   "failed-without-error",
			scopes: []ResolverScopeStatus{{Resolver: "go-vta", Scope: "go", Attempted: true, Failed: true}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := (ResolverReport{Scopes: tc.scopes}).Validate(); err == nil {
				t.Fatalf("invalid resolver report was accepted: %+v", tc.scopes)
			}
		})
	}

	if err := (ResolverReport{Scopes: []ResolverScopeStatus{{Resolver: "go-vta", Scope: "go", Reused: true}}}).Validate(); err != nil {
		t.Fatalf("valid reused scope rejected: %v", err)
	}
	if err := (ResolverReport{Scopes: []ResolverScopeStatus{{Resolver: "go-vta", Scope: "go", Attempted: true, Succeeded: true}}}).Validate(); err != nil {
		t.Fatalf("valid successful scope rejected: %v", err)
	}
	if err := (ResolverReport{Scopes: []ResolverScopeStatus{{Resolver: "go-vta", Scope: "go", Attempted: true, Failed: true, Error: "unavailable"}}}).Validate(); err != nil {
		t.Fatalf("valid failed scope rejected: %v", err)
	}
}

func TestRunAtomic_HealthyManifestMissingExpectedTSResolverForcesRebuild(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "tsconfig.json", "{}\n")
	writeFreshnessFile(t, root, "a.ts", "export const a = 1\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	scipRunAndRead = func(context.Context, string, string) (*scippb.Index, scip.RunStats, error) {
		return &scippb.Index{}, scip.RunStats{}, nil
	}
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Resolver.Scopes = nil
	if err := writeRawFreshnessManifest(dbPath, manifest); err != nil {
		t.Fatal(err)
	}
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("rebuild after missing TS resolver scope: %v", err)
	}
	if res.Reused {
		t.Fatal("healthy manifest with missing TS scope incorrectly took no-op path")
	}
}

func TestResolverReport_RequiresExactlyExpectedScopes(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "go.mod", "module example.test/complete\ngo 1.26\n")
	expected := expectedResolverScopeKeys(root, []SourceFile{
		{Lang: LangTS}, {Lang: LangGo}, {Lang: LangRuby},
	}, []string{"", "packages/app"})
	valid := ResolverReport{Scopes: []ResolverScopeStatus{
		{Resolver: "scip-typescript", Scope: "", Reused: true},
		{Resolver: "scip-typescript", Scope: "packages/app", Attempted: true, Succeeded: true},
		{Resolver: "go-vta", Scope: "go", Attempted: true, Succeeded: true},
		{Resolver: "ruby-static", Scope: "ruby", Attempted: true, Succeeded: true},
	}}
	if err := valid.ValidateExpected(expected); err != nil {
		t.Fatalf("complete resolver report rejected: %v", err)
	}
	missing := valid
	missing.Scopes = missing.Scopes[1:]
	if err := missing.ValidateExpected(expected); err == nil {
		t.Fatal("resolver report missing a TS scope was accepted")
	}
	unexpected := valid
	unexpected.Scopes = append(unexpected.Scopes, ResolverScopeStatus{
		Resolver: "scip-typescript", Scope: "removed", Reused: true,
	})
	if err := unexpected.ValidateExpected(expected); err == nil {
		t.Fatal("resolver report with an unexpected scope was accepted")
	}

	sourceOnly := expectedResolverScopeKeys(root, []SourceFile{{Lang: LangTS}}, nil)
	if len(sourceOnly) != 0 {
		t.Fatalf("source-only TS repository created false resolver expectations: %v", sourceOnly)
	}
}

func TestResolverStatus_RejectsHealthyFailuresAndDegradedSuccess(t *testing.T) {
	failed := ResolverReport{Scopes: []ResolverScopeStatus{{
		Resolver: "go-vta", Scope: "go", Attempted: true, Failed: true, Error: "unavailable",
	}}}
	succeeded := ResolverReport{Scopes: []ResolverScopeStatus{{
		Resolver: "go-vta", Scope: "go", Attempted: true, Succeeded: true,
	}}}
	incomplete := ResolverReport{Scopes: []ResolverScopeStatus{{Resolver: "go-vta", Scope: "go"}}}
	for _, tc := range []struct {
		name   string
		status IndexStatus
		report ResolverReport
	}{
		{name: "healthy failure", status: StatusHealthy, report: failed},
		{name: "degraded success", status: StatusDegraded, report: succeeded},
		{name: "healthy incomplete", status: StatusHealthy, report: incomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateResolverStatus(tc.status, tc.report); err == nil {
				t.Fatalf("status mismatch was accepted: status=%s report=%+v", tc.status, tc.report)
			}
		})
	}
}

func TestRunAtomic_CleansStaleManifestArtifactsOnNextStart(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "x.go", "package x\nfunc F() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range []string{manifestBuildingPath(dbPath), manifestBuildingPath(dbPath) + ".tmp", dbPath + BuildingSuffix} {
		if err := os.WriteFile(artifact, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Reused {
		t.Fatalf("clean next start did not take no-op path: %+v", res)
	}
	for _, artifact := range []string{manifestBuildingPath(dbPath), manifestBuildingPath(dbPath) + ".tmp", dbPath + BuildingSuffix} {
		if _, err := os.Stat(artifact); !os.IsNotExist(err) {
			t.Fatalf("stale artifact %q remains: %v", artifact, err)
		}
	}
}

func TestRunAtomic_CanceledBuildCleansManifestArtifactsAndKeepsLiveGraph(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "x.go", "package x\nfunc F() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	ctx, cancel := context.WithCancel(context.Background())
	oldHook := manifestPostWriteHook
	t.Cleanup(func() {
		manifestPostWriteHook = oldHook
		cancel()
	})
	manifestPostWriteHook = func() { cancel() }
	if _, err := RunAtomicContext(ctx, dbPath, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled build error=%v, want context.Canceled", err)
	}
	for _, artifact := range []string{dbPath + BuildingSuffix, manifestBuildingPath(dbPath), manifestBuildingPath(dbPath) + ".tmp"} {
		if _, err := os.Stat(artifact); !os.IsNotExist(err) {
			t.Fatalf("canceled build artifact %q remains: %v", artifact, err)
		}
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("canceled build removed live database: %v", err)
	}
}

func TestRunAtomic_CancelAfterManifestInstallPreservesLiveGraph(t *testing.T) {
	root := t.TempDir()
	writeFreshnessFile(t, root, "x.go", "package x\nfunc oldSymbol() {}\n")
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	writeFreshnessFile(t, root, "x.go", "package x\nfunc newSymbol() {}\n")

	ctx, cancel := context.WithCancel(context.Background())
	oldHook := manifestPostReplaceHook
	t.Cleanup(func() {
		manifestPostReplaceHook = oldHook
		cancel()
	})
	manifestPostReplaceHook = func() { cancel() }
	if _, err := RunAtomicContext(ctx, dbPath, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled post-manifest build error=%v, want context.Canceled", err)
	}

	for _, artifact := range []string{dbPath + BuildingSuffix, manifestBuildingPath(dbPath), manifestBuildingPath(dbPath) + ".tmp"} {
		if _, err := os.Stat(artifact); !os.IsNotExist(err) {
			t.Fatalf("canceled post-manifest artifact %q remains: %v", artifact, err)
		}
	}
	if _, err := ReadManifest(dbPath); err == nil {
		t.Fatal("canceled post-manifest build left a fresh graph/manifest pair")
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	project := ProjectName(root)
	oldHits, oldErr := store.Search(project, "oldSymbol", "", 5)
	newHits, newErr := store.Search(project, "newSymbol", "", 5)
	closeErr := store.Close()
	if oldErr != nil || newErr != nil || closeErr != nil {
		t.Fatalf("live graph after post-manifest cancellation: old=%v new=%v close=%v", oldErr, newErr, closeErr)
	}
	if len(oldHits) != 1 || len(newHits) != 0 {
		t.Fatalf("post-manifest cancellation changed live graph: old hits=%d new hits=%d", len(oldHits), len(newHits))
	}
}

func prepareFreshnessGraphWithCallEdge(t *testing.T) (root, dbPath, project string) {
	t.Helper()
	root = t.TempDir()
	writeFreshnessFile(t, root, "main.go", "package main\n\nfunc caller() {}\nfunc callee() {}\n")
	dbPath = filepath.Join(t.TempDir(), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatal(err)
	}
	project = ProjectName(root)
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertEdges([]graph.Edge{{
		Project: project, SourceQN: project + ":main.go.caller", TargetQN: project + ":main.go.callee", Type: graph.EdgeCalls,
	}}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return root, dbPath, project
}

func setFreshnessManifestDigestToCurrent(t *testing.T, dbPath, project string) {
	t.Helper()
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.LogicalGraphDigest(project)
	closeErr := store.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.GraphContentDigest = digest
	if err := writeRawFreshnessManifest(dbPath, manifest); err != nil {
		t.Fatal(err)
	}
}

func deleteFreshnessNode(t *testing.T, dbPath, qualifiedName string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM nodes WHERE qualified_name=?`, qualifiedName); err != nil {
		t.Fatal(err)
	}
}

func deleteFreshnessFTSRow(t *testing.T, dbPath, qualifiedName string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO nodes_fts(nodes_fts, rowid, name, qualified_name, label, file_path)
		SELECT 'delete', id, name, qualified_name, label, file_path
		FROM nodes WHERE qualified_name=?`, qualifiedName); err != nil {
		t.Fatal(err)
	}
}

func replaceFreshnessFTSRow(t *testing.T, dbPath, qualifiedName string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO nodes_fts(nodes_fts, rowid, name, qualified_name, label, file_path)
		SELECT 'delete', id, name, qualified_name, label, file_path
		FROM nodes WHERE qualified_name=?`, qualifiedName); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO nodes_fts(rowid,name,qualified_name,label,file_path)
		SELECT id, ?, ?, label, file_path
		FROM nodes WHERE qualified_name=?`, "corrupted", qualifiedName+".corrupted", qualifiedName); err != nil {
		t.Fatal(err)
	}
}

func insertFreshnessSentinel(t *testing.T, dbPath, root string) {
	t.Helper()
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(root)
	if err := store.InsertNodes([]graph.Node{{
		Project: project, Label: "Sentinel", Name: "sentinel", QualifiedName: project + ":__freshness_sentinel__",
	}}); err != nil {
		t.Fatal(err)
	}
}

func assertFreshnessSentinelAbsent(t *testing.T, dbPath, root string) {
	t.Helper()
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hits, err := store.Search(ProjectName(root), "sentinel", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("freshness sentinel survived rebuild: %+v", hits)
	}
}

func sameStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, value := range got {
		if !slices.Contains(want, value) {
			return false
		}
	}
	return true
}
