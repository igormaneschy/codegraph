//go:build !windows

package index

import "os"

func indexRecoveryNeeded(string) (bool, error) { return false, nil }

func recoverIndexReplacement(string) error { return nil }

// os.Rename replaces an existing destination atomically on the supported Unix
// filesystems when both paths share a directory/filesystem. The live database
// is never removed first; destination sidecars are safe to remove because the
// caller checkpointed and closed the old store before reaching this function.
func replaceBuiltIndexPlatform(building, dbPath string) error {
	if err := removeSQLiteSidecars(dbPath); err != nil {
		return err
	}
	return os.Rename(building, dbPath)
}

// A manifest is installed with one rename too. If the process dies between
// graph and manifest replacement, graph identity makes the mismatch a rebuild
// signal rather than a false no-op.
func replaceManifestPlatform(temp, manifest string) error {
	return os.Rename(temp, manifest)
}
