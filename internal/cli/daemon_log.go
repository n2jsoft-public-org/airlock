package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/n2jsoft/airlock/internal/appdir"
)

func daemonLogPath() string {
	dir, err := appdir.LogsDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "daemon.log")
}

// openDaemonLog returns the log of `airlock daemon run`; discarded when its directory is unknown.
func openDaemonLog() *daemonLog { return newDaemonLog(daemonLogPath()) }

// daemonLog opens path on its first write, the previous log becoming path.1: a daemon refused by
// the lock of a running one writes nothing, so it leaves that daemon's log in place. An empty path
// discards what is written.
type daemonLog struct {
	file *os.File
	err  error
	path string
	// pending holds the warnings written before the first write.
	pending []byte
	mu      sync.Mutex
	open    bool
}

func newDaemonLog(path string) *daemonLog { return &daemonLog{path: path} }

// warnLookup keeps a failed lookup for the first write, so it never rotates the log by itself.
func (l *daemonLog) warnLookup(consequence string, err error) {
	if err == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending = fmt.Appendf(l.pending, "%s Warning: %s: %v\n", time.Now().Format(time.RFC3339), consequence, err)
}

func (l *daemonLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.open {
		l.open = true
		if l.path != "" {
			l.file, l.err = rotateDaemonLog(l.path)
		}
		if l.file != nil && len(l.pending) > 0 {
			_, l.err = l.file.Write(l.pending)
		}
		l.pending = nil
	}
	if l.err != nil {
		return 0, l.err
	}
	if l.file == nil {
		return len(p), nil
	}
	return l.file.Write(p)
}

func (l *daemonLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file, l.err = nil, os.ErrClosed
	return err
}

func rotateDaemonLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// A first start has no previous log to keep.
	if err := os.Rename(path, path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- airlock's own log.
}
