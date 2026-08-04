//go:build windows

package index

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsReplace_InstallFailureRestoresPreviousGraph(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "graph.db")
	building := dbPath + BuildingSuffix
	writeWindowsTestFile(t, dbPath, "old graph")
	writeWindowsTestFile(t, building, "new graph")

	originalRename, originalRemove := windowsRename, windowsRemove
	t.Cleanup(func() {
		windowsRename, windowsRemove = originalRename, originalRemove
	})
	windowsRename = func(old, new string) error {
		if old == building && new == dbPath {
			return errors.New("injected install failure")
		}
		return os.Rename(old, new)
	}

	if err := replaceBuiltIndexPlatform(building, dbPath); err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	if got := readWindowsTestFile(t, dbPath); got != "old graph" {
		t.Fatalf("install failure changed canonical graph to %q", got)
	}
	if _, err := os.Stat(replacementBackupPath(dbPath)); !os.IsNotExist(err) {
		t.Fatalf("restored install failure left backup, stat err=%v", err)
	}
}

func TestWindowsReplace_RestoreFailureLeavesNoEmptyCanonical(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "graph.db")
	building := dbPath + BuildingSuffix
	backup := replacementBackupPath(dbPath)
	writeWindowsTestFile(t, dbPath, "old graph")
	writeWindowsTestFile(t, building, "new graph")

	originalRename, originalRemove := windowsRename, windowsRemove
	t.Cleanup(func() {
		windowsRename, windowsRemove = originalRename, originalRemove
	})
	windowsRename = func(old, new string) error {
		if old == building && new == dbPath {
			return errors.New("injected install failure")
		}
		if old == backup && new == dbPath {
			return errors.New("injected restore failure")
		}
		return os.Rename(old, new)
	}

	if err := replaceBuiltIndexPlatform(building, dbPath); err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("restore failure left a canonical file, stat err=%v", err)
	}
	if got := readWindowsTestFile(t, backup); got != "old graph" {
		t.Fatalf("restore failure lost backup graph: %q", got)
	}

	windowsRename = originalRename
	if err := recoverIndexReplacement(dbPath); err != nil {
		t.Fatalf("startup recovery after restore failure: %v", err)
	}
	if got := readWindowsTestFile(t, dbPath); got != "old graph" {
		t.Fatalf("recovered graph=%q, want old graph", got)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("recovery left backup, stat err=%v", err)
	}
}

func TestWindowsReplace_InterruptedMoveRestoresBeforeOpen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "graph.db")
	backup := replacementBackupPath(dbPath)
	writeWindowsTestFile(t, backup, "old graph")

	readerLock, err := AcquireReaderLock(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer readerLock.Release()
	if got := readWindowsTestFile(t, dbPath); got != "old graph" {
		t.Fatalf("recovered graph=%q, want old graph", got)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("interrupted move backup remains, stat err=%v", err)
	}
}

func TestWindowsReplace_ReportsPersistentBackupCleanupFailure(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "graph.db")
	building := dbPath + BuildingSuffix
	backup := replacementBackupPath(dbPath)
	writeWindowsTestFile(t, dbPath, "old graph")
	writeWindowsTestFile(t, building, "new graph")

	originalRename, originalRemove := windowsRename, windowsRemove
	t.Cleanup(func() {
		windowsRename, windowsRemove = originalRename, originalRemove
	})
	windowsRemove = func(path string) error {
		if path == backup {
			return errors.New("injected backup cleanup failure")
		}
		return os.Remove(path)
	}

	if err := replaceBuiltIndexPlatform(building, dbPath); err == nil {
		t.Fatal("backup cleanup failure was ignored")
	}
	if got := readWindowsTestFile(t, dbPath); got != "new graph" {
		t.Fatalf("successful install was not retained: %q", got)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup should remain for deterministic reconciliation: %v", err)
	}

	windowsRemove = originalRemove
	if err := recoverIndexReplacement(dbPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("stale backup was not reconciled, stat err=%v", err)
	}
}

func writeWindowsTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readWindowsTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
