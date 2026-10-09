package bench

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/securefile"
)

func TestMatrixEditRestoresAfterLostWorkerReply(t *testing.T) {
	root := physicalTemp(t)
	original := []byte("package test\n")
	target := filepath.Join(root, "source.go")
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	edit := MatrixEdit{Root: root, Path: "source.go", Backup: filepath.Join(physicalTemp(t), "original"), ExpectedSHA: digestBytes(original), Suffix: "// probe\n"}
	evidence, err := EditMatrixInput(edit)
	if err != nil || evidence.OriginalSHA != edit.ExpectedSHA || evidence.EditedSHA == evidence.OriginalSHA {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	// Restore does not require a successful edit reply or its edited digest.
	edit.Restore = true
	if _, err := EditMatrixInput(edit); err != nil {
		t.Fatal(err)
	}
	restored, err := securefile.ReadFile(target)
	if err != nil || string(restored) != string(original) {
		t.Fatalf("restored=%q err=%v", restored, err)
	}
	if _, err := EditMatrixInput(edit); err != nil {
		t.Fatalf("idempotent restore: %v", err)
	}
}

func TestMatrixEditRejectsConcurrentChangesAndSymlinks(t *testing.T) {
	root := physicalTemp(t)
	target := filepath.Join(root, "source.go")
	original := []byte("original")
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	edit := MatrixEdit{Root: root, Path: "source.go", Backup: filepath.Join(physicalTemp(t), "backup"), ExpectedSHA: digestBytes(original), Suffix: " suffix"}
	if _, err := EditMatrixInput(edit); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("concurrent"), 0o600); err != nil {
		t.Fatal(err)
	}
	edit.Restore = true
	if _, err := EditMatrixInput(edit); err == nil {
		t.Fatal("concurrent bytes overwritten")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(edit.Backup, target); err != nil {
		t.Fatal(err)
	}
	if _, err := EditMatrixInput(edit); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestMatrixInputAdmissionDoesNotReadPrivateFiles(t *testing.T) {
	for _, input := range []string{"../outside.go", "/outside.go", ".env", "dir/.env.local", "node_modules/pkg/a.ts", "vendor/pkg/a.go", ".git/config", "./a.go"} {
		_, err := EditMatrixInput(MatrixEdit{Root: physicalTemp(t), Path: input, Backup: filepath.Join(physicalTemp(t), "backup"), ExpectedSHA: digestBytes(nil), Suffix: "probe"})
		if err == nil {
			t.Fatalf("unsafe path %q admitted", input)
		}
	}
	root := physicalTemp(t)
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EditMatrixInput(MatrixEdit{Root: root, Path: "a.go", Backup: filepath.Join(root, "backup"), ExpectedSHA: digestBytes([]byte("source")), Suffix: "probe"}); err == nil {
		t.Fatal("in-root backup accepted")
	}
}
