package connections

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// catalogVersion is the layout version of database-catalog.json.
const catalogVersion = 1

// CatalogEntry is what the catalog knows of one server: its databases at its last connection.
type CatalogEntry struct {
	SeenAt    time.Time `json:"seen_at"`
	Databases []string  `json:"databases"`
}

// catalogDocument is the JSON of the catalog file.
//
//nolint:govet // fieldalignment: the field order is the file's key order.
type catalogDocument struct {
	Version int                     `json:"version"`
	Servers map[string]CatalogEntry `json:"servers"`
}

// Catalog keeps database-catalog.json: the databases seen on each server, keyed like the entries
// of saved-connections.json, so that agents find a database without connecting to every server.
// It is a cache: a missing, unreadable or newer file reads as empty and is rewritten.
type Catalog struct {
	now  func() time.Time
	path string
}

// NewCatalog keeps the catalog in path; an empty path holds nothing and writes nothing.
func NewCatalog(path string) *Catalog {
	return &Catalog{path: path, now: time.Now}
}

// List returns the entries of the catalog by key; empty when the file is missing or unreadable.
func (c *Catalog) List() map[string]CatalogEntry {
	servers := map[string]CatalogEntry{}
	if c.path == "" {
		return servers
	}
	raw, err := os.ReadFile(c.path) // #nosec G304 -- the path is the user's own file.
	if err != nil {
		return servers
	}
	var doc catalogDocument
	if json.Unmarshal(raw, &doc) != nil || doc.Version != catalogVersion || doc.Servers == nil {
		return servers
	}
	return doc.Servers
}

// Remember records databases as the databases of the server key, seen now.
func (c *Catalog) Remember(key string, databases []string) error {
	databases = slices.Clone(databases)
	slices.Sort(databases)
	return c.update(func(servers map[string]CatalogEntry) bool {
		servers[strings.TrimSpace(key)] = CatalogEntry{SeenAt: c.now().UTC(), Databases: slices.Compact(databases)}
		return true
	})
}

// Forget drops the server key; a missing one writes nothing.
func (c *Catalog) Forget(key string) error {
	return c.update(func(servers map[string]CatalogEntry) bool {
		key = strings.TrimSpace(key)
		_, found := servers[key]
		delete(servers, key)
		return found
	})
}

// update applies change to the catalog under the lock, then writes it when change reports a change.
func (c *Catalog) update(change func(servers map[string]CatalogEntry) bool) error {
	if c.path == "" {
		return nil
	}
	unlock, err := lockFile(c.path)
	if err != nil {
		return err
	}
	defer unlock()
	servers := c.List()
	if !change(servers) {
		return nil
	}
	data, err := json.MarshalIndent(&catalogDocument{Version: catalogVersion, Servers: servers}, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode %s: %w", c.path, err)
	}
	return writeAtomic(c.path, append(data, '\n'))
}
