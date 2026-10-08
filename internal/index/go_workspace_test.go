package index

import (
	"path/filepath"
	"testing"
)

// TestGoWorkspace_ResolvesCrossModuleCalls pins R08 end-to-end through RunAtomic:
// a go.work workspace without a root module must index healthy (not degraded) and
// resolve both intra- and cross-module CALLS, with the workspace inputs left
// uncertified for reuse.
func TestGoWorkspace_ResolvesCrossModuleCalls(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "go.work", "go 1.26\n\nuse (\n\t./a\n\t./b\n)\n")
	writeSecurityFile(t, root, "a/go.mod", "module example.test/a\n\ngo 1.26\n")
	writeSecurityFile(t, root, "a/a.go", "package a\n\nfunc A() { helperA() }\n\nfunc helperA() {}\n")
	writeSecurityFile(t, root, "b/go.mod", "module example.test/b\n\ngo 1.26\n\nrequire example.test/a v0.0.0\n")
	writeSecurityFile(t, root, "b/b.go", "package b\n\nimport \"example.test/a\"\n\nfunc B() { a.A() }\n")

	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	result, err := RunAtomic(dbPath, root)
	if err != nil {
		t.Fatalf("workspace index: %v", err)
	}
	if result.Status != StatusHealthy || result.Resolver.HasFailures() {
		t.Fatalf("workspace index result=%+v, want healthy", result)
	}
	if result.Reused {
		t.Fatal("workspace inputs must not be certified for reuse")
	}
	project := ProjectName(root)
	assertStoredCallTargets(t, dbPath, project, "b/b.go.B", []string{"a/a.go.A"})
	assertStoredCallTargets(t, dbPath, project, "a/a.go.A", []string{"a/a.go.helperA"})
}
