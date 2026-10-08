package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

func TestResolverInputs_InstalledDependencyBytesInvalidateObservation(t *testing.T) {
	for _, location := range []string{"node_modules/local", "packages/app/node_modules/local", "vendor/local"} {
		t.Run(location, func(t *testing.T) {
			root := securityPhysicalTempDir(t)
			writeSecurityFile(t, root, "packages/app/tsconfig.json", "{}")
			writeSecurityFile(t, root, "packages/app/main.ts", "export function run() {}\n")
			writeSecurityFile(t, root, location+"/index.d.ts", "export declare function first(): void;\n")
			before, err := scanRepositoryContext(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			writeSecurityFile(t, root, location+"/index.d.ts", "export declare function other(): void;\n")
			after, err := scanRepositoryContext(context.Background(), root)
			if err != nil || sameRepositoryScan(before, after) {
				t.Fatalf("dependency edit was not observed: err=%v", err)
			}
		})
	}
}

func TestResolverInputs_NestedDependenciesAreStagedFromObservedBytes(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "packages/app/tsconfig.json", "{}")
	writeSecurityFile(t, root, "packages/app/main.ts", "export function run() {}\n")
	const dependency = "packages/app/node_modules/local/index.d.ts"
	const original = "export declare function first(): void;\n"
	writeSecurityFile(t, root, dependency, original)
	scan, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeSecurityFile(t, root, "packages/app/node_modules/local/added.d.ts", "export declare function added(): void;\n")
	snapshot, _, cleanup, err := resolverSnapshotForScan(context.Background(), scan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
	bytes, err := os.ReadFile(filepath.Join(snapshot, dependency))
	if err != nil || string(bytes) != original {
		t.Fatalf("staged dependency=%q err=%v, want observed bytes", bytes, err)
	}
	if _, err := os.Lstat(filepath.Join(snapshot, "packages/app/node_modules/local/added.d.ts")); !os.IsNotExist(err) {
		t.Fatalf("snapshot included an unobserved entry: %v", err)
	}
}

func TestResolverInputs_DependencyMutationBeforeStagingFails(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "tsconfig.json", "{}")
	writeSecurityFile(t, root, "main.ts", "export function run() {}\n")
	writeSecurityFile(t, root, "node_modules/local/index.d.ts", "export declare function first(): void;\n")
	scan, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeSecurityFile(t, root, "node_modules/local/index.d.ts", "export declare function other(): void;\n")
	_, _, cleanup, err := resolverSnapshotForScan(context.Background(), scan)
	if cleanup != nil {
		t.Cleanup(func() {
			if err := cleanup(); err != nil {
				t.Error(err)
			}
		})
	}
	if err == nil || !strings.Contains(err.Error(), "node_modules/local/index.d.ts") || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed dependency staging error=%v", err)
	}
}

func TestResolverInputs_LocalCgoAuxiliaryFilesProduceHealthyCalls(t *testing.T) {
	t.Setenv("CGO_ENABLED", "1")
	t.Setenv("GOFLAGS", "")
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "go.mod", "module example.test/cgo-inputs\ngo 1.26\n")
	writeSecurityFile(t, root, "main.go", "package inputs\n/*\n#include \"thing.h\"\n*/\nimport \"C\"\nfunc Run() { helper(); C.thing() }\nfunc helper() {}\n")
	writeSecurityFile(t, root, "thing.h", "int thing(void);\n")
	writeSecurityFile(t, root, "thing.c", "int thing(void) { return 42; }\n")
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	result, err := RunAtomic(dbPath, root)
	if err != nil || result.Status != StatusHealthy {
		t.Fatalf("cgo result=%+v err=%v, want healthy index with local C/header inputs", result, err)
	}
	assertStoredCallTargets(t, dbPath, ProjectName(root), "main.go.Run", []string{"main.go.helper"})
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if hits, err := store.Search(ProjectName(root), "thing", "File", 10); err != nil || len(hits) != 0 {
		t.Fatalf("auxiliary inputs became source nodes: %+v err=%v", hits, err)
	}
}
