package index

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

var sqliteSidecarSuffixes = []string{"-wal", "-shm", "-journal"}

// removeSQLiteSidecars removes only files that SQLite can regenerate for base.
// The main database is intentionally not touched here; callers use this helper
// before replacement or while cleaning an explicitly failed build.
func removeSQLiteSidecars(base string) error {
	var errs []error
	for _, suffix := range sqliteSidecarSuffixes {
		if err := os.Remove(base + suffix); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("%s: %w", suffix, err))
		}
	}
	return errors.Join(errs...)
}

// removeIndexArtifacts cleans a failed build, its SQLite sidecars, and a stale
// replacement backup when the canonical database is already present. It never
// removes the live database path; a Windows backup is preserved when the live
// path is absent so startup recovery can restore it.
func removeIndexArtifacts(building string) error {
	var errs []error
	if err := os.Remove(building); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("building file: %w", err))
	}
	if err := removeSQLiteSidecars(building); err != nil {
		errs = append(errs, err)
	}
	if err := removeReplacementArtifact(building); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func removeManifestArtifacts(building string) error {
	var errs []error
	for _, path := range []string{building, building + ".tmp"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

func removeReplacementArtifact(building string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	dbPath, ok := strings.CutSuffix(building, BuildingSuffix)
	if !ok {
		return nil
	}
	backup := dbPath + ".backup"
	if _, err := os.Stat(backup); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect replacement backup %q: %w", backup, err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			// The backup may be the only remaining copy of the live graph.
			return nil
		}
		return fmt.Errorf("inspect canonical graph %q: %w", dbPath, err)
	}
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale replacement backup %q: %w", backup, err)
	}
	return nil
}

// RecoverIndex reconciles a deterministic interrupted replacement before a
// production reader opens the canonical database. Recovery is itself exclusive
// so a missing canonical file can never be created by graph.Open before a stale
// backup has been considered.
func RecoverIndex(dbPath string) error {
	abs, err := normalizeIndexPath(dbPath)
	if err != nil {
		return err
	}
	lock, err := AcquireExclusiveLock(abs)
	if err != nil {
		return err
	}
	needed, err := indexRecoveryNeeded(abs)
	if err != nil {
		return errors.Join(err, lock.Release())
	}
	if !needed {
		return lock.Release()
	}
	recoveryErr := recoverIndexReplacement(abs)
	releaseErr := lock.Release()
	return errors.Join(recoveryErr, releaseErr)
}
