package index

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

func TestGoEnvironment_TagChangeInvalidatesNoOpAndMatchesRebuild(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	root := writeGoBuildTagFixture(t)
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	initial, err := RunAtomic(dbPath, root)
	if err != nil || initial.Status != StatusHealthy {
		t.Fatalf("initial=%+v err=%v", initial, err)
	}
	project := ProjectName(root)
	assertStoredCallTargets(t, dbPath, project, "common.go.Run", []string{"default.go.selected"})
	t.Setenv("GOFLAGS", "-tags=alternate")
	changed, err := RunAtomic(dbPath, root)
	if err != nil || changed.Reused || changed.Status != StatusHealthy {
		t.Fatalf("tag transition=%+v err=%v, want healthy rebuild", changed, err)
	}
	assertStoredCallTargets(t, dbPath, project, "common.go.Run", []string{"alternate.go.selected"})
	assertStoredCallTargets(t, dbPath, project, "alternate.go.selected", []string{"common.go.right"})
	freshDB := filepath.Join(securityPhysicalTempDir(t), "fresh.db")
	if fresh, err := RunAtomic(freshDB, root); err != nil || fresh.Status != StatusHealthy {
		t.Fatalf("reference=%+v err=%v", fresh, err)
	}
	if digestOf(t, dbPath, project) != digestOf(t, freshDB, project) {
		t.Fatal("tag transition differs from full rebuild")
	}
	if noOp, err := RunAtomic(dbPath, root); err != nil || !noOp.Reused {
		t.Fatalf("unchanged certified environment must reuse: %+v err=%v", noOp, err)
	}
}

func writeGoBuildTagFixture(t *testing.T) string {
	t.Helper()
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "go.mod", "module example.test/environment\ngo 1.26\n")
	writeSecurityFile(t, root, "common.go", "package environment\nfunc Run() { selected() }\nfunc left() {}\nfunc right() {}\n")
	writeSecurityFile(t, root, "default.go", "//go:build !alternate\n\npackage environment\nfunc selected() { left() }\n")
	writeSecurityFile(t, root, "alternate.go", "//go:build alternate\n\npackage environment\nfunc selected() { right() }\n")
	return root
}

func TestGoEnvironment_FrozenValuesSurviveLateProcessMutation(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	root := writeGoBuildTagFixture(t)
	original := goCallEdges
	t.Cleanup(func() { goCallEdges = original })
	goCallEdges = func(ctx context.Context, project, root string, known func(string) bool, environment []string) ([]graph.Edge, error) {
		t.Setenv("GOFLAGS", "-tags=alternate")
		return original(ctx, project, root, known, environment)
	}
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	result, err := RunAtomic(dbPath, root)
	if err != nil || result.Status != StatusHealthy {
		t.Fatalf("frozen environment result=%+v err=%v", result, err)
	}
	assertStoredCallTargets(t, dbPath, ProjectName(root), "common.go.Run", []string{"default.go.selected"})
}

func TestGoEnvironment_IdentityChangesWithoutPersistingEffectiveValues(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOPROXY", "https://fixture-user:fixture-private-token@example.invalid")
	root := writeGoBuildTagFixture(t)
	before, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(before.manifest)
	if err != nil || strings.Contains(string(encoded), "fixture-private-token") || !validSHA256(before.manifest.GoEnvironment.Digest) {
		t.Fatalf("environment identity must persist only a valid digest: err=%v", err)
	}
	t.Setenv("CGO_ENABLED", "0")
	first, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CGO_ENABLED", "1")
	second, err := scanRepositoryContext(context.Background(), root)
	if err != nil || sameManifestFingerprint(first.manifest, second.manifest) {
		t.Fatalf("CGO environment transition did not invalidate: %v", err)
	}
}

func TestGoEnvironment_ModifiedGlobalConfigInvalidatesIdentity(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	root := writeGoBuildTagFixture(t)
	config := filepath.Join(securityPhysicalTempDir(t), "go-config")
	t.Setenv("GOENV", config)
	if err := os.WriteFile(config, []byte("GOPRIVATE=first.example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("GOPRIVATE=other.example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := scanRepositoryContext(context.Background(), root)
	if err != nil || sameManifestFingerprint(before.manifest, after.manifest) {
		t.Fatalf("persistent go configuration edit was not observed: %v", err)
	}
}

func TestGoEnvironment_UnobservedExternalDependenciesCannotCertifyReuse(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "go.mod", "module example.test/external\ngo 1.26\nrequire example.test/unobserved v1.0.0\n")
	// The dependency is imported, so its bytes are a real build input; an
	// offline cache cannot enumerate them and reuse stays blocked. (A require
	// that is never imported no longer blocks: the per-package enumeration
	// proves it is not a build input; see the probe-fallback test.)
	writeSecurityFile(t, root, "main.go", "package external\n\nimport _ \"example.test/unobserved\"\n\nfunc Run() {}\n")
	original := goCallEdges
	t.Cleanup(func() { goCallEdges = original })
	invocations := 0
	goCallEdges = func(context.Context, string, string, func(string) bool, []string) ([]graph.Edge, error) {
		invocations++
		return nil, nil
	}
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	for round := 1; round <= 2; round++ {
		result, err := RunAtomic(dbPath, root)
		if err != nil || result.Reused || result.Status != StatusHealthy || invocations != round {
			t.Fatalf("round=%d result=%+v err=%v invocations=%d, want fresh resolution without external-input proof", round, result, err, invocations)
		}
	}
}
