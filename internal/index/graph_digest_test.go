package index

import (
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

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
