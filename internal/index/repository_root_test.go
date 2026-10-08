package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/securefile"
)

func TestValidatedRootAliasesKeepIdentity(t *testing.T) {
	base := securityPhysicalTempDir(t)
	root := filepath.Join(base, "repo")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSecurityFile(t, root, "source.go", "package fixture\n")
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	repository, err := validateRoot(alias)
	if err != nil || repository.path != root {
		t.Fatalf("alias root=%+v err=%v", repository, err)
	}
	scan, err := scanRepositoryAtRoot(context.Background(), repository)
	if err != nil || len(scan.files) != 1 || scan.manifest.CanonicalRoot != root {
		t.Fatalf("scan=%+v err=%v", scan, err)
	}
	if projectNameAtRoot(repository.path) != ProjectName(alias) {
		t.Fatal("canonical root changed project identity")
	}
}

func TestValidatedRootNeverBypassesReplacedRootBoundary(t *testing.T) {
	base := securityPhysicalTempDir(t)
	root := filepath.Join(base, "repo")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := validateRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := securityPhysicalTempDir(t)
	writeSecurityFile(t, outside, ".gitignore", "secret\n")
	writeSecurityFile(t, outside, "source.go", "package outside\n")
	if err := os.Rename(root, root+".previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if _, err := scanRepositoryAtRoot(context.Background(), repository); !errors.Is(err, securefile.ErrUnsafePath) {
		t.Fatalf("substituted root: err=%v want ErrUnsafePath", err)
	}
}
