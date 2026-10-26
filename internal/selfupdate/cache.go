package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// CheckInterval is how long a check of the latest release stays fresh.
const CheckInterval = 24 * time.Hour

// Cache is the last check of the latest release, kept between launches for the notice.
type Cache struct {
	CheckedAt time.Time `json:"checkedAt"`
	Current   string    `json:"current"`
	Latest    string    `json:"latest"`
	Status    Status    `json:"status"`
}

// CacheAt is the cache entry of r checked at now.
func (r *Result) CacheAt(now time.Time) Cache {
	return Cache{CheckedAt: now.UTC(), Current: r.Current, Latest: r.Latest, Status: r.Status}
}

// LoadCache reads the cache at path; a missing or unreadable file gives an empty, stale Cache.
func LoadCache(path string) Cache {
	data, err := os.ReadFile(path) // #nosec G304 -- path is airlock's own cache file.
	if err != nil {
		return Cache{}
	}
	var c Cache
	if err = json.Unmarshal(data, &c); err != nil {
		return Cache{}
	}
	return c
}

// Available returns the newer release the cache found for the current build, or "".
func (c Cache) Available(current string) string {
	if c.Current != current || c.Status != StatusAvailable {
		return ""
	}
	return c.Latest
}

// Stale reports whether the cache must be refreshed for the current build at now.
func (c Cache) Stale(current string, now time.Time) bool {
	return c.Current != current || now.Before(c.CheckedAt) || now.Sub(c.CheckedAt) >= CheckInterval
}

// SaveCache writes c to path.
func SaveCache(path string, c Cache) error {
	return writeJSON(path, c)
}

// writeJSON writes v to path through a temporary file, so two airlocks never leave it half written.
func writeJSON(path string, v any) (err error) {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	defer func() {
		if err == nil {
			return
		}
		if removeErr := os.Remove(tmp.Name()); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()

	_, writeErr := tmp.Write(data)
	if err = errors.Join(writeErr, tmp.Close()); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
