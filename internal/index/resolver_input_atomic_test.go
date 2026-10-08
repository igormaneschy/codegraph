package index

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/securefile"
)

func TestResolverInputs_UnsafeDependencyPreservesCommittedGraph(t *testing.T) {
	root := writeGoBuildTagFixture(t)
	writeSecurityFile(t, root, "vendor/local/input.go", "package local\nfunc F() {}\n")
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	initial, err := RunAtomic(dbPath, root)
	if err != nil || initial.Status != StatusHealthy {
		t.Fatalf("initial=%+v err=%v", initial, err)
	}
	project := ProjectName(root)
	before := digestOf(t, dbPath, project)
	outside := securityPhysicalTempDir(t)
	writeSecurityFile(t, outside, "input.go", "package other\nfunc F() {}\n")
	if err := os.RemoveAll(filepath.Join(root, "vendor/local")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "vendor/local")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := RunAtomic(dbPath, root); !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("unsafe dependency refresh error=%v", err)
	}
	if digestOf(t, dbPath, project) != before {
		t.Fatal("unsafe dependency replaced the committed graph")
	}
	assertStoredCallTargets(t, dbPath, project, "common.go.Run", []string{"default.go.selected"})
}

func TestGoEnvironment_UnadmittedOverlayPreservesCommittedGraph(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	root := writeGoBuildTagFixture(t)
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	initial, err := RunAtomic(dbPath, root)
	if err != nil || initial.Status != StatusHealthy {
		t.Fatalf("initial=%+v err=%v", initial, err)
	}
	project := ProjectName(root)
	before := digestOf(t, dbPath, project)
	overlay := filepath.Join(securityPhysicalTempDir(t), "overlay.json")
	if err := os.WriteFile(overlay, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", "-overlay="+overlay)
	if _, err := RunAtomic(dbPath, root); err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Fatalf("unadmitted overlay refresh error=%v", err)
	}
	if digestOf(t, dbPath, project) != before {
		t.Fatal("unadmitted overlay replaced the committed graph")
	}
}
