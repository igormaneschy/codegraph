//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package index

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Lordymine/codegraph/internal/securefile"
)

func TestSimilarity_RejectsFIFOReplacementWithoutBlocking(t *testing.T) {
	root := securityPhysicalTempDir(t)
	const rel = "source.rb"
	writeSecurityFile(t, root, rel, "def alpha(x)\n x + 1\nend\n")
	path := filepath.Join(root, rel)
	nodes, _, err := ExtractDefinitionsChecked("p", SourceFile{AbsPath: path, RelPath: rel, Lang: LangRuby})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	assertSimilarityReadRejected(t, root, nodes, securefile.ErrNotRegular)
}
