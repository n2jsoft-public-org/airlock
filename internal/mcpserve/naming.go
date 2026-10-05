package mcpserve

import (
	"path/filepath"
	"strings"
)

// DefaultServerPrefix starts server names when no prefix is configured.
const DefaultServerPrefix = "airlock"

// ServerName is the stable client-facing name of a session's server, e.g. airlock-dev-archive-db.
// The prefix is not repeated when the profile already carries it (e.g. prefix team, profile team-prod).
func ServerName(prefix, profile, target string) string {
	prefix = slugify(prefix)
	if prefix == "" {
		prefix = DefaultServerPrefix
	}
	name := slugify(profile)
	if name != prefix && !strings.HasPrefix(name, prefix+"-") {
		name = prefix + "-" + name
	}
	return name + "-" + slugify(target)
}

func slugify(value string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// RelativeTo joins paths with ", ", each relative to base when inside it, else to the home (~).
func RelativeTo(base string, paths []string) string {
	home, err := userHomeDir()
	if err != nil {
		home = ""
	}
	relative := make([]string, 0, len(paths))
	for _, path := range paths {
		if rel, ok := within(base, path); ok {
			path = rel
		} else if rel, ok := within(home, path); ok && home != "" {
			path = filepath.Join("~", rel)
		}
		relative = append(relative, path)
	}
	return strings.Join(relative, ", ")
}

// within returns path relative to dir when path is inside dir.
func within(dir, path string) (string, bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// DirectServerName is the stable client-facing name of a direct database's server, e.g.
// airlock-legacy-billing. The prefix is not repeated when the name already carries it.
func DirectServerName(prefix, name string) string {
	prefix = slugify(prefix)
	if prefix == "" {
		prefix = DefaultServerPrefix
	}
	name = slugify(name)
	if name == prefix || strings.HasPrefix(name, prefix+"-") {
		return name
	}
	return prefix + "-" + name
}
