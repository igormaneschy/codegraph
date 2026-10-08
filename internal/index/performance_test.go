package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func configBenchmarkRoot(b *testing.B) string {
	b.Helper()
	root := b.TempDir()
	for i := 0; i < 40; i++ {
		dir := filepath.Join(root, fmt.Sprintf("pkg%02d", i))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "jsconfig.json"), []byte(`{"extends":"../base.json"}`), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "base.json"), []byte(`{"compilerOptions":{}}`), 0o600); err != nil {
		b.Fatal(err)
	}
	return root
}

func BenchmarkScanRepository_ConfigGraph(b *testing.B) {
	root := configBenchmarkRoot(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := scanRepositoryContext(context.Background(), root); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCollectImports_Go(b *testing.B) {
	root := b.TempDir()
	files := make([]SourceFile, 200)
	for i := range files {
		rel := fmt.Sprintf("file%03d.go", i)
		abs := filepath.Join(root, rel)
		if err := os.WriteFile(abs, []byte("package fixture\nfunc Run() {}\n"), 0o600); err != nil {
			b.Fatal(err)
		}
		files[i] = SourceFile{AbsPath: abs, RelPath: rel, Lang: LangGo}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := collectImportsStreamingContext(context.Background(), "fixture", files); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRunAtomic_ConfigNoOp(b *testing.B) {
	root := configBenchmarkRoot(b)
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package fixture\nfunc Run() {}\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	db := filepath.Join(b.TempDir(), "graph.db")
	if _, err := RunAtomic(db, root); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := RunAtomic(db, root)
		if err != nil || !result.Reused {
			b.Fatalf("strict no-op: reused=%v err=%v", result.Reused, err)
		}
	}
}

func TestImportsSkipsUnsupportedSourceReads(t *testing.T) {
	files := []SourceFile{{AbsPath: filepath.Join(t.TempDir(), "absent.go"), RelPath: "absent.go", Lang: LangGo}}
	edges, err := collectImportsStreamingContext(context.Background(), "fixture", files)
	if err != nil || len(edges) != 0 {
		t.Fatalf("unsupported Go import pass: edges=%v err=%v", edges, err)
	}
	if edges := ResolveImports("fixture", files); len(edges) != 0 {
		t.Fatalf("Go imports=%v", edges)
	}
}
