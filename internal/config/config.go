// Package config loads airlock's settings: the conventions of the AWS accounts it connects to.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
}

// Defaults are the built-in settings.
func Defaults() Config {
	return Config{
		JumphostTag:     awstag.Tag{Key: "Usage", Value: "jumphost"},
		MCPServerPrefix: "airlock",
	}
}

// file is the JSON layout of config.json.
type file struct {
	JumphostTag       *string `json:"jumphostTag"`
	DefaultDBUser     *string `json:"defaultDbUser"`
	ReadOnlySecretTag *string `json:"readOnlySecretTag"`
	MCPServerPrefix   *string `json:"mcpServerPrefix"`
	DefaultRegion     *string `json:"defaultRegion"`
}

// Load reads path over the defaults. A missing file (or an empty path) gives the defaults; invalid
// JSON, an unknown key or a malformed value is an error naming the file.
func Load(path string) (Config, error) {
	cfg := Defaults()
	if path == "" {
		return cfg, nil
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the path is the user's own config file.
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("failed to read %s: %w", path, err)
	}

	var f file
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&f); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}

	if f.JumphostTag != nil {
		if cfg.JumphostTag, err = parseRequiredTag(*f.JumphostTag); err != nil {
			return Config{}, fmt.Errorf("invalid config %s: jumphostTag: %w", path, err)
		}
	}
	if f.ReadOnlySecretTag != nil {
		if cfg.ReadOnlySecretTag, err = awstag.Parse(*f.ReadOnlySecretTag); err != nil {
			return Config{}, fmt.Errorf("invalid config %s: readOnlySecretTag: %w", path, err)
		}
	}
	if f.MCPServerPrefix != nil {
		if cfg.MCPServerPrefix, err = parsePrefix(*f.MCPServerPrefix); err != nil {
			return Config{}, fmt.Errorf("invalid config %s: mcpServerPrefix: %w", path, err)
		}
	}
	if f.DefaultDBUser != nil {
		cfg.DefaultDBUser = strings.TrimSpace(*f.DefaultDBUser)
	}
	if f.DefaultRegion != nil {
		cfg.DefaultRegion = strings.TrimSpace(*f.DefaultRegion)
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

func parsePrefix(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("must not be empty")
	}
	return s, nil
}
