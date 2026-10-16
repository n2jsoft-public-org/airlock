package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/sharecode"
)

// sharePrefix starts every config string; its number is the format version: 2 carries the
// nested layout of config.json.
const sharePrefix = "airlock2:"

// legacySharePrefix starts the strings of the flat layout, still imported.
const legacySharePrefix = "airlock1:"

// ErrInvalidShare refuses a string that is not an intact config string.
var ErrInvalidShare = errors.New("not a valid airlock config string (damaged or incomplete)")

// ChangeKind tells what an import does to one key.
type ChangeKind string

const (
	// ChangeAdded writes a key the file does not set yet.
	ChangeAdded ChangeKind = "added"
	// ChangeUpdated replaces the value the file sets.
	ChangeUpdated ChangeKind = "changed"
	// ChangeRemoved drops a key of the file, so that its default applies (replace only).
	ChangeRemoved ChangeKind = "removed"
	// ChangeUnchanged keeps the value the file already sets.
	ChangeUnchanged ChangeKind = "unchanged"
	// ChangeLocked skips a key the organization locks: it is listed and never written.
	ChangeLocked ChangeKind = "locked"
)

// Change is what an import does to one key: its effective value before and after, each flagged
// when it is airlock's default rather than a value of the file.
type Change struct {
	Key
	Kind       ChangeKind
	Old        string
	New        string
	OldDefault bool
	NewDefault bool
	// Locked: the key is locked after the change; WasLocked, set by DiffOrganizations, before it.
	Locked    bool
	WasLocked bool
}

// Plan is an import ready to be applied to Path. Changes lists, in Keys order, every key of the
// string, and with replace every key of the file it removes.
type Plan struct {
	result  file
	Path    string
	Changes []Change
}

// HasChanges reports whether applying the plan modifies the file.
func (p *Plan) HasChanges() bool {
	for _, c := range p.Changes {
		if c.Kind != ChangeUnchanged && c.Kind != ChangeLocked {
			return true
		}
	}
	return false
}

// Export encodes the keys set in path as one opaque string, only those of keys when any is given;
// keys left to airlock's defaults are not part of it.
func Export(path string, keys ...string) (string, error) {
	f, err := readFile(path)
	if err != nil {
		return "", err
	}
	if _, err = f.apply(new(Defaults())); err != nil {
		return "", i18n.NewError("config.config.invalid_config", path, err)
	}
	f.dropPersonal()
	if f.empty() {
		return "", i18n.NewError("config.share.nothing_to_export_sets", path)
	}
	if len(keys) > 0 {
		if f, err = f.only(path, keys); err != nil {
			return "", err
		}
	}
	data, err := json.Marshal(&f)
	if err != nil {
		return "", i18n.NewError("config.settings.failed_to_encode", path, err)
	}
	return encodeShare(data), nil
}

// PlanImport decodes shared and computes what importing it into path changes: its keys overwrite
// those of the file, and with replace the file's other keys are removed. When any keys are given,
// only those are imported or removed, the others left as the file has them. Keys org locks are
// listed as ChangeLocked and left alone. Nothing is written.
func PlanImport(path string, org *Organization, shared string, replace bool, keys ...string) (Plan, error) {
	incoming, err := decodeShare(shared)
	if err != nil {
		return Plan{}, err
	}
	local, err := readFile(path)
	if err != nil {
		return Plan{}, err
	}
	defaults := Defaults()
	before, err := local.apply(&defaults)
	if err != nil {
		return Plan{}, i18n.NewError("config.config.invalid_config", path, err)
	}

	asked := func(name string) bool { return len(keys) == 0 || slices.Contains(keys, name) }
	picked := func(name string) bool { return asked(name) && !org.Locked(name) }
	result := local
	if replace {
		result = file{}
		for _, key := range Keys {
			if !picked(key.Name) {
				*result.field(key.Name) = *local.field(key.Name)
			}
		}
		if !picked("environments") {
			result.ProductionProfiles = local.ProductionProfiles
		}
	}
	for _, key := range Keys {
		if value := *incoming.field(key.Name); value != nil && picked(key.Name) {
			*result.field(key.Name) = value
		}
	}
	result.keepPersonal(&local)
	if result.Environments != nil {
		result.ProductionProfiles = nil
	}
	after, err := result.apply(&defaults)
	if err != nil {
		return Plan{}, err
	}

	oldValues, newValues := before.values(), after.values()
	plan := Plan{Path: path, result: result}
	for _, key := range Keys {
		if key.Personal || !asked(key.Name) {
			continue
		}
		if org.Locked(key.Name) {
			if incoming.sets(key.Name) {
				plan.Changes = append(plan.Changes, Change{Key: key, Kind: ChangeLocked, Old: oldValues[key.Name], New: oldValues[key.Name], Locked: true})
			}
			continue
		}
		inLocal := local.sets(key.Name)
		inIncoming := incoming.sets(key.Name)
		change := Change{
			Key: key, Old: oldValues[key.Name], New: newValues[key.Name],
			OldDefault: !inLocal, NewDefault: !result.sets(key.Name),
		}
		switch {
		case inIncoming && !inLocal:
			change.Kind = ChangeAdded
		case inIncoming && change.Old == change.New:
			change.Kind = ChangeUnchanged
		case inIncoming:
			change.Kind = ChangeUpdated
		case inLocal && replace:
			change.Kind = ChangeRemoved
		default:
			continue
		}
		plan.Changes = append(plan.Changes, change)
	}
	return plan, nil
}

// ApplyImport writes the file computed by PlanImport.
func ApplyImport(plan *Plan) error {
	return writeFile(plan.Path, &plan.result)
}

// encodeShare wraps the JSON of a config in a checksummed, prefixed base64url string.
func encodeShare(data []byte) string {
	return sharecode.Encode(sharePrefix, data)
}

// decodeShare reads a string of encodeShare; its keys must be known and valid.
func decodeShare(shared string) (file, error) {
	data, err := sharecode.Decode(sharePrefix, shared)
	if err != nil {
		// A string of an earlier airlock: its flat keys decode into the keys replacing them.
		if data, err = sharecode.Decode(legacySharePrefix, shared); err != nil {
			return file{}, ErrInvalidShare
		}
	}

	var f file
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&f); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return file{}, i18n.NewError("config.share.config_string_has_setting", err)
		}
		return file{}, ErrInvalidShare
	}
	if _, err := f.apply(new(Defaults())); err != nil {
		return file{}, i18n.NewError("config.share.config_string_has_invalid", err)
	}
	if f.Environments == nil && f.ProductionProfiles != nil {
		// A string of an earlier airlock: its productionProfiles is imported as environments.
		patterns, _ := parseProfilePatterns(*f.ProductionProfiles) //nolint:errcheck // apply validated it.
		f.Environments, f.ProductionProfiles = new(environment.FromProductionProfiles(patterns).String()), nil
	}
	f.dropPersonal()
	return f, nil
}

// only keeps the keys of f named in keys, each of which f must set.
func (f *file) only(path string, keys []string) (file, error) {
	var kept file
	for _, key := range keys {
		if slices.ContainsFunc(Keys, func(k Key) bool { return k.Name == key && k.Personal }) {
			return file{}, i18n.NewError("config.share.is_personal_config_strings", key)
		}
		field, err := f.knownField(key)
		if err != nil {
			return file{}, err
		}
		if key == "environments" && *field == nil && f.ProductionProfiles != nil {
			// A legacy-only file: export what its productionProfiles stands for.
			patterns, _ := parseProfilePatterns(*f.ProductionProfiles) //nolint:errcheck // apply validated it.
			*kept.field(key) = new(environment.FromProductionProfiles(patterns).String())
			continue
		}
		if *field == nil {
			return file{}, i18n.NewError("config.share.does_not_set_airlock", path, key)
		}
		*kept.field(key) = *field
	}
	return kept, nil
}

// empty reports whether f sets no key.
func (f *file) empty() bool {
	for _, key := range Keys {
		if f.sets(key.Name) {
			return false
		}
	}
	return true
}

// dropPersonal clears the keys config strings never carry.
func (f *file) dropPersonal() {
	for _, key := range Keys {
		if key.Personal {
			*f.field(key.Name) = nil
		}
	}
}

// keepPersonal copies the personal keys of local into f.
func (f *file) keepPersonal(local *file) {
	for _, key := range Keys {
		if key.Personal {
			*f.field(key.Name) = *local.field(key.Name)
		}
	}
}
