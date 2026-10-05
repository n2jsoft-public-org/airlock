package connections

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/n2jsoft/airlock/internal/directdb"
)

// legacyDirect is the account and region keying the choices of direct databases in the
// connections.json of an earlier airlock, with the type legacyDirectType.
const (
	legacyDirect     = "local"
	legacyDirectType = "direct"
)

// legacyFile is the connections.json of an earlier airlock: choices keyed by
// <account>/<region>/<type>/<name>.
type legacyFile struct {
	Connections map[string]legacyPrefs `json:"connections"`
}

// legacyPrefs is one entry of the connections.json of an earlier airlock.
type legacyPrefs struct {
	UpdatedAt          time.Time `json:"updated_at"`
	Profile            string    `json:"profile"`
	DBUser             string    `json:"db_user"`
	SignIn             string    `json:"sign_in"`
	DBSecretARN        string    `json:"db_secret_arn"`
	LocalPort          int       `json:"local_port"`
	PasswordInKeychain bool      `json:"password_in_keychain"`
	MCPAllowWrites     bool      `json:"mcp_allow_writes"`
}

// Migrate moves the files of an earlier airlock into the store's file, once: only while that file
// does not exist. databasesPath holds the direct databases (databases.json), connectionsPath the
// remembered choices (connections.json); either may be missing. Once the new file is written, each
// old file is renamed with a .bak suffix. It reports whether it wrote the file: a failed rename
// comes back with true, the migration being done. An old file that cannot be read stops it before
// anything is written or renamed.
func (s *Store) Migrate(databasesPath, connectionsPath string) (bool, error) {
	if s.path == "" {
		return false, errNoPath
	}
	unlock, err := s.lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	switch _, statErr := os.Stat(s.path); {
	case statErr == nil:
		return false, nil
	case !errors.Is(statErr, fs.ErrNotExist):
		return false, fmt.Errorf("failed to read %s: %w", s.path, statErr)
	}
	if !fileExists(databasesPath) && !fileExists(connectionsPath) {
		return false, nil
	}

	dbs, err := directdb.ReadFile(databasesPath)
	if err != nil {
		return false, err
	}
	legacy, err := readLegacyFile(connectionsPath)
	if err != nil {
		return false, err
	}
	if err := s.write(migratedEntries(dbs, legacy.Connections)); err != nil {
		return false, err
	}
	var errs []error
	for _, old := range []string{databasesPath, connectionsPath} {
		if !fileExists(old) {
			continue
		}
		if err := os.Rename(old, old+".bak"); err != nil {
			errs = append(errs, fmt.Errorf("failed to rename %s: %w", old, err))
		}
	}
	return true, errors.Join(errs...)
}

// fileExists reports whether path names an existing file; an empty path names none.
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// readLegacyFile decodes the connections.json of an earlier airlock; a missing file holds nothing.
func readLegacyFile(path string) (legacyFile, error) {
	if path == "" {
		return legacyFile{}, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the user's own file.
	if errors.Is(err, fs.ErrNotExist) {
		return legacyFile{}, nil
	}
	if err != nil {
		return legacyFile{}, fmt.Errorf("failed to read %s: %w", path, err)
	}
	var file legacyFile
	if err := json.Unmarshal(data, &file); err != nil {
		return legacyFile{}, fmt.Errorf("invalid %s: %w", path, err)
	}
	return file, nil
}

// migratedEntries builds the entries of the new file: one per direct database, with the choices
// remembered for it, and one per remembered AWS target. Choices of a direct database whose
// definition is gone, and keys of another format, are dropped.
func migratedEntries(dbs []directdb.Database, legacy map[string]legacyPrefs) map[string]Entry {
	entries := make(map[string]Entry, len(dbs)+len(legacy))
	for idx := range dbs {
		db := dbs[idx]
		entries[db.Name] = Entry{Key: db.Name, Direct: &db}
	}
	for key, prefs := range legacy {
		parts := strings.Split(key, "/")
		if len(parts) != 4 {
			continue
		}
		choices := Choices{
			UsedAt: prefs.UpdatedAt.UTC(), SignIn: prefs.SignIn,
			PasswordInKeychain: prefs.PasswordInKeychain, MCPAllowWrites: prefs.MCPAllowWrites,
		}
		if parts[0] == legacyDirect && parts[1] == legacyDirect && parts[2] == legacyDirectType {
			if entry, found := entries[parts[3]]; found {
				entry.Choices = choices
				entries[parts[3]] = entry
			}
			continue
		}
		target := JumphostTarget{Account: parts[0], Region: parts[1], Type: parts[2], Target: parts[3], Profile: prefs.Profile}
		if target.validate() != nil {
			continue
		}
		choices.User, choices.SecretARN, choices.LocalPort = prefs.DBUser, prefs.DBSecretARN, prefs.LocalPort
		entries[target.Key()] = Entry{Key: target.Key(), Jumphost: &target, Choices: choices}
	}
	return entries
}
