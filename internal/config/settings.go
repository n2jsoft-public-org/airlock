package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
)

// Key describes one setting of config.json.
type Key struct {
	Name string
	// Personal keys are the user's own choice: config strings never carry them.
	Personal bool
}

// Description explains the key in the user's language.
func (k Key) Description() string {
	return i18n.T("config.key." + k.Name)
}

// Keys lists the settings of config.json by their dotted path in the file, in the order the
// file is written in.
var Keys = []Key{
	{Name: "language", Personal: true},
	{Name: "mode"},
	{Name: "environments"},
	{Name: "aws.defaultRegion"},
	{Name: "aws.jumphostTag"},
	{Name: "aws.randomLocalPort"},
	{Name: "mcp.serverPrefix"},
	{Name: "mcp.clients"},
	{Name: "mcp.gateway.enabled"},
	{Name: "mcp.gateway.port"},
	{Name: "agent.autoConnect"},
	{Name: "agent.writes"},
	{Name: "agent.idleTimeout"},
	{Name: "daemon.idleTimeout"},
	{Name: "modules.sql.enabled"},
	{Name: "modules.sql.defaultDbUser"},
	{Name: "modules.sql.readOnlySecretTag"},
	{Name: "modules.k8s.enabled"},
	{Name: "modules.cache.enabled"},
	{Name: "modules.dynamodb.enabled"},
	{Name: "modules.s3.enabled"},
}

// renamedKeys maps each flat key of the earlier layout to the key replacing it.
var renamedKeys = map[string]string{
	"jumphostTag":       "aws.jumphostTag",
	"defaultRegion":     "aws.defaultRegion",
	"randomLocalPort":   "aws.randomLocalPort",
	"mcpServerPrefix":   "mcp.serverPrefix",
	"mcpClients":        "mcp.clients",
	"mcpGateway":        "mcp.gateway.enabled",
	"mcpGatewayPort":    "mcp.gateway.port",
	"agentAutoConnect":  "agent.autoConnect",
	"agentWrites":       "agent.writes",
	"agentIdleTimeout":  "agent.idleTimeout",
	"daemonIdleTimeout": "daemon.idleTimeout",
	"defaultDbUser":     "modules.sql.defaultDbUser",
	"readOnlySecretTag": "modules.sql.readOnlySecretTag",
}

// legacyDisabledModules is the flat key modules.<name>.enabled replaces.
const legacyDisabledModules = "disabledModules"

// Renamed returns the key replacing key, a flat key of the earlier layout; ok is false for any
// other key.
func Renamed(key string) (newKey string, ok bool) {
	newKey, ok = renamedKeys[key]
	return newKey, ok
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
var deprecatedProductionProfiles = Key{Name: legacyProductionProfiles}

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
		return nil, i18n.NewError("config.config.invalid_config", path, err)
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
	for _, legacy := range f.legacy {
		value := legacy.value
		if legacy.ignoredBy != "" {
			value += " (ignored: " + legacy.ignoredBy + " is set)"
		}
		settings = append(settings, Setting{Key: Key{Name: legacy.name}, Value: value, Source: SourceDeprecated})
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
		return i18n.NewError("config.settings.productionprofiles_is_replaced_by", strings.TrimSpace(value))
	}
	key = currentName(key)
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
		return i18n.NewError("config.config.invalid_config", path, err)
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
	return i18n.NewError("config.settings.keep_legacy_production", strings.Join(patterns, ","), set.WithProductionPatterns(patterns))
}

// Unset removes key from path, so that its default applies again; removed is false when the key
// was not in the file.
func Unset(path, key string) (removed bool, err error) {
	f, err := readFile(path)
	if err != nil {
		return false, err
	}
	key = currentName(key)
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
		return i18n.NewError("config.config.failed_to_read", path, err)
	}

	defaults := Defaults()
	values := defaults.values()
	var f file
	for _, key := range Keys {
		*f.field(key.Name) = new(values[key.Name])
	}
	return writeFile(path, &f)
}

// values renders every setting of c as config.json holds it.
func (c *Config) values() map[string]string {
	values := map[string]string{
		"language":                      c.Language,
		"mode":                          c.Mode,
		"environments":                  c.Environments,
		"aws.defaultRegion":             c.DefaultRegion,
		"aws.jumphostTag":               c.JumphostTag.String(),
		"aws.randomLocalPort":           strconv.FormatBool(c.RandomLocalPort),
		"mcp.serverPrefix":              c.MCPServerPrefix,
		"mcp.clients":                   c.MCPClients,
		"mcp.gateway.enabled":           strconv.FormatBool(c.MCPGateway),
		"mcp.gateway.port":              strconv.Itoa(c.MCPGatewayPort),
		"agent.autoConnect":             c.AgentAutoConnect,
		"agent.writes":                  strconv.FormatBool(c.AgentWrites),
		"agent.idleTimeout":             FormatDuration(c.AgentIdleTimeout),
		"daemon.idleTimeout":            FormatDuration(c.DaemonIdleTimeout),
		"modules.sql.defaultDbUser":     c.DefaultDBUser.String(),
		"modules.sql.readOnlySecretTag": c.ReadOnlySecretTag.String(),
	}
	for _, module := range Modules {
		values[moduleEnabledKey(module)] = strconv.FormatBool(!slices.Contains(c.DisabledModules, module))
	}
	return values
}

// field returns the field of f holding key, one of Keys or productionProfiles; nil for any other.
func (f *file) field(key string) **string {
	switch key {
	case "language":
		return &f.Language
	case "mode":
		return &f.Mode
	case "environments":
		return &f.Environments
	case "aws.defaultRegion":
		return &f.DefaultRegion
	case "aws.jumphostTag":
		return &f.JumphostTag
	case "aws.randomLocalPort":
		return &f.RandomLocalPort
	case "mcp.serverPrefix":
		return &f.MCPServerPrefix
	case "mcp.clients":
		return &f.MCPClients
	case "mcp.gateway.enabled":
		return &f.MCPGateway
	case "mcp.gateway.port":
		return &f.MCPGatewayPort
	case "agent.autoConnect":
		return &f.AgentAutoConnect
	case "agent.writes":
		return &f.AgentWrites
	case "agent.idleTimeout":
		return &f.AgentIdleTimeout
	case "daemon.idleTimeout":
		return &f.DaemonIdleTimeout
	case "modules.sql.enabled":
		return &f.SQLEnabled
	case "modules.sql.defaultDbUser":
		return &f.DefaultDBUser
	case "modules.sql.readOnlySecretTag":
		return &f.ReadOnlySecretTag
	case "modules.k8s.enabled":
		return &f.K8sEnabled
	case "modules.cache.enabled":
		return &f.CacheEnabled
	case "modules.dynamodb.enabled":
		return &f.DynamoDBEnabled
	case "modules.s3.enabled":
		return &f.S3Enabled
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
	if key == legacyDisabledModules {
		return nil, i18n.NewError("config.settings.disabledmodules_is_replaced_by", strings.Join(Modules, ", "))
	}
	names := make([]string, 0, len(Keys))
	for _, k := range Keys {
		names = append(names, k.Name)
	}
	return nil, i18n.NewError("config.settings.unknown_setting_valid", key, strings.Join(names, ", "))
}

// currentName returns the key replacing key when it is a flat key of the earlier layout, key
// otherwise.
func currentName(key string) string {
	if newKey, ok := renamedKeys[key]; ok {
		return newKey
	}
	return key
}

// writeFile stores f as indented JSON, keys absent from f left out.
func writeFile(path string, f *file) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return i18n.NewError("config.settings.failed_to_encode", path, err)
	}
	return writeAtomic(path, append(data, '\n'))
}

// writeAtomic replaces path through a temporary file readable by the user only, so a crash never
// leaves it half written.
func writeAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return i18n.NewError("config.settings.failed_to_create", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return i18n.NewError("config.settings.failed_to_write", path, err)
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
		return i18n.NewError("config.settings.failed_to_write", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return i18n.NewError("config.settings.failed_to_write", path, err)
	}
	return nil
}
