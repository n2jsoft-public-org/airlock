package directdb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
)

// fileVersion is the layout version of databases.json and of the share strings.
const fileVersion = 1

var (
	// ErrExists is returned by the stores of direct databases (connections.Databases) when adding or
	// renaming a database over another one.
	ErrExists = errors.New("already exists")
	// ErrNotFound is returned by the stores of direct databases (connections.Databases) for a database
	// to update that they do not hold.
	ErrNotFound = errors.New("not found")

	errTrailingData = errors.New("unexpected content after the end of the JSON document")
)

// NotFoundError reports that no direct database is named name; errors.Is(err, ErrNotFound) holds.
func NotFoundError(name string) error {
	return notFoundError{name: name}
}

// notFoundError keeps its own message while matching ErrNotFound.
type notFoundError struct{ name string }

func (e notFoundError) Error() string {
	return fmt.Sprintf("no direct database named %s (see airlock connection list)", e.name)
}

func (notFoundError) Is(target error) bool { return target == ErrNotFound }

// fileLayout is the JSON document of databases.json; Name of each database is its key.
//
//nolint:govet // fieldalignment: the field order is the file's key order.
type fileLayout struct {
	Version   int                 `json:"version"`
	Databases map[string]Database `json:"databases"`
}

// ReadFile returns the databases of the databases.json of an earlier airlock at path, sorted by
// name: a missing or blank file (or an empty path) holds none; an invalid one is an error naming
// the file, the database and the field.
func ReadFile(path string) ([]Database, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the path is the user's own databases file.
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	layout, err := decodeLayout(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", path, err)
	}
	return sortedDatabases(layout.Databases), nil
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
