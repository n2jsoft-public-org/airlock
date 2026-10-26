package connections

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
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/platform"
)

// fileVersion is the layout version of saved-connections.json.
const fileVersion = 1

var (
	// errUnchanged tells update that a change leaves the file as it is: nothing is written.
	errUnchanged = errors.New("unchanged")

	errNoPath       = errors.New("the location of saved-connections.json is unknown")
	errTrailingData = errors.New("unexpected content after the end of the JSON document")
)

// document is the JSON of the file; each entry is decoded on its own, so that one invalid entry
// never hides the others.
//
//nolint:govet // fieldalignment: the field order is the file's key order.
type document struct {
	Version     int                        `json:"version"`
	Connections map[string]json.RawMessage `json:"connections"`
}

// Store keeps the entries in a JSON file, created on the first write. Every write holds a
// cross-process lock and rereads the file first: the daemon and the dashboards write concurrently.
type Store struct {
	now  func() time.Time
	path string
}

// NewStore keeps the entries in path; an empty path holds none and cannot be written.
func NewStore(path string) *Store {
	return NewStoreWithClock(path, time.Now)
}

// NewStoreWithClock is NewStore with the clock dating the connections; tests fix it.
func NewStoreWithClock(path string, now func() time.Time) *Store {
	return &Store{path: path, now: now}
}

// Path is the file of the store.
func (s *Store) Path() string {
	return s.path
}

// List returns every entry sorted by key, invalid ones included. A missing or blank file holds
// none; a file that is not valid JSON, or of another version, is an error naming it.
func (s *Store) List() ([]Entry, error) {
	entries, err := s.read()
	if err != nil {
		return nil, err
	}
	return sortedEntries(entries), nil
}

// Get returns the entry keyed key; false when the file does not hold it.
func (s *Store) Get(key string) (Entry, bool, error) {
	entries, err := s.read()
	if err != nil {
		return Entry{}, false, err
	}
	entry, found := entries[strings.TrimSpace(key)]
	return entry, found, nil
}

// Find returns the entries name designates: the entry keyed name, else every jumphost entry whose
// <type>/<target> is name, sorted by key.
func (s *Store) Find(name string) ([]Entry, error) {
	name = strings.TrimSpace(name)
	entries, err := s.read()
	if err != nil {
		return nil, err
	}
	if entry, found := entries[name]; found {
		return []Entry{entry}, nil
	}
	var matches []Entry
	sorted := sortedEntries(entries)
	for i := range sorted {
		entry := sorted[i]
		if entry.Jumphost != nil && entry.ShortName() == name {
			matches = append(matches, entry)
		}
	}
	return matches, nil
}

// RememberJumphost records choices as the last connection, now, of target: its entry is created or
// replaced.
func (s *Store) RememberJumphost(target JumphostTarget, choices Choices) error { //nolint:gocritic // hugeParam: a copy it stores
	target = trimmedTarget(&target)
	if err := target.validate(); err != nil {
		return fmt.Errorf("connection %s: %w", target.Key(), err)
	}
	return s.update(func(entries map[string]Entry) error {
		choices.UsedAt = s.now().UTC()
		entries[target.Key()] = Entry{Key: target.Key(), Jumphost: &target, Choices: choices}
		return nil
	})
}

// RememberDirect records choices as the last opening, now, of the direct database name. It does
// nothing when no direct database is named name: a definition removed meanwhile stays removed. A
// direct database has no user of its own choosing (its definition's), no secret and no local port.
func (s *Store) RememberDirect(name string, choices Choices) error { //nolint:gocritic // hugeParam: a copy it stores
	name = strings.TrimSpace(name)
	return s.update(func(entries map[string]Entry) error {
		entry, found := entries[name]
		if !found || entry.Direct == nil {
			return errUnchanged
		}
		choices.User, choices.SecretARN, choices.LocalPort = "", "", 0
		choices.UsedAt = s.now().UTC()
		entry.Choices = choices
		entries[name] = entry
		return nil
	})
}

// Forget drops the remembered choices of the entry key: a jumphost entry goes, a direct database
// keeps its definition. A missing or invalid entry is left as it is.
func (s *Store) Forget(key string) error {
	key = strings.TrimSpace(key)
	return s.update(func(entries map[string]Entry) error {
		entry, found := entries[key]
		switch {
		case !found || entry.Invalid != nil:
			return errUnchanged
		case entry.Jumphost != nil:
			delete(entries, key)
		case entry.Choices == (Choices{}):
			return errUnchanged
		default:
			entry.Choices = Choices{}
			entries[key] = entry
		}
		return nil
	})
}

// Delete removes the entry key, whatever it holds; a missing one is not an error and writes nothing.
func (s *Store) Delete(key string) error {
	key = strings.TrimSpace(key)
	return s.update(func(entries map[string]Entry) error {
		if _, found := entries[key]; !found {
			return errUnchanged
		}
		delete(entries, key)
		return nil
	})
}

// update applies change to the entries read from the file under the lock, then writes them, so
// each write starts from the file's current content. Nothing is written when change fails or
// returns errUnchanged.
func (s *Store) update(change func(entries map[string]Entry) error) error {
	if s.path == "" {
		return errNoPath
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := s.read()
	if err != nil {
		// Rewriting would drop every entry of a file we cannot parse.
		return err
	}
	switch err := change(entries); {
	case errors.Is(err, errUnchanged):
		return nil
	case err != nil:
		return err
	}
	return s.write(entries)
}

// lock holds the cross-process lock of the file, next to it, until unlock is called.
func (s *Store) lock() (unlock func(), err error) {
	mkdirErr := os.MkdirAll(filepath.Dir(s.path), 0o700)
	if mkdirErr != nil {
		return nil, fmt.Errorf("failed to create %s: %w", filepath.Dir(s.path), mkdirErr)
	}
	file, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- next to the user's own file.
	if err != nil {
		return nil, fmt.Errorf("failed to lock %s: %w", s.path, err)
	}
	if lockErr := platform.LockFile(file); lockErr != nil {
		_ = file.Close() //nolint:errcheck // ignore close error on failed lock
		return nil, fmt.Errorf("failed to lock %s: %w", s.path, lockErr)
	}
	return func() {
		_ = platform.UnlockFile(file) //nolint:errcheck // ignore unlock error on cleanup
		_ = file.Close()              //nolint:errcheck // ignore close error on cleanup
	}, nil
}

// read decodes the file; a missing or blank file (or an empty path) holds no entry.
func (s *Store) read() (map[string]Entry, error) {
	entries := map[string]Entry{}
	if s.path == "" {
		return entries, nil
	}
	raw, err := os.ReadFile(s.path) // #nosec G304 -- the path is the user's own file.
	if errors.Is(err, fs.ErrNotExist) {
		return entries, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", s.path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return entries, nil
	}
	doc, err := decodeDocument(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", s.path, err)
	}
	for key, data := range doc.Connections {
		entries[key] = decodeEntry(key, data)
	}
	return entries, nil
}

// decodeDocument decodes the file's JSON: unknown keys are refused, the version must be fileVersion.
func decodeDocument(raw []byte) (document, error) {
	var doc document
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return document{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return document{}, errTrailingData
	}
	if doc.Version != fileVersion {
		return document{}, fmt.Errorf("version %d is not supported (expected %d): update airlock", doc.Version, fileVersion)
	}
	return doc, nil
}

// write stores entries as indented JSON, keys sorted.
func (s *Store) write(entries map[string]Entry) error {
	doc := document{Version: fileVersion, Connections: make(map[string]json.RawMessage, len(entries))}
	for key := range entries {
		entry := entries[key]
		data, err := entry.encode()
		if err != nil {
			return err
		}
		doc.Connections[key] = data
	}
	data, err := json.MarshalIndent(&doc, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode %s: %w", s.path, err)
	}
	return writeAtomic(s.path, append(data, '\n'))
}

// sortedEntries returns the entries sorted by key.
func sortedEntries(entries map[string]Entry) []Entry {
	sorted := make([]Entry, 0, len(entries))
	for key := range entries {
		sorted = append(sorted, entries[key])
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	return sorted
}

// writeAtomic replaces path through a temporary file readable by the user only, so a crash never
// leaves it half written.
func writeAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".saved-connections-*.json")
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
