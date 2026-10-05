//go:build unix

package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// checkPrivateDir refuses dir unless the current user owns it and nobody else may use it (0700):
// another user could otherwise reach the daemon's socket, or put their own in its place.
func checkPrivateDir(dir string) error {
	return checkDirOwner(dir, os.Getuid(), true)
}

// checkSocketDir refuses to dial a daemon whose directory belongs to another user; a missing
// directory is no daemon, not an error.
func checkSocketDir(dir string) error {
	err := checkDirOwner(dir, os.Getuid(), false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// checkDirOwner refuses dir unless uid owns it and, when private, its mode is 0700.
func checkDirOwner(dir string, uid int, private bool) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read the owner of the daemon directory %s", dir)
	}
	if int64(stat.Uid) != int64(uid) {
		return fmt.Errorf("the daemon directory %s belongs to another user (uid %d): remove it or make it yours", dir, stat.Uid)
	}
	if private && info.Mode().Perm() != 0o700 {
		return fmt.Errorf("the daemon directory %s must be private to you (mode 0700), it is %#o", dir, info.Mode().Perm())
	}
	return nil
}

// restrictSocket lets only the current user use the socket.
func restrictSocket(path string) error {
	return os.Chmod(path, 0o600)
}
