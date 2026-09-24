package ideadatasource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	leaseFileSuffix = ".lease"
	lockFileName    = ".lock"
)

var (
	// leaseRoot returns the directory holding session leases; overridden in tests.
	leaseRoot = defaultLeaseRoot
	// currentPID identifies the session owning a lease; overridden in tests.
	currentPID = os.Getpid
)

// lease records that a running session uses a data source.
// Leases live outside .idea so that IntelliJ rewriting dataSources.xml cannot drop them.
type lease struct {
	CreatedAt  time.Time  `json:"createdAt"`
	DataSource DataSource `json:"dataSource"`
	PID        int        `json:"pid"`
}

// leaseStore holds the leases of one data source in one project.
type leaseStore struct {
	dir string
}

func defaultLeaseRoot() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "acme", "ide-datasources"), nil
}

func openLeaseStore(projectDir, key string) (leaseStore, error) {
	root, err := leaseRoot()
	if err != nil {
		return leaseStore{}, fmt.Errorf("failed to resolve lease directory: %w", err)
	}
	absProject, err := filepath.Abs(projectDir)
	if err != nil {
		return leaseStore{}, fmt.Errorf("failed to resolve project directory: %w", err)
	}

	sum := sha256.Sum256([]byte(absProject + "|" + key))
	dir := filepath.Join(root, hex.EncodeToString(sum[:12]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return leaseStore{}, fmt.Errorf("failed to create lease directory: %w", err)
	}

	return leaseStore{dir: dir}, nil
}

// lock takes an exclusive, cross-process lock on the store and returns its release function.
func (s leaseStore) lock() (unlock func() error, err error) {
	file, err := os.OpenFile(filepath.Join(s.dir, lockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to lock %s: %w", file.Name(), err), file.Close())
	}

	return func() error {
		return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
	}, nil
}

func (s leaseStore) leasePath(pid int) string {
	return filepath.Join(s.dir, strconv.Itoa(pid)+leaseFileSuffix)
}

func (s leaseStore) acquire(ds *DataSource) error {
	pid := currentPID()
	payload, err := json.Marshal(lease{PID: pid, CreatedAt: time.Now(), DataSource: *ds})
	if err != nil {
		return fmt.Errorf("failed to encode lease: %w", err)
	}
	if err := os.WriteFile(s.leasePath(pid), payload, 0o600); err != nil {
		return fmt.Errorf("failed to write lease: %w", err)
	}
	return nil
}

func (s leaseStore) release() error {
	if err := os.Remove(s.leasePath(currentPID())); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove lease: %w", err)
	}
	return nil
}

// active returns leases held by running processes, newest first, and deletes the stale ones.
func (s leaseStore) active() ([]lease, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("failed to list leases: %w", err)
	}

	var leases []lease
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), leaseFileSuffix) {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())

		var current lease
		payload, readErr := os.ReadFile(path) // #nosec G304 -- path is built from our own lease directory.
		if readErr == nil && json.Unmarshal(payload, &current) == nil && processAlive(current.PID) {
			leases = append(leases, current)
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("failed to remove stale lease: %w", err)
		}
	}

	sort.Slice(leases, func(i, j int) bool { return leases[i].CreatedAt.After(leases[j].CreatedAt) })
	return leases, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
