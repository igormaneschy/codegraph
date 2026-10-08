package securefile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadDir_SortedEntriesWithoutReadingContents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("descriptor directory reads remain unsupported on Windows (R01)")
	}
	root := physicalTempDir(t)
	for _, name := range []string{"z.txt", "a.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture\n"), 0o000); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := ReadDir(root)
	if err != nil || len(entries) != 2 || entries[0].Name() != "a.txt" || entries[1].Name() != "z.txt" {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func TestReadDir_RejectsLeafAndParentSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("descriptor directory reads remain unsupported on Windows (R01)")
	}
	outside := physicalTempDir(t)
	if err := os.Mkdir(filepath.Join(outside, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := physicalTempDir(t)
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, path := range []string{alias, filepath.Join(alias, "child")} {
		if _, err := ReadDir(path); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("directory alias %q error=%v", path, err)
		}
	}
}

func TestReadDir_MissingDirectoryIsReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("descriptor directory reads remain unsupported on Windows (R01)")
	}
	if _, err := ReadDir(filepath.Join(physicalTempDir(t), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory error=%v", err)
	}
}
