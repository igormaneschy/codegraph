package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeGoEmbedFixture(t *testing.T, pattern string) string {
	t.Helper()
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "go.mod", "module example.test/embed-inputs\ngo 1.26\n")
	writeSecurityFile(t, root, "app/main.go", fmt.Sprintf("package app\nimport \"embed\"\n//go:embed %s\nvar assets embed.FS\nfunc Run() { helper() }\nfunc helper() {}\n", pattern))
	return root
}

func TestGoEmbedInputs_HealthyCallsWithSelectedLocalAssets(t *testing.T) {
	for _, pattern := range []string{"assets/item.txt", "assets/*.txt", "assets", "all:assets", "\"space name.txt\" `other.txt`"} {
		t.Run(pattern, func(t *testing.T) {
			root := writeGoEmbedFixture(t, pattern)
			writeSecurityFile(t, root, "app/assets/item.txt", "fixture\n")
			writeSecurityFile(t, root, "app/assets/nested/other.txt", "nested fixture\n")
			writeSecurityFile(t, root, "app/space name.txt", "spaced fixture\n")
			writeSecurityFile(t, root, "app/other.txt", "other fixture\n")
			db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
			result, err := RunAtomic(db, root)
			if err != nil || result.Status != StatusHealthy || result.Files != 1 {
				t.Fatalf("embed index=%+v err=%v, want healthy", result, err)
			}
			assertStoredCallTargets(t, db, ProjectName(root), "app/main.go.Run", []string{"app/main.go.helper"})
			manifest, err := ReadManifest(db)
			if err != nil || slices.Contains(manifest.ResolverInputs.NoReuseReasons, "go-embed-inputs-unobserved") {
				t.Fatalf("observed local embed inputs kept the unobserved reason: %+v err=%v", manifest.ResolverInputs, err)
			}
			second, err := RunAtomic(db, root)
			if err != nil || !second.Reused {
				t.Fatalf("unchanged embed inputs were not a certified no-op: %+v err=%v", second, err)
			}
		})
	}
}

func TestGoEmbedInputs_SnapshotSelectionMatchesDirectoryAndGlobRules(t *testing.T) {
	for _, pattern := range []string{"assets", "assets/*", "all:assets"} {
		t.Run(pattern, func(t *testing.T) {
			root := writeGoEmbedFixture(t, pattern)
			for _, rel := range []string{"assets/item.txt", "assets/.hidden", "assets/_private", "assets/nested/.hidden", "assets/nested/other.txt", "unused.txt"} {
				writeSecurityFile(t, root, "app/"+rel, "fixture\n")
			}
			writeSecurityFile(t, root, "app/assets/nested/module/go.mod", "module other.test/assets\ngo 1.26\n")
			writeSecurityFile(t, root, "app/assets/nested/module/item.txt", "different module\n")
			writeSecurityFile(t, root, "app/assets/nested/.git/data", "vcs fixture\n")
			writeSecurityFile(t, root, ".cbmignore", "app/assets/\n")
			scan, err := scanRepositoryContext(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, _, cleanup, err := resolverSnapshotForScan(context.Background(), scan)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := cleanup(); err != nil {
					t.Error(err)
				}
			})
			for rel, present := range map[string]bool{
				"assets/item.txt": true, "assets/nested/other.txt": true,
				"assets/.hidden": pattern != "assets", "assets/_private": pattern != "assets",
				"assets/nested/.hidden": pattern == "all:assets", "unused.txt": false,
				"assets/nested/module/item.txt": false, "assets/nested/.git/data": false,
			} {
				_, err := os.Lstat(filepath.Join(snapshot, "app", rel))
				if present && err != nil || !present && !os.IsNotExist(err) {
					t.Errorf("pattern=%q asset=%q presence=%t err=%v", pattern, rel, present, err)
				}
			}
		})
	}
}

func TestGoEmbedInputs_SelectedMutationInvalidatesAndFailsLateStaging(t *testing.T) {
	root := writeGoEmbedFixture(t, "assets")
	writeSecurityFile(t, root, "app/assets/item.txt", "before\n")
	before, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeSecurityFile(t, root, "app/assets/item.txt", "after\n")
	after, err := scanRepositoryContext(context.Background(), root)
	if err != nil || sameRepositoryScan(before, after) {
		t.Fatalf("selected asset mutation was not observed: %v", err)
	}
	_, _, _, err = resolverSnapshotForScan(context.Background(), before)
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("late asset mutation was accepted: %v", err)
	}
}

func TestGoEmbedCertification_AssetChangesInvalidateNoop(t *testing.T) {
	root := writeGoEmbedFixture(t, "assets")
	writeSecurityFile(t, root, "app/assets/item.txt", "one\n")
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	if first, err := RunAtomic(db, root); err != nil || first.Reused || first.Status != StatusHealthy {
		t.Fatalf("first index=%+v err=%v", first, err)
	}
	if second, err := RunAtomic(db, root); err != nil || !second.Reused {
		t.Fatalf("unchanged embed inputs were not a certified no-op: %+v err=%v", second, err)
	}
	writeSecurityFile(t, root, "app/assets/item.txt", "two\n")
	if edited, err := RunAtomic(db, root); err != nil || edited.Reused {
		t.Fatalf("edited asset was reused: %+v err=%v", edited, err)
	}
	writeSecurityFile(t, root, "app/assets/extra.txt", "three\n")
	if added, err := RunAtomic(db, root); err != nil || added.Reused {
		t.Fatalf("added asset was reused: %+v err=%v", added, err)
	}
	if err := os.Remove(filepath.Join(root, "app/assets/extra.txt")); err != nil {
		t.Fatal(err)
	}
	if removed, err := RunAtomic(db, root); err != nil || removed.Reused {
		t.Fatalf("removed asset was reused: %+v err=%v", removed, err)
	}
}

func TestGoEmbedCertification_UnhandledDirectivesKeepReason(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{"missing import", "package app\n\n//go:embed assets/*.txt\nvar text string\n"},
		{"invalid pattern", "package app\n\nimport _ \"embed\"\n//go:embed \"unterminated\nvar text string\n"},
		{"parse error", "package app\n\nimport _ \"embed\"\n//go:embed assets/*.txt\nvar text string\nfunc {\n"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			root := securityPhysicalTempDir(t)
			writeSecurityFile(t, root, "go.mod", "module example.test/unhandled\ngo 1.26\n")
			writeSecurityFile(t, root, "app/main.go", item.source)
			writeSecurityFile(t, root, "app/assets/item.txt", "fixture\n")
			scan, err := scanRepositoryContext(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(scan.manifest.ResolverInputs.NoReuseReasons, "go-embed-inputs-unobserved") {
				t.Fatalf("unhandled directive lost the reason: %v", scan.manifest.ResolverInputs.NoReuseReasons)
			}
		})
	}
}
