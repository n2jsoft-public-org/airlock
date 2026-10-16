package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// legacyKey is a flat key of the earlier layout found in a file.
type legacyKey struct {
	name  string
	value string
	// ignoredBy is the key replacing name when the file sets it too, which then wins.
	ignoredBy string
}

// typedKeys are the keys written as JSON booleans or numbers rather than strings.
var typedKeys = map[string]bool{
	"aws.randomLocalPort": true,
	"mcp.gateway.enabled": true,
	"mcp.gateway.port":    true,
	"agent.writes":        true,
}

// MarshalJSON writes the keys f sets nested by their dotted names, in Keys order, the deprecated
// productionProfiles last. Booleans and the port are JSON booleans and numbers.
func (f *file) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	var open []string // the sections being written, outermost first
	first := true
	buf.WriteByte('{')
	for _, key := range slices.Concat(Keys, []Key{deprecatedProductionProfiles}) {
		value := *f.field(key.Name)
		if value == nil {
			continue
		}
		path := strings.Split(key.Name, ".")
		sections, name := path[:len(path)-1], path[len(path)-1]
		shared := 0
		for shared < len(open) && shared < len(sections) && open[shared] == sections[shared] {
			shared++
		}
		for len(open) > shared {
			buf.WriteByte('}')
			open, first = open[:len(open)-1], false
		}
		for _, section := range sections[shared:] {
			writeName(&buf, section, first)
			buf.WriteByte('{')
			open, first = append(open, section), true
		}
		writeName(&buf, name, first)
		buf.Write(jsonValue(key.Name, *value))
		first = false
	}
	buf.WriteString(strings.Repeat("}", len(open)) + "}")
	return buf.Bytes(), nil
}

// writeName writes the name of a member of a JSON object, after a comma unless it is the first.
func writeName(buf *bytes.Buffer, name string, first bool) {
	if !first {
		buf.WriteByte(',')
	}
	buf.WriteString(strconv.Quote(name) + ":")
}

// jsonValue renders value of key: a boolean or number for typedKeys when value is one, a string
// otherwise.
func jsonValue(key, value string) []byte {
	if typedKeys[key] || strings.HasPrefix(key, "modules.") && strings.HasSuffix(key, ".enabled") {
		if b, err := parseBool(value); err == nil {
			return []byte(strconv.FormatBool(b))
		}
		if n, err := strconv.Atoi(value); err == nil && strconv.Itoa(n) == value {
			return []byte(value)
		}
	}
	data, _ := json.Marshal(value) //nolint:errcheck // a string always encodes.
	return data
}

// UnmarshalJSON reads the nested layout, and the flat keys of the earlier one into the keys
// replacing them. Values may be strings, booleans or numbers; an unknown key is an error.
func (f *file) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := f.decodeObject(decoder, ""); err != nil {
		return err
	}
	return f.upgrade()
}

// decodeObject reads the members of the object starting at decoder, whose path is prefix.
func (f *file) decodeObject(decoder *json.Decoder, prefix string) error {
	if token, err := decoder.Token(); err != nil {
		return err
	} else if token != json.Delim('{') {
		return fmt.Errorf("json: %q is not an object", strings.TrimSuffix(prefix, "."))
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, _ := token.(string) //nolint:errcheck // object members start with a name.
		path := prefix + name
		if isSection(path) {
			if err := f.decodeObject(decoder, path+"."); err != nil {
				return err
			}
			continue
		}
		var raw any
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		if err := f.setDecoded(path, raw); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

// isSection reports whether path names a section of the nested layout.
func isSection(path string) bool {
	return slices.ContainsFunc(Keys, func(k Key) bool { return strings.HasPrefix(k.Name, path+".") })
}

// setDecoded stores the JSON value raw of the key at path; null leaves it unset.
func (f *file) setDecoded(path string, raw any) error {
	var value string
	switch v := raw.(type) {
	case nil:
		return nil
	case string:
		value = v
	case bool:
		value = strconv.FormatBool(v)
	case json.Number:
		value = v.String()
	default:
		return fmt.Errorf("json: %q must be a string, a boolean or a number", path)
	}
	_, renamed := renamedKeys[path]
	if renamed || path == legacyDisabledModules {
		f.legacy = append(f.legacy, legacyKey{name: path, value: strings.TrimSpace(value)})
		return nil
	}
	field := f.field(path)
	if field == nil {
		return fmt.Errorf("json: unknown field %q", path)
	}
	*field = &value
	return nil
}

// upgrade moves the values of the flat keys into the keys replacing them, unless the file sets
// those too.
func (f *file) upgrade() error {
	for idx := range f.legacy {
		legacy := &f.legacy[idx]
		if legacy.name == legacyDisabledModules {
			modules, err := parseModules(legacy.value)
			if err != nil {
				return fmt.Errorf("%s: %w", legacyDisabledModules, err)
			}
			for _, module := range modules {
				if field := f.field(moduleEnabledKey(module)); *field == nil {
					*field = new("false")
				}
			}
			continue
		}
		newKey := renamedKeys[legacy.name]
		field := f.field(newKey)
		switch {
		case field == nil:
			return fmt.Errorf("json: unknown field %q", legacy.name)
		case *field == nil:
			*field = new(legacy.value)
		default:
			legacy.ignoredBy = newKey
		}
	}
	return nil
}
