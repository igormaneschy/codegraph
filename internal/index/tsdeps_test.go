//go:build integration

package index

import (
	"os"
	"path/filepath"
	"testing"
)

// These oracles require the real SCIP TypeScript resolver (Node/npx). Unit
// invalidation tests remain deterministic and never invoke an external tool.
func TestTSInvalidation_RealResolverMatchesRebuildWithExpectedCalls(t *testing.T) {
	for _, scenario := range []string{"transitive reexport", "alias without references", "add-only resolution shadowing", "delete-only resolution shadowing"} {
		t.Run(scenario, func(t *testing.T) {
			root := writeTSBindingFixture(t, scenario)
			project := ProjectName(root)
			dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
			indexHealthyTSBindings(t, dbPath, root)
			assertStoredCallTargets(t, dbPath, project, "app/a/main.ts.run", []string{"app/c/impl.ts.left"})
			changeTSBinding(t, root, scenario)
			indexHealthyTSBindings(t, dbPath, root)
			assertStoredCallTargets(t, dbPath, project, "app/a/main.ts.run", []string{"app/c/impl.ts.right"})
			freshDB := filepath.Join(securityPhysicalTempDir(t), "fresh.db")
			indexHealthyTSBindings(t, freshDB, root)
			assertStoredCallTargets(t, freshDB, project, "app/a/main.ts.run", []string{"app/c/impl.ts.right"})
			if digestOf(t, dbPath, project) != digestOf(t, freshDB, project) {
				t.Fatal("incremental graph differs from full rebuild")
			}
		})
	}
}

func writeTSBindingFixture(t *testing.T, scenario string) string {
	t.Helper()
	repository := securityPhysicalTempDir(t)
	root := filepath.Join(repository, "app")
	writeSecurityFile(t, root, "package.json", `{"name":"binding-oracle","version":"1.0.0"}`)
	config := `{"compilerOptions":{"target":"es2020","module":"commonjs","strict":true},"include":["*.ts"]}`
	// One package owns the canonical symbol paths. Its program imports a
	// transitive chain across nested tsconfig scopes without project references.
	writeSecurityFile(t, root, "tsconfig.json", `{"compilerOptions":{"target":"es2020","module":"commonjs","strict":true},"include":["a/*.ts"]}`)
	for _, dir := range []string{"b", "c"} {
		writeSecurityFile(t, root, dir+"/tsconfig.json", config)
	}
	writeSecurityFile(t, root, "a/main.ts", "import { chosen } from '../b/barrel';\nexport function run() { return chosen(); }\n")
	writeSecurityFile(t, root, "b/barrel.ts", "export { chosen } from '../c/choice';\n")
	writeSecurityFile(t, root, "c/impl.ts", "export function left() { return 1; }\nexport function right() { return 2; }\n")
	writeSecurityFile(t, root, "c/choice.ts", "export { left as chosen } from './impl';\n")
	configureTSBindingScenario(t, root, scenario)
	return repository
}

func configureTSBindingScenario(t *testing.T, root, scenario string) {
	t.Helper()
	switch scenario {
	case "alias without references":
		writeSecurityFile(t, root, "tsconfig.json", `{"compilerOptions":{"target":"es2020","module":"commonjs","strict":true,"paths":{"choice":["./c/choice.ts"]}},"include":["a/*.ts"]}`)
		writeSecurityFile(t, root, "a/main.ts", "import { chosen } from 'choice';\nexport function run() { return chosen(); }\n")
	case "add-only resolution shadowing":
		if err := os.Remove(filepath.Join(root, "c/choice.ts")); err != nil {
			t.Fatal(err)
		}
		writeSecurityFile(t, root, "c/choice/index.ts", "export { left as chosen } from '../impl';\n")
	case "delete-only resolution shadowing":
		writeSecurityFile(t, root, "c/choice/index.ts", "export { right as chosen } from '../impl';\n")
	}
}

func changeTSBinding(t *testing.T, root, scenario string) {
	t.Helper()
	root = filepath.Join(root, "app")
	if scenario == "delete-only resolution shadowing" {
		if err := os.Remove(filepath.Join(root, "c/choice.ts")); err != nil {
			t.Fatal(err)
		}
		return
	}
	writeSecurityFile(t, root, "c/choice.ts", "export { right as chosen } from './impl';\n")
}

func indexHealthyTSBindings(t *testing.T, dbPath, root string) {
	t.Helper()
	result, err := RunAtomic(dbPath, root)
	if err != nil || result.Status != StatusHealthy || result.Reused || len(result.Resolver.Scopes) != 3 {
		t.Fatalf("TS oracle index=%+v err=%v, want healthy resolution of three scopes", result, err)
	}
	for _, scope := range result.Resolver.Scopes {
		if !scope.Attempted || !scope.Succeeded || scope.Reused || scope.Failed {
			t.Errorf("TS oracle scope was not resolved: %+v", scope)
		}
	}
}
