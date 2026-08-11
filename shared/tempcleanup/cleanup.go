package tempcleanup

import (
	"errors"
	"os"
	"path/filepath"
)

// RemoveContents removes entries inside directory without removing the
// directory itself. A missing directory is treated as already clean.
func RemoveContents(directory string) (int, error) {
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	removed := 0
	var cleanupErrors []error
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(directory, entry.Name())); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(cleanupErrors...)
}
