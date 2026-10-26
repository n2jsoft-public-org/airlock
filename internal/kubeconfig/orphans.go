package kubeconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/platform"
)

// ownerSuffix names the file next to a kubeconfig holding the PID of the airlock that wrote it.
const ownerSuffix = ".owner"

// processAlive reports whether pid runs; a variable so tests can end a process.
var processAlive = platform.ProcessAlive

// RemoveOrphans removes the kubeconfigs of dir left by an airlock that ended without releasing
// them (killed, crashed), and returns their paths. Those of a running airlock, maybe another one,
// are kept. A missing directory has none.
func RemoveOrphans(dir string) (removed []string, err error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list %s: %w", dir, err)
	}
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if owner, ok := readOwner(path); ok && processAlive(owner) {
			continue
		}
		if err := removeWithOwner(path); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, path)
	}
	return removed, errors.Join(errs...)
}

// writeOwner records this process as the owner of the kubeconfig at path.
func writeOwner(path string) error {
	if err := os.WriteFile(path+ownerSuffix, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", path+ownerSuffix, err)
	}
	return nil
}

// readOwner returns the PID owning the kubeconfig at path; ok is false without a valid owner file.
func readOwner(path string) (pid int, ok bool) {
	raw, err := os.ReadFile(path + ownerSuffix) // #nosec G304 -- the owner file of one of our kubeconfigs.
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
	return pid, err == nil && pid > 0
}
