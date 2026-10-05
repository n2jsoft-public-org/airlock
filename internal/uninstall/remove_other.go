//go:build !windows

package uninstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// removeBinary deletes path at once: a running program keeps its open copy until it exits.
// install.sh only adds to standard folders such as ~/.local/bin, so removeDir is never set here.
func removeBinary(path string, _ bool) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("cannot remove %s: permission denied (run sudo airlock uninstall)", path)
		}
		return fmt.Errorf("cannot remove %s: %w", path, err)
	}
	return nil
}

// removeFromUserPath is Windows only; the plan never asks for it elsewhere.
func removeFromUserPath(_ string) error {
	return errors.New("no user PATH outside Windows")
}

// UserPath is Windows only.
var UserPath func() (string, error)

// OtherInstances is Windows only: elsewhere a running airlock does not keep its binary in place.
func OtherInstances(_ string, _ ...int) (int, error) {
	return 0, nil
}
