package selfupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// swap moves the running target aside (Windows allows renaming, not overwriting, a running
// executable), then moves newPath in; when that fails, target comes back.
func swap(newPath, target string) error {
	old := target + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(target, old); err != nil {
		return err
	}
	if err := os.Rename(newPath, target); err != nil {
		return errors.Join(err, os.Rename(old, target))
	}
	return nil
}

// CleanupOld removes the binary a previous update moved aside. It fails while that binary still
// runs (another airlock); the next launch tries again.
func CleanupOld(target string) error {
	old := target + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the previous airlock (%s) was not removed: %w", old, err)
	}
	return nil
}
