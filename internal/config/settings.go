package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/n2jsoft/airlock/internal/environment"
)

// Key describes one setting of config.json.
type Key struct {
	Name        string
	Description string
}

// Keys lists the settings of config.json, in the order the file is written in.
var Keys = []Key{
	{Name: "jumphostTag", Description: "Tag (Key=Value) of the running SSM jumphost EC2 instance"},
	{Name: "defaultDbUser", Description: "Database user prefilled on RDS when none is chosen: a user signing in with AWS IAM on PostgreSQL and MySQL, and/or engine=user pairs (postgres, mysql, sqlserver; SQL Server signs in with a password), e.g. app_ro,sqlserver=mcp_ro"},
	{Name: "readOnlySecretTag", Description: "Tag (Key=Value) of the per-database read-only Secrets Manager secrets of SQL Server"},
	{Name: "mcpServerPrefix", Description: "Start of the MCP server names"},
	{Name: "defaultRegion", Description: "Region used after --region, AWS_REGION and the profile's region"},
	{Name: "environments", Description: "Environments badged in the TUI, separated by ;, each name=patterns of AWS profiles (* wildcard, any case); ! marks production, e.g. prod!=*prod*,*-live; staging=*stg*; dev=*dev*"},
	{Name: "mcpClients", Description: "AI clients the MCP servers are registered in: auto (detected in the project), none, or a comma-separated list of claude, vscode, codex, cursor, gemini, each with :user for its user-level file (claude:user also covers Claude Desktop), e.g. claude:user,codex"},
	{Name: "mode", Description: "cli: sessions live in each airlock and close with it; daemon: a background daemon keeps them, and airlock is a view of it"},
	{Name: "mcpGateway", Description: "true: the daemon serves the MCP gateway, through which AI agents list targets and open sessions; false: no gateway"},
	{Name: "mcpGatewayPort", Description: "Port of the MCP gateway on 127.0.0.1 (1024-65535)"},
	{Name: "agentAutoConnect", Description: "Agent openings that skip the dashboard prompt: none, non-prod (targets outside the production environment) or all"},
	{Name: "agentWrites", Description: "true: agents may ask for MCP writes, always approved in a dashboard; false: agents only open read-only sessions"},
	{Name: "agentIdleTimeout", Description: "Close a session opened by an agent after this long without a call through the gateway, e.g. 30m or 2h (at least 1m)"},
	{Name: "randomLocalPort", Description: "true: SSM tunnels listen on a free local port chosen by the OS, avoiding conflicts; false: on the target's port (or the next free one)"},
}

// Source tells where the value of a setting comes from.
type Source string

const (
	SourceDefault Source = "default"
	SourceFile    Source = "file"
)

// SourceDeprecated marks a deprecated key the file still sets.
const SourceDeprecated Source = "deprecated"

// legacyProductionProfiles is the key environments replaces.
const legacyProductionProfiles = "productionProfiles"

// deprecatedProductionProfiles describes productionProfiles in Settings.
var deprecatedProductionProfiles = Key{
	Name:        legacyProductionProfiles,
	Description: "Deprecated: replaced by environments (read as prod!=<patterns> while environments is not set)",
}

// Setting is the effective value of one key; an empty Value means none.
type Setting struct {
	Key
	Value  string
	Source Source
}

// ErrFileExists refuses to replace a config file by the template.
var ErrFileExists = errors.New("config file already exists")

// Settings returns the effective value of every key of path, in Keys order. A missing file gives
// the defaults; an invalid file is an error naming it.
func Settings(path string) ([]Setting, error) {
	f, err := readFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := f.apply(new(Defaults()))
	if err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	values := cfg.values()
	settings := make([]Setting, 0, len(Keys))
	for _, key := range Keys {
		source := SourceDefault
		if f.sets(key.Name) {
			source = SourceFile
		}
		settings = append(settings, Setting{Key: key, Value: values[key.Name], Source: source})
	}
	if f.ProductionProfiles != nil {
		value := strings.TrimSpace(*f.ProductionProfiles)
		if f.Environments != nil {
			value += " (ignored: environments is set)"
		}
		settings = append(settings, Setting{Key: deprecatedProductionProfiles, Value: value, Source: SourceDeprecated})
	}
	return settings, nil
}

// Set validates value for key and writes it into path, keeping the other keys of the file. The
// file is created when missing; a file that cannot be parsed, or whose other keys are invalid, is
// never rewritten. Setting an invalid key repairs it.
func Set(path, key, value string) error {
	f, err := readFile(path)
	if err != nil {
		return err
	}
	if key == legacyProductionProfiles {
		return fmt.Errorf("productionProfiles is replaced by environments: airlock config set environments 'prod!=%s'", strings.TrimSpace(value))
	}
	field, err := f.knownField(key)
	if err != nil {
		return err
	}
	defaults := Defaults()
	*field = nil
	legacy := f.ProductionProfiles
	if key == "environments" {
		// environments replaces the deprecated key: it leaves the file with it.
		f.ProductionProfiles = nil
	}
	if _, err = f.apply(&defaults); err != nil {
		return fmt.Errorf("invalid config %s: %w", path, err)
	}
	value = strings.TrimSpace(value)
	*field = &value
	if _, err = f.apply(&defaults); err != nil {
		return err
	}
	if key == "environments" {
		if keepErr := keepsLegacyProduction(legacy, value); keepErr != nil {
			return keepErr
		}
	}
	return writeFile(path, &f)
}

// keepsLegacyProduction refuses an environments value that would drop the production patterns of
// the productionProfiles the file still sets: they would silently stop flagging production.
func keepsLegacyProduction(legacy *string, value string) error {
	if legacy == nil {
		return nil
	}
	patterns, err := parseProfilePatterns(*legacy)
	if err != nil || len(patterns) == 0 {
		return nil //nolint:nilerr // an invalid or empty productionProfiles flags nothing to keep.
	}
	set, err := environment.Parse(value)
	if err != nil || len(set.ProductionPatterns()) > 0 {
		return nil //nolint:nilerr // apply validated value; production keeps its own patterns.
	}
	return fmt.Errorf("your productionProfiles flags %s: keep them as production, e.g. airlock config set environments '%s', "+
		"or drop them with airlock config unset productionProfiles", strings.Join(patterns, ","), set.WithProductionPatterns(patterns))
}

// Unset removes key from path, so that its default applies again; removed is false when the key
// was not in the file.
func Unset(path, key string) (removed bool, err error) {
	f, err := readFile(path)
	if err != nil {
		return false, err
	}
	field, err := f.knownField(key)
	if err != nil {
		return false, err
	}
	if !f.sets(key) {
		return false, nil
	}
	*field = nil
	if key == "environments" {
		// environments also covers the deprecated key it replaces.
		f.ProductionProfiles = nil
	}
	return true, writeFile(path, &f)
}

// WriteTemplate writes every key with its default value to path. An existing file is refused with
// ErrFileExists unless force is set; it is then first copied to path + ".bak".
func WriteTemplate(path string, force bool) error {
	existing, err := os.ReadFile(path) // #nosec G304 -- the path is the user's own config file.
	switch {
	case err == nil && !force:
		return fmt.Errorf("%s: %w", path, ErrFileExists)
	case err == nil:
		if backupErr := writeAtomic(path+".bak", existing); backupErr != nil {
			return backupErr
		}
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("failed to read %s: %w", path, err)
	}

	defaults := Defaults()
	values := defaults.values()
	var f file
	for _, key := range Keys {
		*f.field(key.Name) = new(values[key.Name])
	}
	return writeFile(path, &f)
}

// values renders every setting of c as written in config.json.
func (c *Config) values() map[string]string {
	return map[string]string{
		"jumphostTag":       c.JumphostTag.String(),
		"defaultDbUser":     c.DefaultDBUser.String(),
		"readOnlySecretTag": c.ReadOnlySecretTag.String(),
		"mcpServerPrefix":   c.MCPServerPrefix,
		"defaultRegion":     c.DefaultRegion,
		"environments":      c.Environments,
		"mcpClients":        c.MCPClients,
		"mode":              c.Mode,
		"mcpGateway":        strconv.FormatBool(c.MCPGateway),
		"mcpGatewayPort":    strconv.Itoa(c.MCPGatewayPort),
		"agentAutoConnect":  c.AgentAutoConnect,
		"agentWrites":       strconv.FormatBool(c.AgentWrites),
		"agentIdleTimeout":  FormatDuration(c.AgentIdleTimeout),
		"randomLocalPort":   strconv.FormatBool(c.RandomLocalPort),
	}
}

// field returns the field of f holding key, which must be one of Keys.
func (f *file) field(key string) **string {
	switch key {
	case "jumphostTag":
		return &f.JumphostTag
	case "defaultDbUser":
		return &f.DefaultDBUser
	case "readOnlySecretTag":
		return &f.ReadOnlySecretTag
	case "mcpServerPrefix":
		return &f.MCPServerPrefix
	case "defaultRegion":
		return &f.DefaultRegion
	case "environments":
		return &f.Environments
	case "mcpClients":
		return &f.MCPClients
	case "mode":
		return &f.Mode
	case "mcpGateway":
		return &f.MCPGateway
	case "mcpGatewayPort":
		return &f.MCPGatewayPort
	case "agentAutoConnect":
		return &f.AgentAutoConnect
	case "agentWrites":
		return &f.AgentWrites
	case "agentIdleTimeout":
		return &f.AgentIdleTimeout
	case "randomLocalPort":
		return &f.RandomLocalPort
	case legacyProductionProfiles:
		return &f.ProductionProfiles
	default:
		return nil
	}
}

// sets reports whether f sets key; environments is also set by the deprecated productionProfiles.
func (f *file) sets(key string) bool {
	if key == "environments" && f.ProductionProfiles != nil {
		return true
	}
	field := f.field(key)
	return field != nil && *field != nil
}

// knownField is field, with an error listing the valid keys for any other key.
func (f *file) knownField(key string) (**string, error) {
	if field := f.field(key); field != nil {
		return field, nil
	}
	names := make([]string, 0, len(Keys))
	for _, k := range Keys {
		names = append(names, k.Name)
	}
	return nil, fmt.Errorf("unknown setting %q (valid: %s)", key, strings.Join(names, ", "))
}

// writeFile stores f as indented JSON, keys absent from f left out.
func writeFile(path string, f *file) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode %s: %w", path, err)
	}
	return writeAtomic(path, append(data, '\n'))
}

// writeAtomic replaces path through a temporary file readable by the user only, so a crash never
// leaves it half written.
func writeAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	// Once renamed the temporary file is gone; before that, a failure leaves it to remove.
	defer func() {
		if err == nil {
			return
		}
		if removeErr := os.Remove(tmp.Name()); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()

	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if err = errors.Join(writeErr, closeErr, os.Chmod(tmp.Name(), 0o600)); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
