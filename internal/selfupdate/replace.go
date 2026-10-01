package selfupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Executable returns the path of the running airlock binary, symlinks resolved, so an update
// replaces the real file and keeps the links to it.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot find the running airlock: %w", err)
	}
	return resolveTarget(exe)
}

func resolveTarget(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("cannot find the running airlock: %w", err)
	}
	return resolved, nil
}

// Replace swaps target for binary, keeping target's file mode. The new binary is written next to
// target first, so a failure leaves target untouched.
func Replace(target string, binary []byte) (err error) {
	info, err := os.Stat(target)
	if err != nil {
		return replaceError(target, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".airlock-update-*")
	if err != nil {
		return replaceError(target, err)
	}
	// Once swapped the temporary file is gone; before that, a failure leaves it to remove.
	defer func() {
		if err == nil {
			return
		}
		if removeErr := os.Remove(tmp.Name()); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()

	_, writeErr := tmp.Write(binary)
	closeErr := tmp.Close()
	chmodErr := os.Chmod(tmp.Name(), info.Mode().Perm()) // #nosec G302 -- the new binary keeps the mode of the one it replaces.
	if err = errors.Join(writeErr, closeErr, chmodErr); err != nil {
		return replaceError(target, err)
	}
	if err = swap(tmp.Name(), target); err != nil {
		return replaceError(target, err)
	}
	return nil
}

func replaceError(target string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("cannot replace %s: permission denied (re-run with sudo, or reinstall with make install)", target)
	}
	return fmt.Errorf("cannot replace %s: %w", target, err)
}
