// Package platform holds the few operating system primitives that differ between Unix and
// Windows: cross-process file locks, process liveness and stopping a process with its children.
package platform

import "errors"

// ErrLocked reports a lock another process holds.
var ErrLocked = errors.New("locked by another process")
