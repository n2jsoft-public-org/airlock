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

// Keys lists the settings of config.json, in the order the file is written in.
var Keys = []Key{
	{Name: "jumphostTag"},
	{Name: "defaultDbUser"},
	{Name: "readOnlySecretTag"},
	{Name: "mcpServerPrefix"},
	{Name: "defaultRegion"},
	{Name: "environments"},
	{Name: "mcpClients"},
	{Name: "mode"},
	{Name: "daemonIdleTimeout"},
	{Name: "mcpGateway"},
	{Name: "mcpGatewayPort"},
	{Name: "agentAutoConnect"},
	{Name: "agentWrites"},
	{Name: "agentIdleTimeout"},
	{Name: "randomLocalPort"},
	{Name: "disabledModules"},
	{Name: "language", Personal: true},
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
		"daemonIdleTimeout": FormatDuration(c.DaemonIdleTimeout),
		"mcpGateway":        strconv.FormatBool(c.MCPGateway),
		"mcpGatewayPort":    strconv.Itoa(c.MCPGatewayPort),
		"agentAutoConnect":  c.AgentAutoConnect,
		"agentWrites":       strconv.FormatBool(c.AgentWrites),
		"agentIdleTimeout":  FormatDuration(c.AgentIdleTimeout),
		"randomLocalPort":   strconv.FormatBool(c.RandomLocalPort),
		"disabledModules":   strings.Join(c.DisabledModules, ","),
		"language":          c.Language,
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
	case "daemonIdleTimeout":
		return &f.DaemonIdleTimeout
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
	case "disabledModules":
		return &f.DisabledModules
	case "language":
		return &f.Language
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
	return nil, i18n.NewError("config.settings.unknown_setting_valid", key, strings.Join(names, ", "))
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
