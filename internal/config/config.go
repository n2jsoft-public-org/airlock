// Package config loads airlock's settings: the conventions of the AWS accounts it connects to.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/n2jsoft/airlock/internal/awstag"
)

// Config holds the settings merged from the built-in defaults, the config file and the flags.
type Config struct {
	// JumphostTag finds the running EC2 instance used as SSM jumphost.
	JumphostTag awstag.Tag
	// DefaultDBUser signs in with AWS IAM on RDS when no user is chosen; empty means ask.
	DefaultDBUser string
	// ReadOnlySecretTag marks the per-database read-only Secrets Manager secrets of SQL Server
	// instances; zero disables read-only SQL Server MCP access.
	ReadOnlySecretTag awstag.Tag
	// MCPServerPrefix starts the MCP server names, e.g. <prefix>-<profile>-<instance>.
	MCPServerPrefix string
	// DefaultRegion is used after --region, AWS_REGION and the profile's region; empty means none.
	DefaultRegion string
	// ProductionProfiles lists, comma-separated, the patterns (* wildcard, any case) of the AWS
	// profiles flagged as production; empty flags none.
	ProductionProfiles string
}

// ProductionProfilePatterns returns the patterns of ProductionProfiles; none when empty.
func (c *Config) ProductionProfilePatterns() []string {
	if c.ProductionProfiles == "" {
		return nil
	}
	return strings.Split(c.ProductionProfiles, ",")
}

// Defaults are the built-in settings.
func Defaults() Config {
	return Config{
		JumphostTag:     awstag.Tag{Key: "Usage", Value: "jumphost"},
		MCPServerPrefix: "airlock",
	}
}

// file is the JSON layout of config.json; a nil field is a key absent from the file. Fields are
// in the order the file is written in.
type file struct {
	JumphostTag       *string `json:"jumphostTag,omitempty"`
	DefaultDBUser     *string `json:"defaultDbUser,omitempty"`
	ReadOnlySecretTag *string `json:"readOnlySecretTag,omitempty"`
	MCPServerPrefix   *string `json:"mcpServerPrefix,omitempty"`
	DefaultRegion     *string `json:"defaultRegion,omitempty"`
	// ProductionProfiles is last: it was added after the others.
	ProductionProfiles *string `json:"productionProfiles,omitempty"`
}

// Load reads path over the defaults. A missing file (or an empty path) gives the defaults; invalid
// JSON, an unknown key or a malformed value is an error naming the file.
func Load(path string) (Config, error) {
	f, err := readFile(path)
	if err != nil {
		return Config{}, err
	}
	defaults := Defaults()
	cfg, err := f.apply(&defaults)
	if err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// readFile decodes path; a missing file (or an empty path) has no key. Invalid JSON or an unknown
// key is an error naming the file.
func readFile(path string) (file, error) {
	var f file
	if path == "" {
		return f, nil
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the path is the user's own config file.
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return file{}, fmt.Errorf("failed to read %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&f); err != nil {
		return file{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return f, nil
}

// apply returns base with the keys of f over it; a malformed value is an error naming its key.
func (f *file) apply(base *Config) (Config, error) {
	cfg := *base
	var err error
	if f.JumphostTag != nil {
		if cfg.JumphostTag, err = parseRequiredTag(*f.JumphostTag); err != nil {
			return Config{}, fmt.Errorf("jumphostTag: %w", err)
		}
	}
	if f.ReadOnlySecretTag != nil {
		if cfg.ReadOnlySecretTag, err = awstag.Parse(*f.ReadOnlySecretTag); err != nil {
			return Config{}, fmt.Errorf("readOnlySecretTag: %w", err)
		}
	}
	if f.MCPServerPrefix != nil {
		if cfg.MCPServerPrefix, err = parsePrefix(*f.MCPServerPrefix); err != nil {
			return Config{}, fmt.Errorf("mcpServerPrefix: %w", err)
		}
	}
	if f.DefaultDBUser != nil {
		cfg.DefaultDBUser = strings.TrimSpace(*f.DefaultDBUser)
	}
	if f.DefaultRegion != nil {
		cfg.DefaultRegion = strings.TrimSpace(*f.DefaultRegion)
	}
	if f.ProductionProfiles != nil {
		if cfg.ProductionProfiles, err = parseProfilePatterns(*f.ProductionProfiles); err != nil {
			return Config{}, fmt.Errorf("productionProfiles: %w", err)
		}
	}
	return cfg, nil
}

// Overrides are the flags given on the command line; nil means the flag was not given.
type Overrides struct {
	JumphostTag       *string
	ReadOnlySecretTag *string
	MCPServerPrefix   *string
}

// Apply returns a copy of c with the given flags applied over it.
func (c *Config) Apply(o Overrides) (Config, error) {
	out := *c
	var err error
	if o.JumphostTag != nil {
		if out.JumphostTag, err = parseRequiredTag(*o.JumphostTag); err != nil {
			return Config{}, fmt.Errorf("--jumphost-tag: %w", err)
		}
	}
	if o.ReadOnlySecretTag != nil {
		if out.ReadOnlySecretTag, err = awstag.Parse(*o.ReadOnlySecretTag); err != nil {
			return Config{}, fmt.Errorf("--read-only-secret-tag: %w", err)
		}
	}
	if o.MCPServerPrefix != nil {
		if out.MCPServerPrefix, err = parsePrefix(*o.MCPServerPrefix); err != nil {
			return Config{}, fmt.Errorf("--mcp-server-prefix: %w", err)
		}
	}
	return out, nil
}

func parseRequiredTag(s string) (awstag.Tag, error) {
	tag, err := awstag.Parse(s)
	if err != nil {
		return awstag.Tag{}, err
	}
	if tag.IsZero() {
		return awstag.Tag{}, errors.New("must not be empty (expected Key=Value)")
	}
	return tag, nil
}

// parseProfilePatterns normalizes comma-separated profile patterns: trimmed, empty ones dropped,
// each a valid path.Match pattern.
func parseProfilePatterns(s string) (string, error) {
	var patterns []string
	for _, pattern := range strings.Split(s, ",") {
		if pattern = strings.TrimSpace(pattern); pattern == "" {
			continue
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return "", fmt.Errorf("invalid pattern %q (use * as wildcard, e.g. *prod*)", pattern)
		}
		patterns = append(patterns, pattern)
	}
	return strings.Join(patterns, ","), nil
}

func parsePrefix(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("must not be empty")
	}
	return s, nil
}
