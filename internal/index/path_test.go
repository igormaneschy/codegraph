package index

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalPath_ResolvesSymlinkedAncestors(t *testing.T) {
	base := t.TempDir()
	physical := filepath.Join(base, "physical", "repo")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(base, "alias-parent")
	if err := os.Symlink(filepath.Dir(physical), aliasParent); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	alias := filepath.Join(aliasParent, filepath.Base(physical))
	got, err := CanonicalPath(alias)
	if err != nil {
		t.Fatal(err)
	}
	want, err := CanonicalPath(physical)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ancestor symlink identity differs: alias=%q physical=%q", got, want)
	}
}

func TestCanonicalPath_ResolvesDarwinVarAliasWhenExposed(t *testing.T) {
	varRoot := string(filepath.Separator) + "var"
	privateVarRoot := filepath.Join(string(filepath.Separator), "private", "var")
	varInfo, err := os.Stat(varRoot)
	if err != nil {
		t.Skipf("/var unavailable: %v", err)
	}
	privateInfo, err := os.Stat(privateVarRoot)
	if err != nil || !os.SameFile(varInfo, privateInfo) {
		t.Skip("platform does not expose /var and /private/var as one physical directory")
	}

	base := t.TempDir()
	rel, err := filepath.Rel(varRoot, base)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Skipf("temporary root is not under /var: %q", base)
	}
	alias := filepath.Join(privateVarRoot, rel)
	baseInfo, err := os.Stat(base)
	if err != nil {
		t.Skipf("temporary root disappeared: %v", err)
	}
	aliasInfo, err := os.Stat(alias)
	if err != nil || !os.SameFile(baseInfo, aliasInfo) {
		t.Skip("temporary root does not have a /private/var alias")
	}

	got, err := CanonicalPath(base)
	if err != nil {
		t.Fatal(err)
	}
	aliasGot, err := CanonicalPath(alias)
	if err != nil {
		t.Fatal(err)
	}
	if got != aliasGot {
		t.Fatalf("/var aliases received different identities: %q != %q", got, aliasGot)
	}
}

func TestValidateRepositoryRootRejectsMissingAndRegularFile(t *testing.T) {
	base := t.TempDir()
	regular := filepath.Join(base, "repo.txt")
	if err := os.WriteFile(regular, []byte("not a repository"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(base, "missing-repository")

	for _, root := range []string{missing, regular} {
		if _, err := ValidateRepositoryRoot(root); err == nil {
			t.Errorf("ValidateRepositoryRoot(%q) unexpectedly succeeded", root)
		}
		if _, err := Discover(root); err == nil {
			t.Errorf("Discover(%q) unexpectedly succeeded", root)
		}
	}
}

func TestCanonicalPath_DoesNotCaseFoldDistinctEntries(t *testing.T) {
	base := t.TempDir()
	upper := filepath.Join(base, "CaseRoot")
	lower := filepath.Join(base, "caseroot")
	if err := os.Mkdir(upper, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lower, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			t.Skip("filesystem is case-insensitive")
		}
		t.Fatal(err)
	}
	upperCanonical, err := CanonicalPath(upper)
	if err != nil {
		t.Fatal(err)
	}
	lowerCanonical, err := CanonicalPath(lower)
	if err != nil {
		t.Fatal(err)
	}
	if upperCanonical == lowerCanonical {
		t.Fatalf("distinct case-sensitive roots were folded: %q", upperCanonical)
	}
}
