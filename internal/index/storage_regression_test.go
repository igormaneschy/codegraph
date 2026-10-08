package index

import (
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

func TestDefinitionStorage_LegalRepeatedDeclarationsKeepFTSConsistent(t *testing.T) {
	fixtures := []struct {
		name, file, source, duplicateQN string
		lang                            Lang
	}{
		{"Go init", "a.go", "package symbols\nfunc init() {}\nfunc helper() {}\nfunc init() {}\n", "p:a.go.init", LangGo},
		{"TS overloads", "a.ts", "abstract class Box {\n abstract alpha(x: string): string;\n abstract alpha(x: number): number;\n helper() {}\n}\n", "p:a.ts.Box.alpha", LangTS},
		{"Ruby reopenings", "a.rb", "class Box\n def alpha; 1; end\nend\ndef helper; 2; end\nclass Box\n def beta; 3; end\nend\n", "p:a.rb.Box", LangRuby},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			nodes, edges := extractDefsFromSource("p", fixture.file, fixture.lang, []byte(fixture.source))
			duplicates := 0
			for _, node := range nodes {
				if node.QualifiedName == fixture.duplicateQN {
					duplicates++
				}
			}
			if duplicates != 2 {
				t.Fatalf("fixture emitted %d duplicate declarations, want 2: %+v", duplicates, nodes)
			}
			store, err := graph.Open(filepath.Join(securityPhysicalTempDir(t), "graph.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.InsertNodes(nodes); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.InsertEdges(edges); err != nil {
				t.Fatal(err)
			}
			if err := store.ValidateIntegrity(); err != nil {
				t.Fatal(err)
			}
			hits, err := store.Search("p", "helper", "", 10)
			if err != nil || len(hits) != 1 || hits[0].Node.Name != "helper" {
				t.Fatalf("helper search=%+v err=%v, want only helper", hits, err)
			}
		})
	}
}

func TestRunAtomic_MultipleGoInitDeclarationsCommitHealthyIndex(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "go.mod", "module example.com/symbols\n\ngo 1.26\n")
	writeSecurityFile(t, root, "a.go", "package symbols\nfunc init() { helper() }\nfunc helper() {}\nfunc init() { helper() }\n")
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	for _, wantReused := range []bool{false, true} {
		result, err := RunAtomic(dbPath, root)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != StatusHealthy || result.Reused != wantReused {
			t.Fatalf("result=%+v, want healthy reused=%v", result, wantReused)
		}
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ValidateIntegrity(); err != nil {
		t.Fatal(err)
	}
	hits, err := store.Search(ProjectName(root), "init", "", 10)
	if err != nil || len(hits) != 1 || hits[0].Node.Name != "init" {
		t.Fatalf("init search=%+v err=%v, want one first-wins init", hits, err)
	}
}
