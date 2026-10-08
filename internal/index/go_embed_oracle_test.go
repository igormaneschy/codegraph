package index

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestGoEmbedInputs_SelectedFilesEqualGoDriverOracle(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOWORK", "off")
	for _, pattern := range []string{"assets", "assets/*", "all:assets", "\"space name.txt\" `other.txt`"} {
		t.Run(pattern, func(t *testing.T) {
			root := writeGoEmbedFixture(t, pattern)
			for _, rel := range []string{"assets/item.txt", "assets/.hidden", "assets/nested/other.txt", "assets/nested/_hidden", "space name.txt", "other.txt"} {
				writeSecurityFile(t, root, "app/"+rel, "fixture\n")
			}
			writeSecurityFile(t, root, "app/assets/nested/module/go.mod", "module example.test/separate\ngo 1.26\n")
			writeSecurityFile(t, root, "app/assets/nested/module/other.txt", "other module\n")
			writeSecurityFile(t, root, "app/assets/nested/.git/fixture", "vcs\n")
			scan, err := scanRepositoryContext(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			var selected []string
			for _, input := range scan.manifest.ResolverInputs.Files {
				selected = append(selected, strings.TrimPrefix(input.Path, "app/"))
			}
			want := goDriverEmbedFiles(t, root)
			if !reflect.DeepEqual(selected, want) {
				t.Fatalf("selected=%v Go driver=%v", selected, want)
			}
		})
	}
}

func goDriverEmbedFiles(t *testing.T, root string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "list", "-json", "./app")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Go driver oracle: %v", err)
	}
	var packageInfo struct{ EmbedFiles []string }
	if err := json.Unmarshal(output, &packageInfo); err != nil {
		t.Fatal(err)
	}
	sort.Strings(packageInfo.EmbedFiles)
	return packageInfo.EmbedFiles
}

func TestGoEmbedInputs_GlobWorksWithMetacharactersInPackageRoot(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "go.mod", "module example.test/metachar\ngo 1.26\n")
	writeSecurityFile(t, root, "app[one]/main.go", "package app\nimport _ \"embed\"\n//go:embed assets/*.txt\nvar text string\n")
	writeSecurityFile(t, root, "app[one]/assets/item.txt", "fixture\n")
	scan, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.ToSlash("app[one]/assets/item.txt")
	if len(scan.manifest.ResolverInputs.Files) != 1 || scan.manifest.ResolverInputs.Files[0].Path != want {
		t.Fatalf("metachar root selection=%v", scan.manifest.ResolverInputs.Files)
	}
}
