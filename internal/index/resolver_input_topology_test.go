package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/securefile"
)

func writeDependencyTopologyFixture(t *testing.T) string {
	t.Helper()
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "tsconfig.json", "{}")
	writeSecurityFile(t, root, "main.ts", "export function run() {}\n")
	writeSecurityFile(t, root, "packages/first/index.d.ts", "export declare function run(): void;\n")
	writeSecurityFile(t, root, "packages/other/index.d.ts", "export declare function run(): void;\n")
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../packages/first", filepath.Join(root, "node_modules/local")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return root
}

func TestResolverInputs_LinkTargetChangeInvalidatesAndRefusesLateStaging(t *testing.T) {
	root := writeDependencyTopologyFixture(t)
	before, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "node_modules/local")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../packages/other", link); err != nil {
		t.Fatal(err)
	}
	after, err := scanRepositoryContext(context.Background(), root)
	if err != nil || sameRepositoryScan(before, after) {
		t.Fatalf("link target transition was not observed: %v", err)
	}
	_, _, _, err = resolverSnapshotForScan(context.Background(), before)
	if !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("late link replacement must refuse staging: %v", err)
	}
}

func TestResolverInputs_EmptyDirectoryMembershipInvalidates(t *testing.T) {
	root := writeDependencyTopologyFixture(t)
	before, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "node_modules/empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := scanRepositoryContext(context.Background(), root)
	if err != nil || sameRepositoryScan(before, after) {
		t.Fatalf("dependency directory membership was not observed: %v", err)
	}
}

func TestResolverInputs_PrivateEnvironmentFileRequiresExplicitAdmission(t *testing.T) {
	root := writeDependencyTopologyFixture(t)
	// This is a generated fixture, not the operator's environment file.
	writeSecurityFile(t, root, "node_modules/local/.env", "fixture-only\n")
	_, err := scanRepositoryContext(context.Background(), root)
	if err == nil {
		t.Fatal("unadmitted private environment file was accepted")
	}
}

func TestResolverInputs_CancellationHasNoObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := observeResolverInputs(ctx, securityPhysicalTempDir(t), []string{"node_modules"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled observation error=%v", err)
	}
}

func TestResolverInputPlan_RejectsMalformedPersistedShape(t *testing.T) {
	for _, plan := range []ResolverInputPlan{
		{Version: "future"},
		{Version: resolverInputVersion, Directories: []string{"../external"}},
		{Version: resolverInputVersion, Files: []InputFingerprint{{Path: "node_modules/local/index.d.ts", SHA256: "invalid"}}},
		{Version: resolverInputVersion, Links: []ResolverInputLink{{Path: "node_modules/local", Target: "../external"}}},
		{Version: resolverInputVersion, Directories: []string{"node_modules/z", "node_modules/a"}},
	} {
		if err := validateResolverInputPlan(plan); err == nil {
			t.Fatalf("malformed plan accepted: %+v", plan)
		}
	}
}
