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
	"github.com/n2jsoft/airlock/internal/orgconfig"
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
	// OrganizationConfig is the source of the organization config: an absolute path or an
	// https:// URL; empty means none.
	OrganizationConfig string
	// MCPGatewayPort is the 127.0.0.1 port of the gateway.
	MCPGatewayPort int
	// AgentIdleTimeout closes an agent-opened session after this long without a relayed call.
	AgentIdleTimeout time.Duration
	// DaemonIdleTimeout stops the daemon after this long without session, view nor gateway call.
	DaemonIdleTimeout time.Duration
	// MCPGateway serves the MCP gateway in daemon mode.
	MCPGateway bool
	// AgentWrites lets agents ask for MCP writes (always prompted).
	AgentWrites bool
	// RandomLocalPort binds SSM tunnels to a free port chosen by the OS instead of the target's port.
	RandomLocalPort bool
	// DisabledModules are the Modules airlock neither lists nor opens, in Modules order.
	DisabledModules []string
}

// Modules are the target families modules.<name>.enabled can turn off.
var Modules = []string{"sql", "k8s", "cache", "dynamodb", "s3", "logs"}

// Modes of the mode setting.
const (
	// ModeCLI keeps the sessions in each airlock, closed with it.
	ModeCLI = "cli"
	// ModeDaemon keeps the sessions in a background daemon; airlock is a view of it.
	ModeDaemon = "daemon"
)

// Values of the agent.autoConnect setting.
const (
	AutoConnectNone    = "none"
	AutoConnectNonProd = "non-prod"
	AutoConnectAll     = "all"
)

// LanguageAuto follows the system language.
const LanguageAuto = "auto"

// DefaultGatewayPort is the port of the MCP gateway when mcp.gateway.port is not set.
const DefaultGatewayPort = 7821

// minIdleTimeout is the shortest agent.idleTimeout and daemon.idleTimeout accepted.
const minIdleTimeout = time.Minute

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

		DaemonIdleTimeout: 10 * time.Minute,

		MCPGateway:       true,
		MCPGatewayPort:   DefaultGatewayPort,
		AgentAutoConnect: AutoConnectNone,
		AgentIdleTimeout: 30 * time.Minute,
		RandomLocalPort:  true,
		Language:         LanguageAuto,
	}
}

// file holds the keys config.json sets, one field per key of Keys; a nil field is a key absent
// from the file. Its JSON layout (file_json.go) nests the dotted names of Keys.
type file struct {
	Language           *string
	OrganizationConfig *string
	Mode               *string
	Environments       *string
	DefaultRegion      *string
	JumphostTag        *string
	RandomLocalPort    *string
	MCPServerPrefix    *string
	MCPClients         *string
	MCPGateway         *string
	MCPGatewayPort     *string
	AgentAutoConnect   *string
	AgentWrites        *string
	AgentIdleTimeout   *string
	DaemonIdleTimeout  *string
	SQLEnabled         *string
	DefaultDBUser      *string
	ReadOnlySecretTag  *string
	K8sEnabled         *string
	CacheEnabled       *string
	DynamoDBEnabled    *string
	S3Enabled          *string
	LogsEnabled        *string
	// ProductionProfiles is the deprecated key environments replaces: read while environments is
	// not set, never written by airlock.
	ProductionProfiles *string
	// legacy lists the flat keys of an earlier layout the file still sets, in file order; their
	// values are already in the fields above.
	legacy []legacyKey
	// lenient makes decoding record unknown keys in unknown instead of failing; the organization
	// file may come from a newer airlock.
	lenient bool
	unknown []string
}

// Load reads configPath over the settings of org, a nil one setting nothing, over the defaults;
// the keys org locks ignore the file. A missing file (or an empty path) gives the layers below;
// invalid JSON, an unknown key or a malformed value is an error naming the file.
func Load(configPath string, org *Organization) (Config, error) {
	_, cfg, _, err := load(configPath, org)
	return cfg, err
}

// load returns the file at configPath, the Config of Load, and the base it applied the file over:
// the defaults with the settings of org.
func load(configPath string, org *Organization) (f file, cfg, base Config, err error) {
	if f, err = readFile(configPath); err != nil {
		return file{}, Config{}, Config{}, err
	}
	if base, err = org.apply(new(Defaults())); err != nil {
		return file{}, Config{}, Config{}, err
	}
	local := f
	local.dropLocked(org)
	if cfg, err = local.apply(&base); err != nil {
		return file{}, Config{}, Config{}, i18n.NewError("config.config.invalid_config", configPath, err)
	}
	return f, cfg, base, nil
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
			return Config{}, fmt.Errorf("aws.jumphostTag: %w", err)
		}
	}
	if f.ReadOnlySecretTag != nil {
		if cfg.ReadOnlySecretTag, err = awstag.Parse(*f.ReadOnlySecretTag); err != nil {
			return Config{}, fmt.Errorf("modules.sql.readOnlySecretTag: %w", err)
		}
	}
	if f.MCPServerPrefix != nil {
		if cfg.MCPServerPrefix, err = parsePrefix(*f.MCPServerPrefix); err != nil {
			return Config{}, fmt.Errorf("mcp.serverPrefix: %w", err)
		}
	}
	if f.DefaultDBUser != nil {
		if cfg.DefaultDBUser, err = dbuser.Parse(*f.DefaultDBUser); err != nil {
			return Config{}, fmt.Errorf("modules.sql.defaultDbUser: %w", err)
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
			return Config{}, fmt.Errorf("mcp.clients: %w", err)
		}
	}
	if f.Mode != nil {
		if cfg.Mode, err = parseMode(*f.Mode); err != nil {
			return Config{}, fmt.Errorf("mode: %w", err)
		}
	}
	if f.MCPGateway != nil {
		if cfg.MCPGateway, err = parseBool(*f.MCPGateway); err != nil {
			return Config{}, fmt.Errorf("mcp.gateway.enabled: %w", err)
		}
	}
	if f.MCPGatewayPort != nil {
		if cfg.MCPGatewayPort, err = parsePort(*f.MCPGatewayPort); err != nil {
			return Config{}, fmt.Errorf("mcp.gateway.port: %w", err)
		}
	}
	if f.AgentAutoConnect != nil {
		if cfg.AgentAutoConnect, err = parseAutoConnect(*f.AgentAutoConnect); err != nil {
			return Config{}, fmt.Errorf("agent.autoConnect: %w", err)
		}
	}
	if f.AgentWrites != nil {
		if cfg.AgentWrites, err = parseBool(*f.AgentWrites); err != nil {
			return Config{}, fmt.Errorf("agent.writes: %w", err)
		}
	}
	if f.DaemonIdleTimeout != nil {
		if cfg.DaemonIdleTimeout, err = parseIdleTimeout(*f.DaemonIdleTimeout); err != nil {
			return Config{}, fmt.Errorf("daemon.idleTimeout: %w", err)
		}
	}
	if f.AgentIdleTimeout != nil {
		if cfg.AgentIdleTimeout, err = parseIdleTimeout(*f.AgentIdleTimeout); err != nil {
			return Config{}, fmt.Errorf("agent.idleTimeout: %w", err)
		}
	}
	if f.RandomLocalPort != nil {
		if cfg.RandomLocalPort, err = parseBool(*f.RandomLocalPort); err != nil {
			return Config{}, fmt.Errorf("aws.randomLocalPort: %w", err)
		}
	}
	if cfg.DisabledModules, err = f.disabledModules(cfg.DisabledModules); err != nil {
		return Config{}, err
	}
	if f.Language != nil {
		if cfg.Language, err = parseLanguage(*f.Language); err != nil {
			return Config{}, fmt.Errorf("language: %w", err)
		}
	}
	if f.OrganizationConfig != nil {
		if cfg.OrganizationConfig, err = parseOrganizationSource(*f.OrganizationConfig); err != nil {
			return Config{}, fmt.Errorf("organizationConfig: %w", err)
		}
	}
	return cfg, nil
}

// disabledModules returns the Modules turned off: those whose modules.<name>.enabled f sets to
// false, plus those of base f leaves unset, in Modules order.
func (f *file) disabledModules(base []string) ([]string, error) {
	var disabled []string
	for _, module := range Modules {
		key := moduleEnabledKey(module)
		value := *f.field(key)
		if value == nil {
			if slices.Contains(base, module) {
				disabled = append(disabled, module)
			}
			continue
		}
		enabled, err := parseBool(*value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if !enabled {
			disabled = append(disabled, module)
		}
	}
	return disabled, nil
}

// moduleEnabledKey is the key turning module on or off.
func moduleEnabledKey(module string) string {
	return "modules." + module + ".enabled"
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

// parseOrganizationSource accepts the sources the organization resolver reads; empty means no
// organization. The error never echoes URL credentials.
func parseOrganizationSource(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return s, nil
	}
	if _, err := orgconfig.ParseSource(s); err != nil {
		return "", err
	}
	return s, nil
}

// parseModules reads comma-separated Modules, in any case; the result is in Modules order.
func parseModules(s string) ([]string, error) {
	var modules []string
	for name := range strings.SplitSeq(s, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if !slices.Contains(Modules, name) {
			return nil, i18n.NewError("config.config.unknown_module_valid", name, strings.Join(Modules, ", "))
		}
		modules = append(modules, name)
	}
	if len(modules) == 0 {
		return nil, nil
	}
	return slices.DeleteFunc(slices.Clone(Modules), func(m string) bool { return !slices.Contains(modules, m) }), nil
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
	if err != nil || d < minIdleTimeout {
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
