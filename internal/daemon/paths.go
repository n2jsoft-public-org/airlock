package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Paths are the files of the daemon.
type Paths struct {
	Dir       string
	Socket    string
	Lock      string
	Processes string
}

// maxSocketPath is the longest socket path the OS accepts (sun_path, without its final NUL).
func maxSocketPath() int {
	if runtime.GOOS == "darwin" {
		return 103
	}
	return 107
}

// DefaultPaths are the daemon's files in airlock's cache directory.
func DefaultPaths() (Paths, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return Paths{}, fmt.Errorf("failed to locate the cache directory: %w", err)
	}
	return PathsIn(filepath.Join(cache, "airlock", "daemon"))
}

// PathsIn returns the daemon's files in dir; an error when the socket path is too long for the OS.
func PathsIn(dir string) (Paths, error) {
	p := Paths{
		Dir: dir, Socket: filepath.Join(dir, "daemon.sock"), Lock: filepath.Join(dir, "daemon.lock"),
		Processes: filepath.Join(dir, "processes.json"),
	}
	if len(p.Socket) > maxSocketPath() {
		return Paths{}, fmt.Errorf("the daemon socket path %s is longer than the %d characters the system allows", p.Socket, maxSocketPath())
	}
	return p, nil
}
