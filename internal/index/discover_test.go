package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Lordymine/codegraph/internal/securefile"
)

// TestDiscover_RespectsGitignore pins that discovery honors the repo's .gitignore:
// a gitignored directory (tmp/ — where a Go module cache or build output often
// lives) and a gitignored file glob (*.gen.go) are skipped, while real source is
// kept. Without this, a repo's vendored deps/build artifacts flood the graph.
func TestDiscover_RespectsGitignore(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".gitignore", "tmp/\n*.gen.go\n# comment\n")
	writeFile(t, dir, "main.go", "package main")
	writeFile(t, dir, "internal/svc.go", "package internal")
	writeFile(t, dir, "tmp/gomodcache/dep.go", "package dep") // gitignored dir
	writeFile(t, dir, "schema.gen.go", "package main")        // gitignored glob

	files, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, f := range files {
		rels = append(rels, f.RelPath)
	}

	if !slices.Contains(rels, "main.go") || !slices.Contains(rels, "internal/svc.go") {
		t.Errorf("real source dropped: %v", rels)
	}
	for _, gone := range []string{"tmp/gomodcache/dep.go", "schema.gen.go"} {
		if slices.Contains(rels, gone) {
			t.Errorf("gitignored path %q was indexed: %v", gone, rels)
		}
	}
}

func TestDiscover_RubyExtensions(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"app/models/user.rb", "lib/tasks/report.rake", "config.ru", "sorbet/rbi/user.rbi"} {
		writeFile(t, dir, path, "# source")
	}

	files, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"app/models/user.rb", "lib/tasks/report.rake", "config.ru", "sorbet/rbi/user.rbi"} {
		found := false
		for _, file := range files {
			if file.RelPath == path && file.Lang == LangRuby {
				found = true
			}
		}
		if !found {
			t.Errorf("Ruby source %q was not discovered as LangRuby: %+v", path, files)
		}
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDiscover_SkipsSymlinkedSources pins that discovery never indexes symlink
// entries: a symlinked source is not opened or returned whether its target
// lies outside the repository (the vulnerable path: verifyReadableSource used
// to follow it and read the external file) or inside it, while regular source
// keeps working.
func TestDiscover_SkipsSymlinkedSources(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeFile(t, outside, "secret.go", "package secret\n")
	writeFile(t, dir, "main.go", "package main\n")
	writeFile(t, dir, "real.go", "package real\n")
	if err := os.Symlink(filepath.Join(outside, "secret.go"), filepath.Join(dir, "leak.go")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "real.go"), filepath.Join(dir, "alias.go")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	files, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, f := range files {
		rels = append(rels, f.RelPath)
	}
	for _, gone := range []string{"leak.go", "alias.go"} {
		if slices.Contains(rels, gone) {
			t.Errorf("symlinked path %q was indexed: %v", gone, rels)
		}
	}
	for _, kept := range []string{"main.go", "real.go"} {
		if !slices.Contains(rels, kept) {
			t.Errorf("regular source %q dropped: %v", kept, rels)
		}
	}
}

func TestDiscover_RejectsSymlinkedIgnoreFiles(t *testing.T) {
	for _, name := range []string{".gitignore", ".cbmignore"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			outside := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(outside, []byte("*.go\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, name)); err != nil {
				t.Skipf("symlink creation unavailable: %v", err)
			}
			writeFile(t, root, "main.go", "package main\n")

			_, err := Discover(root)
			if err == nil || !errors.Is(err, securefile.ErrUnsafePath) {
				t.Fatalf("symlinked %s error=%v, want ErrUnsafePath", name, err)
			}
		})
	}
}

// TestScanRepositoryContext_SkipsExternalSymlinks pins that the repository
// scan never hashes or fingerprints unrecognized symlinked sources: a symlink
// can point outside the root, and an external file must not be certified as a
// repository source or analysis input.
func TestScanRepositoryContext_SkipsExternalSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n")
	writeFile(t, outside, "secret.go", "package secret\n")
	if err := os.Symlink(filepath.Join(outside, "secret.go"), filepath.Join(dir, "leak.go")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	scan, err := scanRepositoryContext(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range scan.files {
		if f.RelPath == "leak.go" {
			t.Fatalf("symlinked external source was scanned: %+v", scan.files)
		}
	}
	foundMain := false
	for _, f := range scan.files {
		if f.RelPath == "main.go" {
			foundMain = true
		}
	}
	if !foundMain {
		t.Errorf("regular source dropped from scan: %+v", scan.files)
	}
}

// TestScanRepositoryContext_RejectsSymlinkedManifestInput pins that a
// recognized manifest/resolver input must never be a symlink: silently
// skipping it would drop a resolver scope or certify an attempted input change
// as fresh, so the scan fails closed with securefile.ErrUnsafePath instead of
// fingerprinting the external file.
func TestScanRepositoryContext_RejectsSymlinkedManifestInput(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n")
	writeFile(t, outside, "package.json", `{"name":"outside"}`)
	if err := os.Symlink(filepath.Join(outside, "package.json"), filepath.Join(dir, "package.json")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	_, err := scanRepositoryContext(context.Background(), dir)
	if err == nil || !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("symlinked manifest input scan error=%v, want ErrUnsafePath", err)
	}
}
