package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/n2jsoft/airlock/internal/sharecode"
)

// sharePrefix starts every config string; its number is the format version.
const sharePrefix = "airlock1:"

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
		if c.Kind != ChangeUnchanged {
			return true
		}
	}
	return false
}

// Export encodes the keys set in path as one opaque string; keys left to airlock's defaults are not
// part of it.
func Export(path string) (string, error) {
	f, err := readFile(path)
	if err != nil {
		return "", err
	}
	if _, err = f.apply(new(Defaults())); err != nil {
		return "", fmt.Errorf("invalid config %s: %w", path, err)
	}
	if f.empty() {
		return "", fmt.Errorf("nothing to export: %s sets no key (airlock's defaults apply)", path)
	}
	data, err := json.Marshal(&f)
	if err != nil {
		return "", fmt.Errorf("failed to encode %s: %w", path, err)
	}
	return encodeShare(data), nil
}

// PlanImport decodes shared and computes what importing it into path changes: its keys overwrite
// those of the file, and with replace the file's other keys are removed. Nothing is written.
func PlanImport(path, shared string, replace bool) (Plan, error) {
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
		return Plan{}, fmt.Errorf("invalid config %s: %w", path, err)
	}

	result := local
	if replace {
		result = file{}
	}
	for _, key := range Keys {
		if value := *incoming.field(key.Name); value != nil {
			*result.field(key.Name) = value
		}
	}
	after, err := result.apply(&defaults)
	if err != nil {
		return Plan{}, err
	}

	oldValues, newValues := before.values(), after.values()
	plan := Plan{Path: path, result: result}
	for _, key := range Keys {
		inLocal := *local.field(key.Name) != nil
		inIncoming := *incoming.field(key.Name) != nil
		change := Change{
			Key: key, Old: oldValues[key.Name], New: newValues[key.Name],
			OldDefault: !inLocal, NewDefault: *result.field(key.Name) == nil,
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
		return file{}, ErrInvalidShare
	}

	var f file
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&f); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return file{}, fmt.Errorf("the config string has a setting this airlock does not know (%w): update airlock", err)
		}
		return file{}, ErrInvalidShare
	}
	if _, err := f.apply(new(Defaults())); err != nil {
		return file{}, fmt.Errorf("the config string has an invalid value: %w", err)
	}
	return f, nil
}

// empty reports whether f sets no key.
func (f *file) empty() bool {
	for _, key := range Keys {
		if *f.field(key.Name) != nil {
			return false
		}
	}
	return true
}
