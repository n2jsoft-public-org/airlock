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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/n2jsoft/airlock/internal/aiclient"
	"github.com/n2jsoft/airlock/internal/awstag"
	"github.com/n2jsoft/airlock/internal/dbuser"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
)

// Config holds the settings merged from the built-in defaults, the config file and the flags.
type Config struct {
	// JumphostTag finds the running EC2 instance used as SSM jumphost.
	JumphostTag awstag.Tag
	// ReadOnlySecretTag marks the per-database read-only Secrets Manager secrets of SQL Server
	// instances; zero disables read-only SQL Server MCP access.
	ReadOnlySecretTag awstag.Tag
	// DefaultDBUser are the users prefilled per engine on RDS when none is chosen: the AWS IAM user
	// of PostgreSQL and MySQL, the password user of SQL Server; zero means ask.
	DefaultDBUser dbuser.Defaults
	// MCPServerPrefix starts the MCP server names, e.g. <prefix>-<profile>-<instance>.
	MCPServerPrefix string
	// DefaultRegion is used after --region, AWS_REGION and the profile's region; empty means none.
	DefaultRegion string
	// Environments is the normalized environments setting (environment.Set.String): the names
	// given to AWS profiles by patterns, one of them production.
	Environments string
	// MCPClients selects, in the aiclient.Parse form, the AI clients the MCP servers are
	// registered in (normalized; auto by default).
	MCPClients string
	// Mode is ModeCLI or ModeDaemon.
	Mode string
	// AgentAutoConnect is AutoConnectNone, AutoConnectNonProd or AutoConnectAll: the agent openings
	// that skip the dashboard prompt.
	AgentAutoConnect string
	// Language is LanguageAuto or one of i18n.Supported.
	Language string
	// MCPGatewayPort is the 127.0.0.1 port of the gateway.
	MCPGatewayPort int
	// AgentIdleTimeout closes an agent-opened session after this long without a relayed call.
	AgentIdleTimeout time.Duration
	// MCPGateway serves the MCP gateway in daemon mode.
	MCPGateway bool
	// AgentWrites lets agents ask for MCP writes (always prompted).
	AgentWrites bool
	// RandomLocalPort binds SSM tunnels to a free port chosen by the OS instead of the target's port.
	RandomLocalPort bool
}

// Modes of the mode setting.
const (
	// ModeCLI keeps the sessions in each airlock, closed with it.
	ModeCLI = "cli"
	// ModeDaemon keeps the sessions in a background daemon; airlock is a view of it.
	ModeDaemon = "daemon"
)

// Values of the agentAutoConnect setting.
const (
	AutoConnectNone    = "none"
	AutoConnectNonProd = "non-prod"
	AutoConnectAll     = "all"
)

// LanguageAuto follows the system language.
const LanguageAuto = "auto"

// DefaultGatewayPort is the port of the MCP gateway when mcpGatewayPort is not set.
const DefaultGatewayPort = 7821

// minAgentIdleTimeout is the shortest agentIdleTimeout accepted.
const minAgentIdleTimeout = time.Minute

// EnvironmentSet returns the parsed Environments.
func (c *Config) EnvironmentSet() environment.Set {
	set, err := environment.Parse(c.Environments)
	if err != nil {
		// Load only keeps valid values; a hand-built Config falls back to the default.
		return environment.Default()
	}
	return set
}

// MCPClientSelection returns the AI clients selected by MCPClients.
func (c *Config) MCPClientSelection() aiclient.Selection {
	sel, err := aiclient.Parse(c.MCPClients)
	if err != nil {
		// Load and Apply only keep valid values; a hand-built Config falls back to the default.
		return aiclient.AutoDetect
	}
	return sel
}

// Defaults are the built-in settings.
func Defaults() Config {
	return Config{
		JumphostTag:     awstag.Tag{Key: "Usage", Value: "jumphost"},
		MCPServerPrefix: "airlock",
		Environments:    environment.Default().String(),
		MCPClients:      aiclient.Auto,
		Mode:            ModeCLI,

		MCPGateway:       true,
		MCPGatewayPort:   DefaultGatewayPort,
		AgentAutoConnect: AutoConnectNone,
		AgentIdleTimeout: 30 * time.Minute,
		RandomLocalPort:  true,
		Language:         LanguageAuto,
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
	Environments      *string `json:"environments,omitempty"`
	MCPClients        *string `json:"mcpClients,omitempty"`
	Mode              *string `json:"mode,omitempty"`
	MCPGateway        *string `json:"mcpGateway,omitempty"`
	MCPGatewayPort    *string `json:"mcpGatewayPort,omitempty"`
	AgentAutoConnect  *string `json:"agentAutoConnect,omitempty"`
	AgentWrites       *string `json:"agentWrites,omitempty"`
	AgentIdleTimeout  *string `json:"agentIdleTimeout,omitempty"`
	RandomLocalPort   *string `json:"randomLocalPort,omitempty"`
	Language          *string `json:"language,omitempty"`
	// ProductionProfiles is the deprecated key environments replaces: read while environments is
	// not set, never written by airlock.
	ProductionProfiles *string `json:"productionProfiles,omitempty"`
}

// Load reads configPath over the defaults. A missing file (or an empty path) gives the defaults; invalid
// JSON, an unknown key or a malformed value is an error naming the file.
func Load(configPath string) (Config, error) {
	f, err := readFile(configPath)
	if err != nil {
		return Config{}, err
	}
	cfg, err := f.apply(new(Defaults()))
	if err != nil {
		return Config{}, i18n.NewError("config.config.invalid_config", configPath, err)
	}
	return cfg, nil
}

// readFile decodes configPath; a missing file (or an empty path) has no key. Invalid JSON or an unknown
// key is an error naming the file.
func readFile(configPath string) (file, error) {
	var f file
	if configPath == "" {
		return f, nil
	}
	raw, err := os.ReadFile(configPath) // #nosec G304 -- the path is the user's own config file.
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return file{}, i18n.NewError("config.config.failed_to_read", configPath, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&f); err != nil {
		return file{}, i18n.NewError("config.config.invalid_config", configPath, err)
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
		if cfg.DefaultDBUser, err = dbuser.Parse(*f.DefaultDBUser); err != nil {
			return Config{}, fmt.Errorf("defaultDbUser: %w", err)
		}
	}
	if f.DefaultRegion != nil {
		cfg.DefaultRegion = strings.TrimSpace(*f.DefaultRegion)
	}
	switch {
	case f.Environments != nil:
		set, parseErr := environment.Parse(*f.Environments)
		if parseErr != nil {
			return Config{}, fmt.Errorf("environments: %w", parseErr)
		}
		cfg.Environments = set.String()
	case f.ProductionProfiles != nil:
		patterns, parseErr := parseProfilePatterns(*f.ProductionProfiles)
		if parseErr != nil {
			return Config{}, fmt.Errorf("productionProfiles: %w", parseErr)
		}
		cfg.Environments = environment.FromProductionProfiles(patterns).String()
	}
	if f.MCPClients != nil {
		if cfg.MCPClients, err = parseMCPClients(*f.MCPClients); err != nil {
			return Config{}, fmt.Errorf("mcpClients: %w", err)
		}
	}
	if f.Mode != nil {
		if cfg.Mode, err = parseMode(*f.Mode); err != nil {
			return Config{}, fmt.Errorf("mode: %w", err)
		}
	}
	if f.MCPGateway != nil {
		if cfg.MCPGateway, err = parseBool(*f.MCPGateway); err != nil {
			return Config{}, fmt.Errorf("mcpGateway: %w", err)
		}
	}
	if f.MCPGatewayPort != nil {
		if cfg.MCPGatewayPort, err = parsePort(*f.MCPGatewayPort); err != nil {
			return Config{}, fmt.Errorf("mcpGatewayPort: %w", err)
		}
	}
	if f.AgentAutoConnect != nil {
		if cfg.AgentAutoConnect, err = parseAutoConnect(*f.AgentAutoConnect); err != nil {
			return Config{}, fmt.Errorf("agentAutoConnect: %w", err)
		}
	}
	if f.AgentWrites != nil {
		if cfg.AgentWrites, err = parseBool(*f.AgentWrites); err != nil {
			return Config{}, fmt.Errorf("agentWrites: %w", err)
		}
	}
	if f.AgentIdleTimeout != nil {
		if cfg.AgentIdleTimeout, err = parseIdleTimeout(*f.AgentIdleTimeout); err != nil {
			return Config{}, fmt.Errorf("agentIdleTimeout: %w", err)
		}
	}
	if f.RandomLocalPort != nil {
		if cfg.RandomLocalPort, err = parseBool(*f.RandomLocalPort); err != nil {
			return Config{}, fmt.Errorf("randomLocalPort: %w", err)
		}
	}
	if f.Language != nil {
		if cfg.Language, err = parseLanguage(*f.Language); err != nil {
			return Config{}, fmt.Errorf("language: %w", err)
		}
	}
	return cfg, nil
}

// Overrides are the flags given on the command line; nil means the flag was not given.
type Overrides struct {
	JumphostTag       *string
	ReadOnlySecretTag *string
	MCPServerPrefix   *string
	MCPClients        *string
	Mode              *string
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
	if o.MCPClients != nil {
		if out.MCPClients, err = parseMCPClients(*o.MCPClients); err != nil {
			return Config{}, fmt.Errorf("--mcp-clients: %w", err)
		}
	}
	if o.Mode != nil {
		if out.Mode, err = parseMode(*o.Mode); err != nil {
			return Config{}, fmt.Errorf("--mode: %w", err)
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
		return awstag.Tag{}, i18n.NewError("config.config.must_not_be_empty")
	}
	return tag, nil
}

// parseProfilePatterns reads the comma-separated patterns of the deprecated productionProfiles:
// trimmed, empty ones dropped, each a valid path.Match pattern.
func parseProfilePatterns(s string) ([]string, error) {
	var patterns []string
	for pattern := range strings.SplitSeq(s, ",") {
		if pattern = strings.TrimSpace(pattern); pattern == "" {
			continue
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return nil, i18n.NewError("config.config.invalid_pattern_use_as", pattern)
		}
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

// parseMCPClients normalizes a client selection (see aiclient.Parse).
func parseMCPClients(s string) (string, error) {
	sel, err := aiclient.Parse(s)
	if err != nil {
		return "", err
	}
	return sel.String(), nil
}

func parseMode(s string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(s)); mode {
	case ModeCLI, ModeDaemon:
		return mode, nil
	default:
		return "", i18n.NewError("config.config.unknown_mode_valid", s, ModeCLI, ModeDaemon)
	}
}

func parseLanguage(s string) (string, error) {
	lang := strings.ToLower(strings.TrimSpace(s))
	if lang == LanguageAuto || slices.Contains(i18n.Supported, lang) {
		return lang, nil
	}
	return "", i18n.NewError("config.config.unknown_language_valid", s, LanguageAuto, strings.Join(i18n.Supported, ", "))
}

func parsePrefix(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", i18n.NewError("config.config.must_not_be_empty_2")
	}
	return s, nil
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, i18n.NewError("config.config.invalid_value_valid_true", s)
	}
}

func parsePort(s string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || port < 1024 || port > 65535 {
		return 0, i18n.NewError("config.config.invalid_port_valid_1024", s)
	}
	return port, nil
}

func parseAutoConnect(s string) (string, error) {
	switch value := strings.ToLower(strings.TrimSpace(s)); value {
	case AutoConnectNone, AutoConnectNonProd, AutoConnectAll:
		return value, nil
	default:
		return "", i18n.NewError("config.config.unknown_value_valid", s, AutoConnectNone, AutoConnectNonProd, AutoConnectAll)
	}
}

func parseIdleTimeout(s string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil || d < minAgentIdleTimeout {
		return 0, i18n.NewError("config.config.invalid_duration_duration_of", s)
	}
	return d, nil
}

// FormatDuration renders d as config.json writes it: 30m, 2h, 1h30m.
func FormatDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0 && d < time.Hour:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h" + strconv.FormatInt(int64(d%time.Hour/time.Minute), 10) + "m"
	default:
		return d.String()
	}
}
