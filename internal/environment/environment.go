// Package environment holds the environments of the environments setting: names given to AWS
// profiles by patterns, one of them production.
package environment

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// ProductionAlias always names the production environment: its name, or an alias of it.
const ProductionAlias = "prod"

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,11}$`)

// Env is the environment of a profile or a database; the zero Env is "none".
type Env struct {
	// Name is the environment's name, lower case; "" for none.
	Name string
	// Index is the position among the configured non-production environments, in config order;
	// meaningful only when Configured and not Production.
	Index int
	// Production marks the production environment.
	Production bool
	// Configured reports a name of the setting; a database may name one this setting lacks.
	Configured bool
}

// IsZero reports "no environment".
func (e Env) IsZero() bool { return e.Name == "" }

// entry is one environment of the setting.
type entry struct {
	name     string
	patterns []string
}

// Set is the parsed environments setting. The zero Set is Default: a production environment named
// prod, without patterns.
type Set struct {
	// production's name is "" in the zero Set, read as ProductionAlias.
	production entry
	others     []entry
}

// Default is the setting's default, "prod!=".
func Default() Set { return Set{} }

// FromProductionProfiles is the setting a productionProfiles value stands for: prod!=<patterns>.
// The patterns must be valid path.Match patterns.
func FromProductionProfiles(patterns []string) Set {
	return Set{production: entry{name: ProductionAlias, patterns: patterns}}
}

// Parse reads "name=patterns" entries separated by ";", a "!" after the name marking production.
// Names and patterns are trimmed and names lower-cased; empty entries and patterns are dropped. A
// blank string is Default; without "!", a production environment named prod is added.
func Parse(s string) (Set, error) {
	var set Set
	seen := map[string]bool{}
	for raw := range strings.SplitSeq(s, ";") {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		name, patterns, ok := strings.Cut(raw, "=")
		if !ok {
			return Set{}, fmt.Errorf("invalid entry %q: expected name=patterns, e.g. staging=*stg*", raw)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		production := strings.HasSuffix(name, "!")
		name = strings.TrimSpace(strings.TrimSuffix(name, "!"))
		if err := ValidName(name); err != nil {
			return Set{}, err
		}
		if seen[name] {
			return Set{}, fmt.Errorf("environment %s is given twice", name)
		}
		seen[name] = true
		parsed, err := parsePatterns(patterns)
		if err != nil {
			return Set{}, err
		}
		e := entry{name: name, patterns: parsed}
		switch {
		case production && set.production.name != "":
			return Set{}, errors.New("only one environment may be marked production (!)")
		case production:
			set.production = e
		case name == ProductionAlias:
			return Set{}, fmt.Errorf("%s is reserved for the production environment: write %s!=<patterns>", ProductionAlias, ProductionAlias)
		default:
			set.others = append(set.others, e)
		}
	}
	return set, nil
}

// parsePatterns splits comma-separated path.Match patterns, trimmed, empty ones dropped.
func parsePatterns(s string) ([]string, error) {
	var patterns []string
	for pattern := range strings.SplitSeq(s, ",") {
		if pattern = strings.TrimSpace(pattern); pattern == "" {
			continue
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf("invalid pattern %q (use * as wildcard, e.g. *prod*)", pattern)
		}
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

// ValidName returns an error when name cannot name an environment.
func ValidName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid environment name %q: 1 to 12 lower-case letters, digits or -, starting with a letter", name)
	}
	return nil
}

// productionEntry is the production environment, named prod when the setting names none.
func (s Set) productionEntry() entry {
	e := s.production
	if e.name == "" {
		e.name = ProductionAlias
	}
	return e
}

// String is the normalized setting: production first, then the others in config order.
func (s Set) String() string {
	format := func(e entry, marker string) string {
		return e.name + marker + "=" + strings.Join(e.patterns, ",")
	}
	parts := []string{format(s.productionEntry(), "!")}
	for _, e := range s.others {
		parts = append(parts, format(e, ""))
	}
	return strings.Join(parts, "; ")
}

// Production is the production environment.
func (s Set) Production() Env {
	return Env{Name: s.productionEntry().name, Production: true, Configured: true}
}

// ProductionPatterns returns the patterns of the production environment, none when it has none.
func (s Set) ProductionPatterns() []string {
	return slices.Clone(s.production.patterns)
}

// WithProductionPatterns returns s with the patterns of its production environment replaced.
func (s Set) WithProductionPatterns(patterns []string) Set {
	s.production.patterns = slices.Clone(patterns)
	return s
}

// Match returns the environment of an AWS profile: production when one of its patterns matches,
// else the first other environment with a matching pattern, else none.
func (s Set) Match(profile string) Env {
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == "" {
		return Env{}
	}
	if matches(s.production.patterns, profile) {
		return s.Production()
	}
	for idx, e := range s.others {
		if matches(e.patterns, profile) {
			return Env{Name: e.name, Index: idx, Configured: true}
		}
	}
	return Env{}
}

func matches(patterns []string, profile string) bool {
	for _, pattern := range patterns {
		if ok, err := path.Match(strings.ToLower(pattern), profile); err == nil && ok {
			return true
		}
	}
	return false
}

// Lookup returns the environment named name (any case): prod or the production name gives
// production; a name the setting lacks comes back unconfigured; "" gives none.
func (s Set) Lookup(name string) Env {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return Env{}
	}
	production := s.Production()
	if name == ProductionAlias || name == production.Name {
		return production
	}
	for idx, e := range s.others {
		if e.name == name {
			return Env{Name: e.name, Index: idx, Configured: true}
		}
	}
	return Env{Name: name}
}

// Names lists the configured environments, production first.
func (s Set) Names() []string {
	names := []string{s.productionEntry().name}
	for _, e := range s.others {
		names = append(names, e.name)
	}
	return names
}
