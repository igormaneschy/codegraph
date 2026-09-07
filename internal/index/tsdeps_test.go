package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

// writeTSMonoFixture builds a single-scope-dependency monorepo: shared (no
// deps), app-a (references shared, paths-maps it, imports it), app-b
// (independent). No node_modules: scip runs offline.
func writeTSMonoFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	pkg := func(dir, name, tsconfig string, files map[string]string) {
		d := filepath.Join(root, filepath.FromSlash(dir))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "package.json"), []byte(`{"name":`+name+`,"version":"1.0.0"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "tsconfig.json"), []byte(tsconfig), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	pkg("packages/shared", `"shared"`,
		`{"compilerOptions":{"target":"es2020","module":"commonjs","strict":true,"composite":true},"include":["*.ts"]}`,
		map[string]string{
			"util.ts":  "export function sharedUtil(x: number): number { return x*2; }\nexport function sharedFmt(s: string): string { return s.trim(); }\n",
			"types.ts": "export interface SharedOpts { verbose?: boolean; }\nexport function describe(o: SharedOpts): string { return o.verbose ? \"v\" : \"q\"; }\n",
		})
	pkg("packages/app-a", `"app-a"`,
		`{"compilerOptions":{"target":"es2020","module":"commonjs","strict":true,"paths":{"shared":["../shared/*.ts"]}},"include":["*.ts"],"references":[{"path":"../shared"}]}`,
		map[string]string{
			"main.ts": "import { sharedUtil } from 'shared';\nimport { describe } from '../shared/types';\nexport function runA(x: number){ return sharedUtil(x)+1; }\nexport function tell(v: boolean){ return describe({verbose: v}); }\n",
		})
	pkg("packages/app-b", `"app-b"`,
		`{"compilerOptions":{"target":"es2020","module":"commonjs","strict":true},"include":["*.ts"]}`,
		map[string]string{
			"indep.ts": "export function indepB(x: string): string { return x.toUpperCase(); }\nexport function indepB2(x: string): number { return x.length; }\n",
		})
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"mono","private":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func indexTSMono(t *testing.T, root string) (string, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatalf("index: %v", err)
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return dbPath, ProjectName(root)
}

func tsInputs(t *testing.T, root string, rels ...string) map[string]InputFingerprint {
	t.Helper()
	out := map[string]InputFingerprint{}
	for _, rel := range rels {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		out[rel] = InputFingerprint{Path: rel, SHA256: hashBytes(data)}
	}
	return out
}

var tsMonoDirs = []string{"packages/shared", "packages/app-a", "packages/app-b"}

var tsMonoConfigs = []string{
	"packages/shared/tsconfig.json",
	"packages/app-a/tsconfig.json",
	"packages/app-b/tsconfig.json",
}

// TestTSReferenceGraph_ModelsDependents pins the ownership model: app-a
// references shared, so shared's dependents are {shared, app-a} — never the
// independent app-b.
func TestTSReferenceGraph_ModelsDependents(t *testing.T) {
	root := writeTSMonoFixture(t)
	g, err := tsReferenceGraph(root, tsMonoDirs, tsInputs(t, root, tsMonoConfigs...))
	if err != nil {
		t.Fatalf("reference graph: %v", err)
	}
	if len(g["packages/app-a"]) != 1 || g["packages/app-a"][0] != "packages/shared" {
		t.Errorf("app-a references = %v, want [packages/shared]", g["packages/app-a"])
	}
	if len(g["packages/shared"]) != 0 || len(g["packages/app-b"]) != 0 {
		t.Errorf("shared/app-b must reference nothing: %v", g)
	}
	deps := tsDependents(g, "packages/shared")
	if !deps["packages/shared"] || !deps["packages/app-a"] || deps["packages/app-b"] {
		t.Errorf("shared dependents = %v, want {shared, app-a}", deps)
	}
	if indep := tsDependents(g, "packages/app-b"); len(indep) != 1 || !indep["packages/app-b"] {
		t.Errorf("app-b dependents = %v, want exactly itself", indep)
	}
}

// TestTSSelective_IndependentEditReuses rest of the monorepo: editing only the
// independent app-b invalidates exactly app-b — no all-TS marker, no
// shared/app-a rerun.
func TestTSSelective_IndependentEditReuses(t *testing.T) {
	root := writeTSMonoFixture(t)
	dbPath, project := indexTSMono(t, root)
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	changed, err := changedScopesWithTSDependencies(context.Background(), store, project, root,
		tsInputs(t, root, tsMonoConfigs...),
		Changes{Changed: []string{"packages/app-b/indep.ts"}}, tsMonoDirs)
	if err != nil {
		t.Fatal(err)
	}
	if changed[allTSCallScopesMarker] {
		t.Errorf("independent edit must not set the all-TS marker: %v", changed)
	}
	for scope, want := range map[string]bool{"packages/app-b": true, "packages/shared": false, "packages/app-a": false} {
		if changed[scope] != want {
			t.Errorf("scope %q changed=%v, want %v (full map %v)", scope, changed[scope], want, changed)
		}
	}
}

// TestTSSelective_DependencyEditInvalidatesDownstream pins the other
// direction: editing shared invalidates shared + its reference dependent
// app-a (which imports it), but still reuses the independent app-b.
func TestTSSelective_DependencyEditInvalidatesDownstream(t *testing.T) {
	root := writeTSMonoFixture(t)
	dbPath, project := indexTSMono(t, root)
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	changed, err := changedScopesWithTSDependencies(context.Background(), store, project, root,
		tsInputs(t, root, tsMonoConfigs...),
		Changes{Changed: []string{"packages/shared/util.ts"}}, tsMonoDirs)
	if err != nil {
		t.Fatal(err)
	}
	if changed[allTSCallScopesMarker] {
		t.Errorf("dependency edit must stay selective, got marker: %v", changed)
	}
	for scope, want := range map[string]bool{"packages/shared": true, "packages/app-a": true, "packages/app-b": false} {
		if changed[scope] != want {
			t.Errorf("scope %q changed=%v, want %v (full map %v)", scope, changed[scope], want, changed)
		}
	}
}

// TestTSSelective_UnverifiableConfigFallsBack pins fail-closed modeling: a
// tsconfig changed since observation (hash mismatch) invalidates everything,
// exactly like before.
func TestTSSelective_UnverifiableConfigFallsBack(t *testing.T) {
	root := writeTSMonoFixture(t)
	dbPath, project := indexTSMono(t, root)
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	inputs := tsInputs(t, root, tsMonoConfigs...)
	inputs["packages/app-a/tsconfig.json"] = InputFingerprint{
		Path: "packages/app-a/tsconfig.json", SHA256: "deadbeef",
	}
	changed, err := changedScopesWithTSDependencies(context.Background(), store, project, root,
		inputs, Changes{Changed: []string{"packages/app-b/indep.ts"}}, tsMonoDirs)
	if err != nil {
		t.Fatal(err)
	}
	if !changed[allTSCallScopesMarker] {
		t.Fatalf("unverifiable config must fall back to all-TS: %v", changed)
	}
}

// digestOf opens dbPath and returns the committed logical-graph digest.
func digestOf(t *testing.T, dbPath, project string) string {
	t.Helper()
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	digest, err := store.LogicalGraphDigest(project)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

// assertDigestEquivalent indexes root fresh, applies edit, incrementally
// re-indexes (digest D1), then wipes and fully rebuilds (digest D2): selective
// reuse is correct iff D1 == D2 — the same logical graph, never IDs.
func assertDigestEquivalent(t *testing.T, root, rel, body string) {
	t.Helper()
	project := ProjectName(root)
	dbA := filepath.Join(t.TempDir(), "a.db")
	if _, err := RunAtomic(dbA, root); err != nil {
		t.Fatalf("fresh index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := RunAtomic(dbA, root)
	if err != nil {
		t.Fatalf("incremental index: %v", err)
	}
	d1 := digestOf(t, dbA, project)
	dbB := filepath.Join(t.TempDir(), "b.db")
	if _, err := RunAtomic(dbB, root); err != nil {
		t.Fatalf("rebuild index: %v", err)
	}
	d2 := digestOf(t, dbB, project)
	if d1 != d2 {
		t.Errorf("incremental digest %q != rebuild digest %q after editing %s (reused=%v)", d1, d2, rel, res.Reused)
	}
	if res.Reused {
		t.Errorf("edit of %s certified a no-op", rel)
	}
}

// TestTSSelective_DigestEquivalentAcrossEdits is P7's acceptance core: for an
// independent edit, a dependent edit, and a shared-type edit, the selective
// CALLS set equals the full-rebuild reference — no caller differs.
func TestTSSelective_DigestEquivalentAcrossEdits(t *testing.T) {
	const indepBody = "export function indepB(x: string): string { return x.toUpperCase() + \"!\"; }\nexport function indepB2(x: string): number { return x.length; }\nexport function indepB3(x: string): number { return x.length + 1; }\n"
	const depBody = "import { sharedUtil } from 'shared';\nimport { describe } from '../shared/types';\nexport function runA(x: number){ return sharedUtil(x)+2; }\nexport function tell(v: boolean){ return describe({verbose: v}); }\n"
	const sharedBody = "export function sharedUtil(x: number): number { return x*3; }\nexport function sharedFmt(s: string): string { return s.trim(); }\n"
	const typesBody = "export interface SharedOpts { verbose?: boolean; debug?: boolean; }\nexport function describe(o: SharedOpts): string { return o.verbose ? \"v\" : \"q\"; }\n"

	t.Run("independent", func(t *testing.T) {
		assertDigestEquivalent(t, writeTSMonoFixture(t), "packages/app-b/indep.ts", indepBody)
	})
	t.Run("dependent", func(t *testing.T) {
		assertDigestEquivalent(t, writeTSMonoFixture(t), "packages/app-a/main.ts", depBody)
	})
	t.Run("shared", func(t *testing.T) {
		assertDigestEquivalent(t, writeTSMonoFixture(t), "packages/shared/util.ts", sharedBody)
	})
	t.Run("shared-types", func(t *testing.T) {
		assertDigestEquivalent(t, writeTSMonoFixture(t), "packages/shared/types.ts", typesBody)
	})
}
