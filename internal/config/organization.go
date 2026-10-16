package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/n2jsoft/airlock/internal/i18n"
)

// Organization is the settings an organization pushes to its developers, and the keys it locks.
type Organization struct {
	// Source is where the file was read from.
	Source   string
	locked   map[string]bool
	settings file
}

// ErrLocked refuses to change a key the organization locks.
var ErrLocked = errors.New("locked by the organization")

// lockedError is the localized refusal; errors.Is finds ErrLocked in it.
type lockedError struct{ error }

func (e lockedError) Unwrap() []error    { return []error{e.error} }
func (lockedError) Is(target error) bool { return target == ErrLocked }

// refuseLocked returns an ErrLocked error naming key when o locks it; the deprecated
// productionProfiles is the environments key.
func (o *Organization) refuseLocked(key string) error {
	key = currentName(key)
	if key == legacyProductionProfiles {
		key = "environments"
	}
	if o == nil || !o.Locked(key) {
		return nil
	}
	return lockedError{i18n.NewError("config.organization.key_locked", key, o.Source)}
}

// DiffOrganizations lists, in Keys order, the keys whose value or lock differs between two
// organization configs (nil for none). Old and New are the rendered values, "" when unset; Locked
// is the lock state of new.
func DiffOrganizations(oldOrg, newOrg *Organization) []Change {
	oldValues, newValues := oldOrg.rendered(), newOrg.rendered()
	var changes []Change
	for _, key := range Keys {
		oldSet, newSet := oldOrg.Sets(key.Name), newOrg.Sets(key.Name)
		change := Change{Key: key, Locked: newOrg.Locked(key.Name), WasLocked: oldOrg.Locked(key.Name)}
		if oldSet {
			change.Old = oldValues[key.Name]
		}
		if newSet {
			change.New = newValues[key.Name]
		}
		switch {
		case !oldSet && !newSet:
			continue
		case oldSet && newSet && change.Old == change.New && change.WasLocked == change.Locked:
			continue
		case !oldSet:
			change.Kind = ChangeAdded
		case !newSet:
			change.Kind = ChangeRemoved
		default:
			change.Kind = ChangeUpdated
		}
		changes = append(changes, change)
	}
	return changes
}

// rendered is the value of every key o sets, as config.json holds it; nil for a nil o.
func (o *Organization) rendered() map[string]string {
	if o == nil {
		return nil
	}
	cfg, _ := o.apply(new(Defaults())) //nolint:errcheck // ParseOrganization validated the settings.
	return cfg.values()
}

// ParseOrganization reads an organization file: {"settings": {...nested...}, "locked": [...] | "*"}.
// Unknown top-level keys, unknown and personal settings are ignored with a warning; a malformed value, or a locked key
// the file does not set, is an error.
func ParseOrganization(source string, data []byte) (org *Organization, warnings []string, err error) {
	// A map, not a struct: a misspelt "locked" must warn, not silently lock nothing.
	var wire map[string]json.RawMessage
	if err = json.Unmarshal(data, &wire); err != nil {
		return nil, nil, i18n.NewError("config.organization.invalid", source, err)
	}
	for _, key := range slices.Sorted(maps.Keys(wire)) {
		if key != "settings" && key != "locked" {
			warnings = append(warnings, i18n.Format("config.organization.unknown_field", key))
		}
	}
	f := file{lenient: true}
	if settings := wire["settings"]; len(settings) > 0 {
		if err = f.UnmarshalJSON(settings); err != nil {
			return nil, nil, i18n.NewError("config.organization.invalid", source, err)
		}
	}
	for _, key := range f.unknown {
		warnings = append(warnings, i18n.Format("config.organization.unknown_setting", key))
	}
	for _, key := range Keys {
		if key.Personal && f.sets(key.Name) {
			warnings = append(warnings, i18n.Format("config.organization.personal_setting", key.Name))
		}
	}
	f.dropPersonal()
	if _, err = f.apply(new(Defaults())); err != nil {
		return nil, nil, i18n.NewError("config.organization.invalid", source, err)
	}

	locked := map[string]bool{}
	var star string
	switch trimmed := bytes.TrimSpace(wire["locked"]); {
	case len(trimmed) == 0 || string(trimmed) == "null":
	case json.Unmarshal(trimmed, &star) == nil && star == "*":
		for _, key := range Keys {
			locked[key.Name] = f.sets(key.Name)
		}
	default:
		var names []string
		if err = json.Unmarshal(trimmed, &names); err != nil {
			return nil, nil, i18n.NewError("config.organization.invalid_locked")
		}
		for _, name := range names {
			key := currentName(name)
			if !slices.ContainsFunc(Keys, func(k Key) bool { return k.Name == key }) {
				return nil, nil, i18n.NewError("config.organization.unknown_locked", name)
			}
			if !f.sets(key) {
				return nil, nil, i18n.NewError("config.organization.locked_without_value", key)
			}
			locked[key] = true
		}
	}
	return &Organization{Source: source, settings: f, locked: locked}, warnings, nil
}

// Locked reports whether the organization locks key, a dotted name or a flat legacy one.
func (o *Organization) Locked(key string) bool {
	return o != nil && o.locked[currentName(key)]
}

// Sets reports whether the organization gives key a value.
func (o *Organization) Sets(key string) bool {
	return o != nil && o.settings.sets(currentName(key))
}

// apply returns base with the settings of o over it; a nil o sets nothing.
func (o *Organization) apply(base *Config) (Config, error) {
	if o == nil {
		return *base, nil
	}
	cfg, err := o.settings.apply(base)
	if err != nil {
		return Config{}, i18n.NewError("config.organization.invalid", o.Source, err)
	}
	return cfg, nil
}

// dropLocked removes from f the keys org locks, with the deprecated productionProfiles when it
// locks environments. Flat legacy keys are already in the keys replacing them (upgrade).
func (f *file) dropLocked(org *Organization) {
	for _, key := range Keys {
		if org.Locked(key.Name) {
			*f.field(key.Name) = nil
		}
	}
	if org.Locked("environments") {
		f.ProductionProfiles = nil
	}
}

// Managed is the machine-wide managed config: a pointer to the organization file, or the file
// itself inline.
type Managed struct {
	Source string
	Inline []byte
}

// ParseManaged reads a managed config, which sets either {"organizationConfig": "<source>"} or
// an inline organization file ("settings", "locked"), not both.
func ParseManaged(data []byte) (Managed, error) {
	var wire struct {
		OrganizationConfig string          `json:"organizationConfig"`
		Settings           json.RawMessage `json:"settings"`
		Locked             json.RawMessage `json:"locked"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return Managed{}, i18n.NewError("config.organization.invalid", "managed", err)
	}
	inline := len(wire.Settings) > 0 || len(wire.Locked) > 0
	switch {
	case wire.OrganizationConfig != "" && inline:
		return Managed{}, i18n.NewError("config.organization.managed_both")
	case wire.OrganizationConfig != "":
		return Managed{Source: wire.OrganizationConfig}, nil
	case inline:
		return Managed{Inline: data}, nil
	}
	return Managed{}, i18n.NewError("config.organization.managed_neither")
}
