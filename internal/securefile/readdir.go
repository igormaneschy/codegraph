package securefile

import (
	"errors"
	"os"
	"sort"
)

// ReadDir lists entries through a no-follow directory descriptor, not a
// validation-then-reopen path. It never reads the contents of the entries.
func ReadDir(path string) ([]os.DirEntry, error) {
	directory, err := openReadDirectory(path)
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(-1)
	if err := errors.Join(readErr, directory.Close()); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}
