package mcpserve

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// codexFormat is Codex's config.toml, servers in [mcp_servers.<name>] tables. The file is edited
// as text, so the user's comments and layout survive; every change is checked by parsing it.
type codexFormat struct{}

func (codexFormat) add(path string, entry ClientEntry) error {
	return updateTOML(path, func(doc string) (string, bool, error) {
		doc, _ = removeTOMLServer(doc, entry.Name)
		servers, err := tomlServers(doc)
		if err != nil {
			return "", false, err
		}
		if _, defined := servers[entry.Name]; defined {
			return "", false, fmt.Errorf("MCP server %q is defined in a form airlock cannot edit (expected a [mcp_servers.%s] table)",
				entry.Name, tomlKey(entry.Name))
		}
		if doc != "" {
			if !strings.HasSuffix(doc, "\n") {
				doc += "\n"
			}
			doc += "\n"
		}
		doc += fmt.Sprintf("[mcp_servers.%s]\nurl = %s\nhttp_headers = { Authorization = %s }\n",
			tomlKey(entry.Name), tomlString(entry.URL), tomlString("Bearer "+entry.Token))
		return doc, true, nil
	})
}

func (codexFormat) remove(path string, entry ClientEntry) (bool, error) {
	removed := false
	err := updateTOML(path, func(doc string) (string, bool, error) {
		servers, err := tomlServers(doc)
		if err != nil {
			return "", false, err
		}
		current, ok := servers[entry.Name].(map[string]any)
		if !ok || current["url"] != entry.URL {
			return "", false, nil
		}
		doc, removed = removeTOMLServer(doc, entry.Name)
		return doc, removed, nil
	})
	return removed, err
}

// updateTOML applies change to the text of a TOML file under a cross-process lock. change returns
// false when nothing needs to be written; a file that does not parse, before or after, is left alone.
func updateTOML(path string, change func(doc string) (string, bool, error)) (err error) {
	unlock, err := lockConfig(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()

	payload, err := os.ReadFile(path) // #nosec G304 -- path is a known client file.
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to read %s: %w", path, err)
	}
	doc := string(payload)
	if _, parseErr := tomlServers(doc); parseErr != nil {
		return fmt.Errorf("%s: %w", path, parseErr)
	}
	updated, write, err := change(doc)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if !write {
		return nil
	}
	if _, parseErr := tomlServers(updated); parseErr != nil {
		return fmt.Errorf("%s: the edited file would be invalid, left unchanged: %w", path, parseErr)
	}
	return writeFileAtomic(path, []byte(updated))
}

// tomlServers parses doc and returns its mcp_servers table (nil when absent).
func tomlServers(doc string) (map[string]any, error) {
	var root map[string]any
	if err := toml.Unmarshal([]byte(doc), &root); err != nil {
		return nil, fmt.Errorf("not valid TOML: %w", err)
	}
	switch servers := root["mcp_servers"].(type) {
	case nil:
		return nil, nil
	case map[string]any:
		return servers, nil
	default:
		return nil, errors.New(`"mcp_servers" is not a table`)
	}
}

// removeTOMLServer drops the [mcp_servers.<name>] table and its sub-tables from doc, along with the
// blank line separating it from what precedes. removed is false when doc has no such table.
func removeTOMLServer(doc, name string) (string, bool) {
	lines := strings.SplitAfter(doc, "\n")
	kept := make([]string, 0, len(lines))
	inServer, removed := false, false
	for _, line := range lines {
		if path, ok := tomlHeader(line); ok {
			wasIn := inServer
			inServer = len(path) >= 2 && path[0] == "mcp_servers" && path[1] == name
			if inServer && !wasIn && len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
				kept = kept[:len(kept)-1]
			}
		}
		if inServer {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, ""), removed
}

// tomlHeader returns the key path of a table header line ([a.b] or [[a.b]]); ok is false for any
// other line.
func tomlHeader(line string) (path []string, ok bool) {
	rest := strings.TrimSpace(line)
	if !strings.HasPrefix(rest, "[") {
		return nil, false
	}
	array := strings.HasPrefix(rest, "[[")
	rest = strings.TrimPrefix(rest, "[")
	if array {
		rest = strings.TrimPrefix(rest, "[")
	}
	var key strings.Builder
	var quote byte
	end := -1
	for i := 0; i < len(rest) && end < 0; i++ {
		c := rest[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(rest) {
				key.WriteByte(c)
				i++
				c = rest[i]
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ']':
			end = i
			continue
		}
		key.WriteByte(c)
	}
	if end < 0 {
		return nil, false
	}
	tail := rest[end+1:]
	if array {
		if !strings.HasPrefix(tail, "]") {
			return nil, false
		}
		tail = tail[1:]
	}
	if tail = strings.TrimSpace(tail); tail != "" && !strings.HasPrefix(tail, "#") {
		return nil, false
	}
	return splitTOMLKey(key.String())
}

// splitTOMLKey splits a dotted key into its unquoted parts.
func splitTOMLKey(key string) ([]string, bool) {
	var parts []string
	for key = strings.TrimSpace(key); ; {
		var part string
		switch {
		case strings.HasPrefix(key, `"`):
			end := closingQuote(key)
			if end < 0 {
				return nil, false
			}
			unquoted, err := strconv.Unquote(key[:end+1])
			if err != nil {
				return nil, false
			}
			part, key = unquoted, key[end+1:]
		case strings.HasPrefix(key, "'"):
			end := strings.IndexByte(key[1:], '\'')
			if end < 0 {
				return nil, false
			}
			part, key = key[1:end+1], key[end+2:]
		default:
			end := strings.IndexByte(key, '.')
			if end < 0 {
				end = len(key)
			}
			part, key = strings.TrimSpace(key[:end]), key[end:]
			if part == "" {
				return nil, false
			}
		}
		parts = append(parts, part)
		key = strings.TrimSpace(key)
		if key == "" {
			return parts, true
		}
		if !strings.HasPrefix(key, ".") {
			return nil, false
		}
		key = strings.TrimSpace(key[1:])
	}
}

// closingQuote returns the index of the quote closing the basic string key starts with, or -1.
func closingQuote(key string) int {
	for i := 1; i < len(key); i++ {
		switch key[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// tomlKey writes name as a bare key when it can, else as a basic string.
func tomlKey(name string) string {
	if name == "" {
		return `""`
	}
	for _, r := range name {
		if !isBareKeyRune(r) {
			return tomlString(name)
		}
	}
	return name
}

func isBareKeyRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
}

// tomlString writes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			const hexDigits = "0123456789ABCDEF"
			b.WriteString(`\u00`)
			b.WriteByte(hexDigits[r>>4])
			b.WriteByte(hexDigits[r&0xf])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
