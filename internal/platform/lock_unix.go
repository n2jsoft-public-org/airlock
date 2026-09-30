//go:build unix

package platform

import (
	"os"
	"syscall"
)

// LockFile blocks until this process holds an exclusive lock on file, shared by every process.
func LockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

// UnlockFile releases the lock taken by LockFile.
func UnlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
