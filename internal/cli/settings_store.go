package cli

import (
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/pflag"
)

// settingFlagNames maps each key of config.json to the flag of the root command overriding it.
var settingFlagNames = map[string]string{
	"aws.jumphostTag":               "jumphost-tag",
	"modules.sql.defaultDbUser":     "db-user",
	"modules.sql.readOnlySecretTag": "read-only-secret-tag",
	"mcp.serverPrefix":              "mcp-server-prefix",
	"aws.defaultRegion":             "region",
	"mcp.clients":                   "mcp-clients",
	"mode":                          "mode",
}

// configSettingsStore is the ui.SettingsStore of config.json at path.
type configSettingsStore struct {
	// overriddenBy maps a key to the flag of this run winning over the file, e.g. "--region".
	overriddenBy map[string]string
	path         string
}

var _ ui.SettingsStore = (*configSettingsStore)(nil)

// newConfigSettingsStore returns the store of path; flags are those of this run.
func newConfigSettingsStore(path string, flags *pflag.FlagSet) *configSettingsStore {
	overriddenBy := map[string]string{}
	for key, name := range settingFlagNames {
		if flag := flags.Lookup(name); flag != nil && flag.Changed {
			overriddenBy[key] = "--" + name
		}
	}
	return &configSettingsStore{path: path, overriddenBy: overriddenBy}
}

// Snapshot lists every key of the file with its effective value, its source and how it is edited.
func (s *configSettingsStore) Snapshot() (ui.SettingsSnapshot, error) {
	settings, err := config.Settings(s.path)
	if err != nil {
		return ui.SettingsSnapshot{}, err
	}
	cfg, err := config.Load(s.path)
	if err != nil {
		return ui.SettingsSnapshot{}, err
	}
	views := make([]ui.SettingView, 0, len(settings))
	for _, setting := range settings {
		if setting.Source == config.SourceDeprecated {
			continue
		}
		view := ui.SettingView{
			Name: setting.Name, Description: setting.Description(), Value: setting.Value,
			FromFile: setting.Source == config.SourceFile, OverriddenBy: s.overriddenBy[setting.Name],
		}
		switch setting.Name {
		case "mode":
			// The mode decides how airlock starts: only the next launch reads it.
			view.Choices, view.NextLaunch = []string{config.ModeCLI, config.ModeDaemon}, true
		case "language":
			// The language is read once at start.
			view.Choices, view.NextLaunch = []string{config.LanguageAuto, "en", "fr"}, true
		case "aws.defaultRegion":
			view.Region = true
		case "mcp.gateway.enabled":
			// The gateway starts with the daemon: only its next start reads it.
			view.Choices, view.NextLaunch = []string{"true", "false"}, true
		case "mcp.gateway.port", "daemon.idleTimeout":
			view.NextLaunch = true
		case "agent.autoConnect":
			view.Choices = []string{config.AutoConnectNone, config.AutoConnectNonProd, config.AutoConnectAll}
		case "agent.writes", "aws.randomLocalPort", "modules.sql.enabled", "modules.k8s.enabled",
			"modules.cache.enabled", "modules.dynamodb.enabled", "modules.s3.enabled":
			view.Choices = []string{"false", "true"}
		}
		views = append(views, view)
	}
	return ui.SettingsSnapshot{Path: s.path, Settings: views, Environments: cfg.EnvironmentSet()}, nil
}

// Set validates value and writes it for key, keeping the other keys of the file.
func (s *configSettingsStore) Set(key, value string) error {
	return config.Set(s.path, key, value)
}

// Unset removes key from the file, so that its default applies again.
func (s *configSettingsStore) Unset(key string) error {
	_, err := config.Unset(s.path, key)
	return err
}
