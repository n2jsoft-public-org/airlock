package selfupdate

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// permissionHint tells how to get past a folder airlock cannot write to.
const permissionHint = "run it from an administrator terminal, or reinstall with install.ps1"

// swap moves the running target aside (Windows allows renaming, not overwriting, a running
// executable), then moves newPath in; when that fails, target comes back. An earlier .old that
// still runs cannot be removed: the target then goes to a unique .old-<random> name.
func swap(newPath, target string) error {
	old := target + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		suffix, randErr := randomSuffix()
		if randErr != nil {
			return errors.Join(err, randErr)
		}
		old = target + ".old-" + suffix
	}
	if err := os.Rename(target, old); err != nil {
		return err
	}
	if err := os.Rename(newPath, target); err != nil {
		return errors.Join(err, os.Rename(old, target))
	}
	return nil
}

func randomSuffix() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CleanupOld removes the binaries previous updates moved aside (<target>.old and <target>.old-*).
// One that still runs (another airlock) cannot be removed: that is expected and silent, and the
// next launch tries again. Only unexpected failures are returned.
func CleanupOld(target string) error {
	dir, base := filepath.Split(target)
	entries, err := os.ReadDir(filepath.Clean(dir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var errs []error
	for _, entry := range entries {
		name := entry.Name()
		if name != base+".old" && !strings.HasPrefix(name, base+".old-") {
			continue
		}
		leftover := filepath.Join(dir, name)
		err = os.Remove(leftover)
		if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission) {
			errs = append(errs, fmt.Errorf("the previous airlock (%s) was not removed: %w", leftover, err))
		}
	}
	return errors.Join(errs...)
}
