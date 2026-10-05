//go:build unix

package platform

import (
	"errors"
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

// TryLockFile takes the lock of LockFile without waiting: ErrLocked when another process holds it.
func TryLockFile(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}
