//go:build !windows

package selfupdate

import "os"

// swap moves newPath over target in one step; a running airlock keeps its old copy until it exits.
func swap(newPath, target string) error {
	return os.Rename(newPath, target)
}

// CleanupOld does nothing outside Windows, where a running binary can be replaced directly.
func CleanupOld(_ string) error {
	return nil
}
