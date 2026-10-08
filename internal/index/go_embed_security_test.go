package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/securefile"
)

func TestGoEmbedInputs_UnsafeAssetPreservesCommittedGraph(t *testing.T) {
	root := writeGoEmbedFixture(t, "assets/item.txt")
	writeSecurityFile(t, root, "app/assets/item.txt", "fixture\n")
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	result, err := RunAtomic(db, root)
	if err != nil || result.Status != StatusHealthy {
		t.Fatalf("initial=%+v err=%v", result, err)
	}
	project := ProjectName(root)
	before := digestOf(t, db, project)
	outside := securityPhysicalTempDir(t)
	writeSecurityFile(t, outside, "item.txt", "outside fixture\n")
	asset := filepath.Join(root, "app/assets/item.txt")
	if err := os.Remove(asset); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "item.txt"), asset); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := RunAtomic(db, root); !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("unsafe asset error=%v", err)
	}
	if digestOf(t, db, project) != before {
		t.Fatal("unsafe embed asset replaced committed graph")
	}
	assertStoredCallTargets(t, db, project, "app/main.go.Run", []string{"app/main.go.helper"})
}

func TestGoEmbedInputs_UnsafeParentIsNotAnEmptyGlobMatch(t *testing.T) {
	root := writeGoEmbedFixture(t, "assets/*.txt")
	outside := securityPhysicalTempDir(t)
	writeSecurityFile(t, outside, "item.txt", "outside fixture\n")
	if err := os.Symlink(outside, filepath.Join(root, "app/assets")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := scanRepositoryContext(context.Background(), root)
	if !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("glob swallowed unsafe-directory error: %v", err)
	}
}

func TestGoEmbedInputs_PrivateEnvironmentFileRequiresAdmission(t *testing.T) {
	for _, pattern := range []string{"assets/.env", "all:assets"} {
		t.Run(pattern, func(t *testing.T) {
			root := writeGoEmbedFixture(t, pattern)
			// Generated fixture only; never the operator's environment.
			writeSecurityFile(t, root, "app/assets/.env", "fixture-only\n")
			_, err := scanRepositoryContext(context.Background(), root)
			if err == nil {
				t.Fatal("private environment file admitted without authorization")
			}
		})
	}
}

func TestGoEmbedInputs_UnobservedAdditionDoesNotEnterSnapshot(t *testing.T) {
	root := writeGoEmbedFixture(t, "assets")
	writeSecurityFile(t, root, "app/assets/item.txt", "fixture\n")
	before, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeSecurityFile(t, root, "app/assets/added.txt", "new fixture\n")
	after, err := scanRepositoryContext(context.Background(), root)
	if err != nil || sameRepositoryScan(before, after) {
		t.Fatalf("asset membership was not observed: %v", err)
	}
	snapshot, _, cleanup, err := resolverSnapshotForScan(context.Background(), before)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Lstat(filepath.Join(snapshot, "app/assets/added.txt")); !os.IsNotExist(err) {
		t.Fatalf("unobserved asset was staged: %v", err)
	}
}

func TestGoEmbedInputs_LateSymlinkCannotReplaceObservedBytes(t *testing.T) {
	root := writeGoEmbedFixture(t, "assets/item.txt")
	writeSecurityFile(t, root, "app/assets/item.txt", "fixture\n")
	scan, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	outside := securityPhysicalTempDir(t)
	writeSecurityFile(t, outside, "item.txt", "outside fixture\n")
	asset := filepath.Join(root, "app/assets/item.txt")
	if err := os.Remove(asset); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "item.txt"), asset); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, _, _, err = resolverSnapshotForScan(context.Background(), scan)
	if !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("late asset symlink was accepted: %v", err)
	}
}

func TestGoEmbedInputs_CancellationReturnsNoPaths(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	paths, err := goEmbedInputPaths(ctx, repositoryScan{files: []SourceFile{{Lang: LangGo}}})
	if !errors.Is(err, context.Canceled) || paths != nil {
		t.Fatalf("canceled selection=%v err=%v", paths, err)
	}
}
