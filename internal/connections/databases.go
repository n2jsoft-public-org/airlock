package connections

import (
	"fmt"
	"strings"

	"github.com/n2jsoft/airlock/internal/directdb"
)

// Databases is the direct databases of a Store: what the dashboard, the runner and the `connection`
// commands see of it. Jumphost entries are never listed, written or removed through it.
type Databases struct {
	store *Store
}

// Databases is the direct databases of s.
func (s *Store) Databases() *Databases {
	return &Databases{store: s}
}

// Store is the store the databases live in.
func (d *Databases) Store() *Store {
	return d.store
}

// Path is the file of the store.
func (d *Databases) Path() string {
	return d.store.path
}

// List returns the valid direct databases sorted by name; invalid entries are left out (`airlock
// connection list` shows them).
func (d *Databases) List() ([]directdb.Database, error) {
	entries, err := d.store.List()
	if err != nil {
		return nil, err
	}
	dbs := make([]directdb.Database, 0, len(entries))
	for idx := range entries {
		if entries[idx].Direct != nil {
			dbs = append(dbs, *entries[idx].Direct)
		}
	}
	return dbs, nil
}

// Get returns the direct database named name; false when there is none. An invalid entry named
// name is reported as its error, so that opening it tells what to fix.
func (d *Databases) Get(name string) (directdb.Database, bool, error) {
	entry, found, err := d.store.Get(name)
	switch {
	case err != nil:
		return directdb.Database{}, false, err
	case !found || entry.Jumphost != nil:
		return directdb.Database{}, false, nil
	case entry.Invalid != nil:
		return directdb.Database{}, false, entry.Invalid
	}
	return *entry.Direct, true, nil
}

// Put normalizes and validates db, then writes it: added when previousName is empty, else in place
// of previousName, renamed when db.Name differs. The remembered choices of the entry are kept, and
// carried on a rename. Adding or renaming over another entry wraps directdb.ErrExists; a
// previousName that is no direct database wraps directdb.ErrNotFound.
func (d *Databases) Put(previousName string, db directdb.Database) error { //nolint:gocritic // hugeParam: db is a copy the store normalizes
	db.Normalize()
	if err := db.Validate(); err != nil {
		return err
	}
	previousName = strings.TrimSpace(previousName)
	return d.store.update(func(entries map[string]Entry) error {
		var choices Choices
		if previousName != "" {
			previous, found := entries[previousName]
			if !found || previous.Direct == nil {
				return fmt.Errorf("database %q %w", previousName, directdb.ErrNotFound)
			}
			if !directdb.ConnectionChanged(previous.Direct, &db) {
				choices = previous.Choices
			}
		}
		if _, taken := entries[db.Name]; taken && db.Name != previousName {
			return fmt.Errorf("database %q %w", db.Name, directdb.ErrExists)
		}
		delete(entries, previousName)
		entries[db.Name] = Entry{Key: db.Name, Direct: &db, Choices: choices}
		return nil
	})
}

// PutAll writes dbs in one write, each in place of the entry of the same name: the remembered
// choices of a direct database are kept while its connection is the same (only the environment may
// differ), else dropped; an invalid entry is replaced. Every database is normalized and validated
// first; an invalid one writes nothing.
func (d *Databases) PutAll(dbs []directdb.Database) error {
	valid := make([]directdb.Database, len(dbs))
	for idx := range dbs {
		db := dbs[idx]
		db.Normalize()
		if err := db.Validate(); err != nil {
			return fmt.Errorf("database %q: %w", db.Name, err)
		}
		valid[idx] = db
	}
	return d.store.update(func(entries map[string]Entry) error {
		for idx := range valid {
			db := valid[idx]
			entry := Entry{Key: db.Name, Direct: &db}
			if previous, found := entries[db.Name]; found && previous.Direct != nil && !directdb.ConnectionChanged(previous.Direct, &db) {
				entry.Choices = previous.Choices
			}
			entries[db.Name] = entry
		}
		return nil
	})
}

// Delete removes the direct database named name with its remembered choices, or the invalid entry
// of that name; a missing one is not an error and writes nothing.
func (d *Databases) Delete(name string) error {
	name = strings.TrimSpace(name)
	return d.store.update(func(entries map[string]Entry) error {
		entry, found := entries[name]
		if !found || entry.Jumphost != nil {
			return errUnchanged
		}
		delete(entries, name)
		return nil
	})
}
