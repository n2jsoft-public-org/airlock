package orgconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// RefreshInterval is how long a fetched organization config stays fresh.
const RefreshInterval = time.Hour

// Cache is the last organization config fetched from a remote source, kept between launches.
type Cache struct {
	FetchedAt time.Time `json:"fetchedAt"`
	Source    string    `json:"source"` // Source.Raw the body came from
	ETag      string    `json:"etag,omitempty"`
	// Rejected is why the last body read was refused, "" once a read succeeds: Body is then still
	// the previous valid one. It never holds a token.
	Rejected string `json:"rejected,omitempty"`
	Body     []byte `json:"body,omitempty"`
	// Previous is the body before the last change, kept until the CLI has shown the diff.
	Previous []byte `json:"previous,omitempty"`
}

// LoadCache reads the cache at path; a missing or unreadable file gives an empty Cache.
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

// For is c when it holds a body fetched from source, so a changed organizationConfig never
// applies the old organization's cache.
func (c Cache) For(source string) (Cache, bool) { //nolint:gocritic // Cache by value is the public contract.
	if c.Source != source || len(c.Body) == 0 {
		return Cache{}, false
	}
	return c, true
}

// Stale reports whether c must be refreshed at now.
func (c Cache) Stale(now time.Time) bool { //nolint:gocritic // Cache by value is the public contract.
	return c.FetchedAt.IsZero() || now.Before(c.FetchedAt) || now.Sub(c.FetchedAt) >= RefreshInterval
}

// SaveCache writes c to path through a temporary file, so two airlocks never leave it half written.
func SaveCache(path string, c Cache) (err error) { //nolint:gocritic // Cache by value is the public contract.
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".organization-cache-*.json") // created 0600
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
