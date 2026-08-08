package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	scippb "github.com/scip-code/scip/bindings/go/scip"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/scip"
	"github.com/Lordymine/codegraph/internal/securefile"
)

func TestSecurity_SourceConsumersRejectSymlinkedInputs(t *testing.T) {
	root := securityPhysicalTempDir(t)
	outside := securityPhysicalTempDir(t)
	outsideSource := filepath.Join(outside, "secret.go")
	if err := os.WriteFile(outsideSource, []byte("package secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked.go")
	if err := os.Symlink(outsideSource, linked); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	if _, _, err := ExtractDefinitionsChecked("project", SourceFile{
		AbsPath: linked, RelPath: "linked.go", Lang: LangGo,
	}); !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("definitions symlink error=%v, want ErrUnsafePath", err)
	}
	if _, err := collectImportsStreamingContext(context.Background(), "project", []SourceFile{{
		AbsPath: linked, RelPath: "linked.go", Lang: LangGo,
	}}); !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("imports symlink error=%v, want ErrUnsafePath", err)
	}

	store, err := graph.Open(filepath.Join(securityPhysicalTempDir(t), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldTargets := rubyCallTargets
	t.Cleanup(func() { rubyCallTargets = oldTargets })
	rubyCallTargets = func(*graph.Store, string) ([]graph.RubyCallTarget, error) {
		return []graph.RubyCallTarget{{
			Owner: "Gateway", Name: "authorize", QualifiedName: "project:gateway.rb.Gateway.authorize",
		}}, nil
	}
	_, status, err := resolveRubyCalls(context.Background(), store, "project", []SourceFile{{
		AbsPath: linked, RelPath: "gateway.rb", Lang: LangRuby,
	}}, scip.BuildEnclosingFromSpans(nil), map[string]bool{"ruby": true})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Failed || !strings.Contains(status.Error, securefile.ErrUnsafePath.Error()) {
		t.Fatalf("Ruby symlink status=%+v, want visible unsafe-path failure", status)
	}
}

func TestSecurity_TSResolverUsesPrivateStableSnapshot(t *testing.T) {
	root := securityPhysicalTempDir(t)
	original := []byte("export const stable = 1\n")
	writeSecurityFile(t, root, "tsconfig.json", "{}\n")
	writeSecurityFile(t, root, "main.ts", string(original))
	outside := filepath.Join(securityPhysicalTempDir(t), "outside.ts")
	if err := os.WriteFile(outside, []byte("export const outside = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := graph.Open(filepath.Join(securityPhysicalTempDir(t), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	var resolverDir string
	scipRunAndRead = func(_ context.Context, dir, out string) (*scippb.Index, scip.RunStats, error) {
		resolverDir = dir
		staged, readErr := securefile.ReadFile(filepath.Join(dir, "main.ts"))
		if readErr != nil {
			return nil, scip.RunStats{}, readErr
		}
		if string(staged) != string(original) {
			return nil, scip.RunStats{}, errors.New("resolver snapshot contained unexpected source")
		}
		if err := os.Remove(filepath.Join(root, "main.ts")); err != nil {
			return nil, scip.RunStats{}, err
		}
		if err := os.Symlink(outside, filepath.Join(root, "main.ts")); err != nil {
			return nil, scip.RunStats{}, err
		}
		staged, readErr = securefile.ReadFile(filepath.Join(dir, "main.ts"))
		if readErr != nil {
			return nil, scip.RunStats{}, readErr
		}
		if string(staged) != string(original) {
			return nil, scip.RunStats{}, errors.New("resolver snapshot changed after source replacement")
		}
		if err := os.WriteFile(out, nil, 0o600); err != nil {
			return nil, scip.RunStats{}, err
		}
		return &scippb.Index{}, scip.RunStats{}, nil
	}

	if _, err := resolveTSCalls(context.Background(), store, "project", root,
		scip.BuildEnclosingFromSpans(nil), map[string]bool{"": true}); err != nil {
		t.Fatal(err)
	}
	if resolverDir == "" || filepath.Clean(resolverDir) == filepath.Clean(root) {
		t.Fatalf("SCIP received repository root %q, want private snapshot", resolverDir)
	}
}

func TestSecurity_RunAtomicResolverHandoffDoesNotPassRepositoryRoot(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "tsconfig.json", "{}\n")
	writeSecurityFile(t, root, "main.ts", "export const stable = 1\n")
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")

	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	var resolverDirs []string
	scipRunAndRead = func(_ context.Context, dir, out string) (*scippb.Index, scip.RunStats, error) {
		resolverDirs = append(resolverDirs, dir)
		if err := os.WriteFile(out, nil, 0o600); err != nil {
			return nil, scip.RunStats{}, err
		}
		return &scippb.Index{}, scip.RunStats{}, nil
	}
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatal(err)
	}
	if len(resolverDirs) != 1 || filepath.Clean(resolverDirs[0]) == filepath.Clean(root) {
		t.Fatalf("production resolver dirs=%v, want one private snapshot outside repository root", resolverDirs)
	}
}

func TestSecurity_GoResolverUsesPrivateStableSnapshot(t *testing.T) {
	root := securityPhysicalTempDir(t)
	original := []byte("package main\nfunc stable() {}\n")
	writeSecurityFile(t, root, "go.mod", "module example.test/stable\ngo 1.26\n")
	writeSecurityFile(t, root, "main.go", string(original))
	outside := filepath.Join(securityPhysicalTempDir(t), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\nfunc attacker() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldResolver := goCallEdges
	t.Cleanup(func() { goCallEdges = oldResolver })
	var resolverRoot string

	// The resolver seam receives only the staged root. Mutate the source through
	// the canonical path before returning, then verify the staged descriptor was
	// the one consumed. The original is restored by the test cleanup below.
	originalPath := filepath.Join(root, "main.go")
	t.Cleanup(func() {
		_ = os.Remove(originalPath)
		_ = os.WriteFile(originalPath, original, 0o600)
	})
	goCallEdges = func(_ context.Context, _ string, stagedRoot string, _ func(string) bool) ([]graph.Edge, error) {
		resolverRoot = stagedRoot
		staged, readErr := securefile.ReadFile(filepath.Join(stagedRoot, "main.go"))
		if readErr != nil {
			return nil, readErr
		}
		if string(staged) != string(original) {
			return nil, errors.New("Go resolver snapshot contained unexpected source")
		}
		if err := os.Remove(originalPath); err != nil {
			return nil, err
		}
		if err := os.Symlink(outside, originalPath); err != nil {
			return nil, err
		}
		staged, readErr = securefile.ReadFile(filepath.Join(stagedRoot, "main.go"))
		if readErr != nil {
			return nil, readErr
		}
		if string(staged) != string(original) {
			return nil, errors.New("Go resolver snapshot changed after source replacement")
		}
		return nil, nil
	}

	_, status, err := resolveGoCalls(context.Background(), "project", root, []SourceFile{{
		AbsPath: originalPath, RelPath: "main.go", Lang: LangGo,
	}}, scip.BuildEnclosingFromSpans(nil), map[string]bool{"go": true})
	if err != nil {
		t.Fatal(err)
	}
	if resolverRoot == "" || filepath.Clean(resolverRoot) == filepath.Clean(root) || !status.Succeeded {
		t.Fatalf("Go resolver root=%q status=%+v, want successful private snapshot", resolverRoot, status)
	}
}

func TestSecurity_TSConfigScopesSkipSymlinkEntries(t *testing.T) {
	root := securityPhysicalTempDir(t)
	outside := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "tsconfig.json", "{}\n")
	writeSecurityFile(t, outside, "tsconfig.json", "{}\n")
	writeSecurityFile(t, outside, "leak.ts", "export const leak = 1\n")
	if err := os.Symlink(outside, filepath.Join(root, "linked-package")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	dirs, err := tsconfigDirsContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStringSlice(dirs, []string{""}) {
		t.Fatalf("tsconfig scopes=%v, want only root scope", dirs)
	}
}

func TestSecurity_ManifestResolverSymlinkFailsClosed(t *testing.T) {
	root := securityPhysicalTempDir(t)
	outside := securityPhysicalTempDir(t)
	writeSecurityFile(t, outside, "package.json", `{"private":true}`)
	if err := os.Symlink(filepath.Join(outside, "package.json"), filepath.Join(root, "package.json")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := scanRepositoryContext(context.Background(), root); err == nil || !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("manifest symlink scan error=%v, want ErrUnsafePath", err)
	}
}

func TestSecurity_ResolverRejectsExternalDependencySymlink(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "tsconfig.json", "{}\n")
	writeSecurityFile(t, root, "main.ts", "export const stable = 1\n")
	outside := securityPhysicalTempDir(t)
	writeSecurityFile(t, outside, "package/index.ts", "export const secret = 1\n")
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "package"), filepath.Join(root, "node_modules", "package")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	scan, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := resolverSnapshotForScan(context.Background(), scan); err == nil || !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("external dependency symlink error=%v, want ErrUnsafePath", err)
	}
}

func TestSecurity_ResolverRewritesInRepositoryDependencySymlink(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "tsconfig.json", "{}\n")
	writeSecurityFile(t, root, "main.ts", "export const stable = 1\n")
	writeSecurityFile(t, root, "packages/shared/index.ts", "export const shared = 1\n")
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../packages/shared", filepath.Join(root, "node_modules", "shared")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	scan, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, cleanup, err := resolverSnapshotForScan(context.Background(), scan)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Cleanup(func() {
			if cleanupErr := cleanup(); cleanupErr != nil {
				t.Errorf("cleanup resolver snapshot: %v", cleanupErr)
			}
		})
	}
	staged, err := os.ReadFile(filepath.Join(snapshot, "node_modules", "shared", "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(staged) != "export const shared = 1\n" {
		t.Fatalf("staged dependency content=%q", staged)
	}
	linkPath := filepath.Join(snapshot, "node_modules", "shared")
	linkTarget, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(linkTarget) {
		t.Fatalf("staged dependency link retained absolute target %q", linkTarget)
	}
	resolved := filepath.Join(filepath.Dir(linkPath), linkTarget)
	if rel, err := filepath.Rel(snapshot, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("staged dependency link escapes snapshot: target=%q rel=%q err=%v", linkTarget, rel, err)
	}
}

func TestSecurity_TSResolverCleanupFailureIsObservable(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "tsconfig.json", "{}\n")
	writeSecurityFile(t, root, "main.ts", "export const stable = 1\n")
	store, err := graph.Open(filepath.Join(securityPhysicalTempDir(t), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	scipRunAndRead = func(_ context.Context, dir, out string) (*scippb.Index, scip.RunStats, error) {
		replacePrivateResolverDirectoryForTest(t, dir)
		if err := os.WriteFile(out, nil, 0o600); err != nil {
			return nil, scip.RunStats{}, err
		}
		return &scippb.Index{}, scip.RunStats{}, nil
	}
	if _, err := resolveTSCalls(context.Background(), store, "project", root,
		scip.BuildEnclosingFromSpans(nil), map[string]bool{"": true}); err == nil || !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("resolver cleanup error=%v, want observable ErrUnsafePath", err)
	}
}

func TestSecurity_GoResolverCleanupFailureIsObservable(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "go.mod", "module example.test/cleanup\ngo 1.26\n")
	writeSecurityFile(t, root, "main.go", "package main\nfunc stable() {}\n")
	oldResolver := goCallEdges
	t.Cleanup(func() { goCallEdges = oldResolver })
	goCallEdges = func(_ context.Context, _ string, stagedRoot string, _ func(string) bool) ([]graph.Edge, error) {
		replacePrivateResolverDirectoryForTest(t, stagedRoot)
		return nil, nil
	}
	_, _, err := resolveGoCalls(context.Background(), "project", root, []SourceFile{{
		AbsPath: filepath.Join(root, "main.go"), RelPath: "main.go", Lang: LangGo,
	}}, scip.BuildEnclosingFromSpans(nil), map[string]bool{"go": true})
	if err == nil || !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("Go resolver cleanup error=%v, want observable ErrUnsafePath", err)
	}
}

func TestSecurity_RunPipelineResolverCleanupFailureIsObservable(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "tsconfig.json", "{}\n")
	writeSecurityFile(t, root, "main.ts", "export const stable = 1\n")
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	oldRunner := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = oldRunner })
	scipRunAndRead = func(_ context.Context, dir, out string) (*scippb.Index, scip.RunStats, error) {
		replacePrivateResolverDirectoryForTest(t, dir)
		if err := os.WriteFile(out, nil, 0o600); err != nil {
			return nil, scip.RunStats{}, err
		}
		return &scippb.Index{}, scip.RunStats{}, nil
	}
	if _, err := RunAtomic(dbPath, root); err == nil || !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("pipeline resolver cleanup error=%v, want observable ErrUnsafePath", err)
	}
}

func replacePrivateResolverDirectoryForTest(t *testing.T, dir string) {
	t.Helper()
	moved := dir + ".quarantine"
	// #nosec G703 -- this helper intentionally renames the private resolver
	// directory itself to simulate replacement; it never follows the path.
	if err := os.Rename(dir, moved); err != nil {
		t.Fatalf("replace private resolver directory: %v", err)
	}
	// #nosec G703 -- recreate only the exact private snapshot path just moved so
	// cleanup observes a replaced directory entry deterministically.
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("recreate private resolver directory: %v", err)
	}
	t.Cleanup(func() {
		// #nosec G703 -- cleanup removes only the test-created replacement path.
		_ = os.RemoveAll(dir)
		// #nosec G703 -- cleanup removes only the test-created quarantine path.
		_ = os.RemoveAll(moved)
	})
}

func TestSecurity_ManifestReadAndWriteRejectSidecarSymlinks(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "main.go", "package main\nfunc main() {}\n")
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	if _, err := RunAtomic(dbPath, root); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	destination := ManifestPath(dbPath)
	target := filepath.Join(securityPhysicalTempDir(t), "outside-manifest.json")
	if err := os.WriteFile(target, []byte("keep outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, destination); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := ReadManifest(dbPath); !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("manifest symlink read error=%v, want ErrUnsafePath", err)
	}
	if err := writeManifestFile(destination, manifest); err != nil {
		t.Fatal(err)
	}
	targetData, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(targetData) != "keep outside\n" {
		t.Fatalf("manifest symlink target changed to %q", targetData)
	}
	info, err := os.Lstat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest destination=%v mode=%o, want regular 0600", info.Mode(), info.Mode().Perm())
	}
}

func securityPhysicalTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return physical
}

func writeSecurityFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
