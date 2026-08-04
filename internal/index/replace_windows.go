//go:build windows

package index

import (
	"fmt"
	"os"
)

var (
	windowsRename = os.Rename
	windowsRemove = os.Remove
)

const replacementBackupSuffix = ".backup"

func replacementBackupPath(dbPath string) string {
	return dbPath + replacementBackupSuffix
}

func indexRecoveryNeeded(dbPath string) (bool, error) {
	_, err := os.Stat(replacementBackupPath(dbPath))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect replacement backup %q: %w", replacementBackupPath(dbPath), err)
	}
	return true, nil
}

// recoverIndexReplacement reconciles the deterministic backup left by an
// interrupted Windows replacement. It is called only while the caller owns the
// exclusive database lock.
func recoverIndexReplacement(dbPath string) error {
	backup := replacementBackupPath(dbPath)
	if _, err := os.Stat(backup); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect replacement backup %q: %w", backup, err)
	}

	if _, err := os.Stat(dbPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect canonical graph %q: %w", dbPath, err)
		}
		if err := removeSQLiteSidecars(dbPath); err != nil {
			return fmt.Errorf("remove stale canonical sidecars: %w", err)
		}
		if err := windowsRename(backup, dbPath); err != nil {
			return fmt.Errorf("restore interrupted graph from %q: %w", backup, err)
		}
		return nil
	}

	// Both files exist after a completed install whose backup cleanup was
	// interrupted. Keep the canonical replacement and reconcile the stale copy.
	if err := windowsRemove(backup); err != nil {
		return fmt.Errorf("remove stale replacement backup %q: %w", backup, err)
	}
	return nil
}

// Windows does not provide the same portable overwrite-rename contract as
// Unix. Move the closed old graph to a deterministic adjacent backup, install
// the build, and restore the backup if installation fails. The backup remains
// recoverable until cleanup succeeds, including across process crashes.
func replaceBuiltIndexPlatform(building, dbPath string) error {
	if err := recoverIndexReplacement(dbPath); err != nil {
		return err
	}
	if err := removeSQLiteSidecars(dbPath); err != nil {
		return err
	}

	_, statErr := os.Stat(dbPath)
	if os.IsNotExist(statErr) {
		if err := windowsRename(building, dbPath); err != nil {
			return fmt.Errorf("install replacement: %w", err)
		}
		return nil
	}
	if statErr != nil {
		return statErr
	}

	backup := replacementBackupPath(dbPath)
	if err := windowsRename(dbPath, backup); err != nil {
		return fmt.Errorf("move old graph to backup: %w", err)
	}
	if err := windowsRename(building, dbPath); err != nil {
		if restoreErr := windowsRename(backup, dbPath); restoreErr != nil {
			return fmt.Errorf("install replacement: %w; restore old graph from %q: %v", err, backup, restoreErr)
		}
		return fmt.Errorf("install replacement: %w", err)
	}
	if err := windowsRemove(backup); err != nil {
		return fmt.Errorf("cleanup replacement backup %q: %w", backup, err)
	}
	return nil
}

// Windows cannot portably overwrite an existing file with os.Rename. Removing
// the old manifest first leaves either a missing or a new manifest; both states
// force a rebuild when paired with the graph identity.
func replaceManifestPlatform(temp, manifest string) error {
	if err := os.Remove(manifest); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(temp, manifest)
}
