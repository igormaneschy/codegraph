package query

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/index"
)

// TestEngine_DetectChangesReportsConfigOnlyEdit pins R18: editing only a recorded
// sidecar input (here go.mod) must not look like a clean tree. detect_changes
// reports it as a `config` line so an agent does not trust a graph that would
// rebuild, while a genuinely unchanged tree stays empty.
func TestEngine_DetectChangesReportsConfigOnlyEdit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/dc\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package dc\n\nfunc Run() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	if _, err := index.RunAtomic(dbPath, root); err != nil {
		t.Fatalf("index: %v", err)
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := NewEngine(store, index.ProjectName(root), root)

	clean, err := eng.DetectChanges()
	if err != nil {
		t.Fatal(err)
	}
	if clean.Any() {
		t.Fatalf("fresh index reported changes: %+v", clean)
	}

	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/dc\n\ngo 1.26\n\n// config-only edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := eng.DetectChanges()
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.Changed)+len(changed.Added)+len(changed.Deleted) != 0 {
		t.Fatalf("config-only edit must not look like a source change: %+v", changed)
	}
	if len(changed.ConfigChanged) != 1 || changed.ConfigChanged[0] != "go.mod" {
		t.Fatalf("config changes=%v, want [go.mod]", changed.ConfigChanged)
	}
	if !strings.Contains(changed.Summary(), "config\tgo.mod") {
		t.Fatalf("summary=%q, want a config line", changed.Summary())
	}
}
