package directdb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// fileVersion is the layout version of databases.json and of the share strings.
const fileVersion = 1

var (
	// ErrExists refuses to add or rename a database over another one.
	ErrExists = errors.New("already exists")
	// ErrNotFound reports a database to update that the file does not hold.
	ErrNotFound = errors.New("not found")

	errNoPath       = errors.New("the location of databases.json is unknown")
	errTrailingData = errors.New("unexpected content after the end of the JSON document")
)

// NotFoundError reports that no direct database is named name; errors.Is(err, ErrNotFound) holds.
func NotFoundError(name string) error {
	return notFoundError{name: name}
}

// notFoundError keeps its own message while matching ErrNotFound.
type notFoundError struct{ name string }

func (e notFoundError) Error() string {
	return fmt.Sprintf("no direct database named %s (see airlock db list)", e.name)
}

func (notFoundError) Is(target error) bool { return target == ErrNotFound }

// fileLayout is the JSON document of databases.json; Name of each database is its key.
//
//nolint:govet // fieldalignment: the field order is the file's key order.
type fileLayout struct {
	Version   int                 `json:"version"`
	Databases map[string]Database `json:"databases"`
}

// Store keeps the direct databases in a JSON file, created on the first write. Every write reads
// the file again first, so two airlock processes only lose a change made in the same instant.
type Store struct {
	path string
}

// NewStore keeps the databases in path; an empty path holds none and cannot be written.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Path is the file of the store.
func (s *Store) Path() string {
	return s.path
}

// List returns the databases sorted by name. A missing or empty file holds none; an invalid one is
// an error naming the file, the database and the field.
func (s *Store) List() ([]Database, error) {
	databases, err := s.read()
	if err != nil {
		return nil, err
	}
	return sortedDatabases(databases), nil
}

// Get returns the database named name; false when the file does not hold it.
func (s *Store) Get(name string) (Database, bool, error) {
	databases, err := s.read()
	if err != nil {
		return Database{}, false, err
	}
	db, found := databases[strings.TrimSpace(name)]
	return db, found, nil
}

// Put normalizes and validates db, then writes it: added when previousName is empty, else in
// place of previousName, renamed when db.Name differs. Adding or renaming over another database
// wraps ErrExists; a previousName the file does not hold wraps ErrNotFound.
func (s *Store) Put(previousName string, db Database) error { //nolint:gocritic // hugeParam: db is a copy the store normalizes, never the caller's value
	db.Normalize()
	if err := db.Validate(); err != nil {
		return err
	}
	previousName = strings.TrimSpace(previousName)
	return s.update(func(databases map[string]Database) error {
		if previousName != "" {
			if _, found := databases[previousName]; !found {
				return fmt.Errorf("database %q %w", previousName, ErrNotFound)
			}
		}
		if _, taken := databases[db.Name]; taken && db.Name != previousName {
			return fmt.Errorf("database %q %w", db.Name, ErrExists)
		}
		delete(databases, previousName)
		databases[db.Name] = db
		return nil
	})
}

// Delete removes the database named name; a missing one is not an error and writes nothing.
func (s *Store) Delete(name string) error {
	name = strings.TrimSpace(name)
	if _, found, err := s.Get(name); err != nil || !found {
		return err
	}
	return s.update(func(databases map[string]Database) error {
		delete(databases, name)
		return nil
	})
}

// update applies change to the databases read from the file, then writes them, so each write
// starts from the file's current content. Nothing is written when change fails.
func (s *Store) update(change func(databases map[string]Database) error) error {
	if s.path == "" {
		return errNoPath
	}
	databases, err := s.read()
	if err != nil {
		return err
	}
	if err := change(databases); err != nil {
		return err
	}
	return s.write(databases)
}

// read decodes the file; a missing or blank file (or an empty path) holds no database.
func (s *Store) read() (map[string]Database, error) {
	databases := map[string]Database{}
	if s.path == "" {
		return databases, nil
	}
	raw, err := os.ReadFile(s.path) // #nosec G304 -- the path is the user's own databases file.
	if errors.Is(err, fs.ErrNotExist) {
		return databases, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", s.path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return databases, nil
	}
	layout, err := decodeLayout(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", s.path, err)
	}
	return layout.Databases, nil
}

// decodeLayout decodes the JSON of databases.json or of a share string: unknown keys are refused,
// the version must be fileVersion, and every database must be valid. Name is set from each key.
func decodeLayout(raw []byte) (fileLayout, error) {
	var layout fileLayout
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&layout); err != nil {
		return fileLayout{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fileLayout{}, errTrailingData
	}
	if layout.Version != fileVersion {
		return fileLayout{}, fmt.Errorf("version %d is not supported (expected %d): update airlock", layout.Version, fileVersion)
	}
	databases := make(map[string]Database, len(layout.Databases))
	for _, db := range sortedDatabases(layout.Databases) {
		if err := db.Validate(); err != nil {
			return fileLayout{}, fmt.Errorf("database %q: %w", db.Name, err)
		}
		databases[db.Name] = db
	}
	layout.Databases = databases
	return layout, nil
}

// sortedDatabases returns the databases of a decoded map sorted by name, each named by its key.
func sortedDatabases(databases map[string]Database) []Database {
	sorted := make([]Database, 0, len(databases))
	for name, db := range databases {
		db.Name = name
		sorted = append(sorted, db)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	return sorted
}

// write stores databases as indented JSON, names sorted.
func (s *Store) write(databases map[string]Database) error {
	data, err := json.MarshalIndent(&fileLayout{Version: fileVersion, Databases: databases}, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode %s: %w", s.path, err)
	}
	return writeAtomic(s.path, append(data, '\n'))
}

// writeAtomic replaces path through a temporary file readable by the user only, so a crash never
// leaves it half written.
func writeAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".databases-*.json")
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	// Once renamed the temporary file is gone; before that, a failure leaves it to remove.
	defer func() {
		if err == nil {
			return
		}
		if removeErr := os.Remove(tmp.Name()); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()

	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if err = errors.Join(writeErr, closeErr, os.Chmod(tmp.Name(), 0o600)); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
