package securefile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenReadRegularFile(t *testing.T) {
	path := filepath.Join(physicalTempDir(t), "source.txt")
	if err := os.WriteFile(path, []byte("regular\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "regular\n" {
		t.Fatalf("got %q, want regular file content", data)
	}
}

func TestOpenReadRejectsSymlinkLeaf(t *testing.T) {
	dir := physicalTempDir(t)
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := OpenRead(link); err == nil || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink leaf error=%v, want ErrUnsafePath", err)
	}
}

func TestOpenReadDescriptorSurvivesLeafReplacement(t *testing.T) {
	dir := physicalTempDir(t)
	path := filepath.Join(dir, "source.txt")
	outside := filepath.Join(physicalTempDir(t), "outside.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenRead(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		_ = f.Close()
		t.Skipf("symlink creation unavailable: %v", err)
	}
	data, readErr := io.ReadAll(f)
	closeErr := f.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if string(data) != "original\n" {
		t.Fatalf("descriptor read %q, want original content", data)
	}
}

func TestWritePrivateReplacesSymlinkWithoutTouchingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		if err := WritePrivate(filepath.Join(t.TempDir(), "artifact.txt"), []byte("data")); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Windows WritePrivate error=%v, want ErrUnsupported", err)
		}
		return
	}
	dir := physicalTempDir(t)
	target := filepath.Join(physicalTempDir(t), "target.txt")
	destination := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(target, []byte("keep target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, destination); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := WritePrivate(destination, []byte("new artifact\n")); err != nil {
		t.Fatal(err)
	}
	targetData, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(targetData) != "keep target\n" {
		t.Fatalf("symlink target changed to %q", targetData)
	}
	artifactData, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(artifactData) != "new artifact\n" {
		t.Fatalf("destination content=%q", artifactData)
	}
	info, err := os.Lstat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("destination mode=%s, want regular file", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("destination mode=%o, want 600", info.Mode().Perm())
	}
}

func TestMkdirAllPrivateRejectsParentSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		if err := MkdirAllPrivate(filepath.Join(t.TempDir(), "nested")); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Windows MkdirAllPrivate error=%v, want ErrUnsupported", err)
		}
		return
	}
	realParent := physicalTempDir(t)
	root := physicalTempDir(t)
	link := filepath.Join(root, "link")
	if err := os.Symlink(realParent, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := MkdirAllPrivate(filepath.Join(link, "nested")); err == nil || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("parent symlink error=%v, want ErrUnsafePath", err)
	}
}

func TestWritePrivateRejectsTemporaryEntrySubstitution(t *testing.T) {
	if runtime.GOOS == "windows" {
		return
	}
	dir := physicalTempDir(t)
	destination := filepath.Join(dir, "artifact.txt")
	outside := filepath.Join(physicalTempDir(t), "outside.txt")
	if err := os.WriteFile(outside, []byte("keep outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldHook := beforePrivateRenameHook
	t.Cleanup(func() { beforePrivateRenameHook = oldHook })
	beforePrivateRenameHook = func(temp string) {
		if err := os.Remove(temp); err != nil {
			t.Fatalf("remove temporary entry: %v", err)
		}
		if err := os.Symlink(outside, temp); err != nil {
			t.Fatalf("replace temporary entry: %v", err)
		}
	}
	err := WritePrivate(destination, []byte("new artifact\n"))
	if err == nil || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("temporary substitution error=%v, want ErrUnsafePath", err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination after rejected substitution stat=%v, want absent", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep outside\n" {
		t.Fatalf("outside target changed to %q", data)
	}
}

func TestWritePrivateRejectsRegularTemporaryEntrySubstitution(t *testing.T) {
	if runtime.GOOS == "windows" {
		return
	}
	dir := physicalTempDir(t)
	destination := filepath.Join(dir, "artifact.txt")
	oldHook := beforePrivateRenameHook
	t.Cleanup(func() { beforePrivateRenameHook = oldHook })
	beforePrivateRenameHook = func(temp string) {
		if err := os.Remove(temp); err != nil {
			t.Fatalf("remove temporary entry: %v", err)
		}
		if err := os.WriteFile(temp, []byte("attacker entry\n"), 0o600); err != nil {
			t.Fatalf("replace temporary entry: %v", err)
		}
	}
	err := WritePrivate(destination, []byte("new artifact\n"))
	if err == nil || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("temporary regular substitution error=%v, want ErrUnsafePath", err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination after rejected substitution stat=%v, want absent", err)
	}
}

func TestPrivateDirectoryCleanupRejectsReplacedEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := MkdirTempPrivate(t.TempDir(), "codegraph-private-"); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Windows MkdirTempPrivate error=%v, want ErrUnsupported", err)
		}
		return
	}
	parent := physicalTempDir(t)
	private, err := MkdirTempPrivate(parent, "codegraph-private-")
	if err != nil {
		t.Fatal(err)
	}
	original := private.Path()
	quarantine := original + ".quarantine"
	t.Cleanup(func() {
		_ = os.Remove(quarantine)
		_ = os.Remove(original)
		_ = os.RemoveAll(quarantine)
	})
	if err := os.WriteFile(filepath.Join(original, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, quarantine); err != nil {
		t.Fatal(err)
	}
	replacement := physicalTempDir(t)
	if err := os.Symlink(replacement, original); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := private.Cleanup(); err == nil || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("replaced private directory cleanup error=%v, want ErrUnsafePath", err)
	}
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("quarantined original snapshot was removed: %v", err)
	}
	if info, err := os.Lstat(original); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("replacement snapshot entry=%v info=%v, want retained symlink", err, info)
	}
}

func TestMkdirAllPrivateRejectsWorldWritableExistingDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		if err := MkdirAllPrivate(filepath.Join(t.TempDir(), "nested")); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Windows MkdirAllPrivate error=%v, want ErrUnsupported", err)
		}
		return
	}
	destination := filepath.Join(physicalTempDir(t), "quality-run")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destination, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAllPrivate(destination); err == nil || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("world-writable existing destination error=%v, want ErrUnsafePath", err)
	}
}

func physicalTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return physical
}
