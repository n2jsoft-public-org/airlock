//go:build !windows

package selfupdate

import "os"

// permissionHint tells how to get past a folder airlock cannot write to; sudo drops GH_TOKEN and
// root has no gh login, so the token travels explicitly.
const permissionHint = `run sudo GH_TOKEN="$(gh auth token)" airlock update, or reinstall with make install`

// swap moves newPath over target in one step; a running airlock keeps its old copy until it exits.
func swap(newPath, target string) error {
	return os.Rename(newPath, target)
}

// CleanupOld does nothing outside Windows, where a running binary can be replaced directly.
func CleanupOld(_ string) error {
	return nil
}
