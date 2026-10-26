package directdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/sharecode"
)

// sharePrefix starts every database string; its number is the format version.
const sharePrefix = "airlockdb1:"

// ErrInvalidShare refuses a string that is not an intact database string.
var ErrInvalidShare = errors.New("not a valid airlock database string (damaged or incomplete)")

// ChangeKind tells what an import does to one database.
type ChangeKind string

const (
	// ChangeAdded writes a database the file does not hold yet.
	ChangeAdded ChangeKind = "added"
	// ChangeUpdated replaces the definition the file holds.
	ChangeUpdated ChangeKind = "changed"
	// ChangeUnchanged keeps the definition the file already holds.
	ChangeUnchanged ChangeKind = "unchanged"
)

// FieldChange is one field of a changed definition, as written in the file ("-" when left out).
type FieldChange struct {
	Field string
	Old   string
	New   string
}

// ImportChange is what an import does to one database of the string.
type ImportChange struct {
	Kind     ChangeKind
	Database Database
	// Fields lists the changed fields of a ChangeUpdated database; nil otherwise.
	Fields []FieldChange
}

// ConnectionChanged reports whether the import changes how a database already defined is reached:
// a changed field other than environment. Its remembered password must then be forgotten.
func (c *ImportChange) ConnectionChanged() bool {
	if c.Kind != ChangeUpdated {
		return false
	}
	for i := range c.Fields {
		if c.Fields[i].Field != "environment" {
			return true
		}
	}
	return false
}

// ImportPlan lists, by name, every database of an imported string.
type ImportPlan struct {
	Changes []ImportChange
}

// HasChanges reports whether applying the plan modifies the file.
func (p *ImportPlan) HasChanges() bool {
	for i := range p.Changes {
		if p.Changes[i].Kind != ChangeUnchanged {
			return true
		}
	}
	return false
}

// Export encodes dbs as one opaque string for teammates to import. Passwords are not part of a
// definition, so never part of the string.
func Export(dbs []Database) (string, error) {
	if len(dbs) == 0 {
		return "", errors.New("nothing to export: no direct database (add one with `airlock connection add`)")
	}
	layout := fileLayout{Version: fileVersion, Databases: make(map[string]Database, len(dbs))}
	for i := range dbs {
		db := dbs[i]
		db.Normalize()
		if err := db.Validate(); err != nil {
			return "", fmt.Errorf("database %q: %w", db.Name, err)
		}
		layout.Databases[db.Name] = db
	}
	data, err := json.Marshal(&layout)
	if err != nil {
		return "", fmt.Errorf("failed to encode the databases: %w", err)
	}
	return sharecode.Encode(sharePrefix, data), nil
}

// PlanImport decodes shared and computes what importing it over current changes: every database
// of the string is added or replaces the one of the same name; local databases missing from the
// string are kept. One invalid database refuses the whole string. Nothing is written.
func PlanImport(current []Database, shared string) (ImportPlan, error) {
	incoming, err := decodeShare(shared)
	if err != nil {
		return ImportPlan{}, err
	}
	local := make(map[string]*Database, len(current))
	for i := range current {
		local[current[i].Name] = &current[i]
	}

	var plan ImportPlan
	for _, db := range sortedDatabases(incoming) {
		change := ImportChange{Database: db, Kind: ChangeAdded}
		if previous, found := local[db.Name]; found {
			change.Fields = changedFields(previous, &db)
			change.Kind = ChangeUpdated
			if change.Fields == nil {
				change.Kind = ChangeUnchanged
			}
		}
		plan.Changes = append(plan.Changes, change)
	}
	return plan, nil
}

// Writer writes definitions, each in place of the one of the same name, in one write.
type Writer interface {
	PutAll(dbs []Database) error
}

// ApplyImport writes the added and changed databases of plan in one write.
func ApplyImport(store Writer, plan *ImportPlan) error {
	if !plan.HasChanges() {
		return nil
	}
	var dbs []Database
	for i := range plan.Changes {
		if change := &plan.Changes[i]; change.Kind != ChangeUnchanged {
			dbs = append(dbs, change.Database)
		}
	}
	return store.PutAll(dbs)
}

// decodeShare reads a string of Export; its fields must be known, its databases valid.
func decodeShare(shared string) (map[string]Database, error) {
	data, err := sharecode.Decode(sharePrefix, shared)
	if err != nil {
		return nil, ErrInvalidShare
	}
	layout, err := decodeLayout(data)
	var syntaxErr *json.SyntaxError
	switch {
	case errors.Is(err, errTrailingData):
		return nil, ErrInvalidShare
	case err != nil && strings.Contains(err.Error(), "unknown field"):
		return nil, fmt.Errorf("the database string has a field this airlock does not know (%w): update airlock", err)
	case err != nil && (errors.As(err, &syntaxErr) || strings.HasPrefix(err.Error(), "json: ") || strings.Contains(err.Error(), "unexpected EOF")):
		return nil, ErrInvalidShare
	case err != nil:
		return nil, fmt.Errorf("the database string is not valid: %w", err)
	case len(layout.Databases) == 0:
		return nil, ErrInvalidShare
	}
	return layout.Databases, nil
}

// changedFields compares two definitions field by field, as written in the file; nil when equal.
func changedFields(previous, next *Database) []FieldChange {
	before, after := writtenFields(previous), writtenFields(next)
	var changes []FieldChange
	for i := range before {
		if before[i][1] != after[i][1] {
			changes = append(changes, FieldChange{Field: before[i][0], Old: before[i][1], New: after[i][1]})
		}
	}
	return changes
}

// writtenFields lists the fields of db with their written value, "-" for one left out.
func writtenFields(db *Database) [][2]string {
	orDash := func(value string) string {
		if value == "" {
			return "-"
		}
		return value
	}
	port := "-"
	if db.Port != 0 {
		port = strconv.Itoa(db.Port)
	}
	return [][2]string{
		{"engine", string(db.Engine)},
		{"host", db.Host},
		{"port", port},
		{"database", orDash(db.Database)},
		{"user", db.User},
		{"tls", orDash(string(db.TLS))},
		{"environment", orDash(db.Environment)},
	}
}
