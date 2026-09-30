package platform

import (
	"os"

	"golang.org/x/sys/windows"
)

// LockFile blocks until this process holds an exclusive lock on file, shared by every process.
func LockFile(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}

// UnlockFile releases the lock taken by LockFile.
func UnlockFile(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, new(windows.Overlapped))
}
