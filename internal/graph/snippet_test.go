package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnippet_ValidRelativePath(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SnippetPaged(repo, "a.go", 2, 200, 32*1024, 2, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "two" || got.HasMore {
		t.Fatalf("got %+v, want text %q", got, "two")
	}
}

func TestSnippet_RejectsTraversal(t *testing.T) {
	repo := t.TempDir()
	neighbor := t.TempDir()
	if err := os.WriteFile(filepath.Join(neighbor, "secret.txt"), []byte("nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "inside.go"), []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []string{
		filepath.Join("..", filepath.Base(neighbor), "secret.txt"),
		"../" + filepath.Base(neighbor) + "/secret.txt",
		filepath.Join("pkg", "..", "..", filepath.Base(neighbor), "secret.txt"),
	}
	for _, p := range cases {
		p = filepath.ToSlash(p)
		if _, err := SnippetPaged(repo, p, 1, 200, 32*1024, 1, -1); err == nil || !strings.Contains(err.Error(), "outside repository root") {
			t.Fatalf("path %q should be rejected, err=%v", p, err)
		}
	}
}

func TestSnippet_RejectsAbsolutePath(t *testing.T) {
	repo := t.TempDir()
	abs := filepath.Join(repo, "x.go")
	if err := os.WriteFile(abs, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SnippetPaged(repo, abs, 1, 200, 32*1024, 1, -1); err == nil || !strings.Contains(err.Error(), "absolute paths") {
		t.Fatalf("absolute path should be rejected, err=%v", err)
	}
}

func TestResolveRepoFile_RejectsDotDot(t *testing.T) {
	repo := t.TempDir()
	if _, err := resolveRepoFile(repo, ".."); err == nil {
		t.Fatal("expected rejection for ..")
	}
}

// TestSnippet_RejectsSymlinkOutsideRoot pins physical confinement: a symlink
// whose lexical spelling sits inside the repository but whose target lies
// outside must be rejected before any read. The lexical check alone passes
// this path, so the check must compare resolved physical locations.
func TestSnippet_RejectsSymlinkOutsideRoot(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(repo, "leak.go")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := SnippetPaged(repo, "leak.go", 1, 200, 32*1024, 1, -1); err == nil || !strings.Contains(err.Error(), "outside repository root") {
		t.Fatalf("symlink escaping the root should be rejected, err=%v", err)
	}
}

// TestSnippet_AllowsSymlinkResolvingInsideRoot pins that physical confinement
// does not over-restrict: a symlink whose target stays inside the repository
// is still readable.
func TestSnippet_AllowsSymlinkResolvingInsideRoot(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "real.go"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "real.go"), filepath.Join(repo, "alias.go")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	got, err := SnippetPaged(repo, "alias.go", 1, 200, 32*1024, 2, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "one\ntwo" {
		t.Fatalf("got %q, want %q", got.Text, "one\ntwo")
	}
}
