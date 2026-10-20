package cli

import (
	"os"
	"strings"

	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/mcpserve"
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

// nextLaunchSettings are the keys only the next launch of airlock reads.
var nextLaunchSettings = map[string]bool{
	"mode": true, "language": true, "mcp.gateway.enabled": true, "mcp.gateway.port": true, "daemon.idleTimeout": true,
}

// optionalSettings may be left empty.
var optionalSettings = map[string]bool{
	"modules.sql.readOnlySecretTag": true, "modules.sql.defaultDbUser": true, "organizationConfig": true,
}

// settingSection is the section of key, one of ui.SettingSections: its first segment, the first two
// for a module (modules.sql), "general" at top level.
func settingSection(key string) string {
	parts := strings.SplitN(key, ".", 3)
	switch {
	case len(parts) == 1:
		return "general"
	case parts[0] == "modules" && len(parts) == 3:
		return parts[0] + "." + parts[1]
	}
	return parts[0]
}

// settingKind is how key is edited; a key not listed is edited as text.
func settingKind(key string) ui.SettingKind {
	switch key {
	case "aws.randomLocalPort", "mcp.gateway.enabled", "agent.writes", "modules.sql.enabled", "modules.k8s.enabled",
		"modules.cache.enabled", "modules.dynamodb.enabled", "modules.s3.enabled", "modules.logs.enabled":
		return ui.KindToggle
	case "mode", "language", "agent.autoConnect":
		return ui.KindChoice
	case "aws.defaultRegion":
		return ui.KindRegion
	case "agent.idleTimeout", "daemon.idleTimeout":
		return ui.KindDuration
	case "aws.jumphostTag", "modules.sql.readOnlySecretTag":
		return ui.KindTag
	case "mcp.gateway.port":
		return ui.KindPort
	case "mcp.clients":
		return ui.KindClients
	case "environments":
		return ui.KindEnvironments
	}
	return ui.KindText
}

// settingOptions are the values offered for key; nil for a key edited otherwise.
func settingOptions(key string) []ui.SettingOption {
	var values []string
	switch key {
	case "mode":
		values = []string{config.ModeCLI, config.ModeDaemon}
	case "language":
		values = []string{config.LanguageAuto, "en", "fr"}
	case "agent.autoConnect":
		values = []string{config.AutoConnectNone, config.AutoConnectNonProd, config.AutoConnectAll}
	case "agent.idleTimeout", "daemon.idleTimeout":
		values = []string{"15m", "30m", "1h", "4h"}
		options := make([]ui.SettingOption, 0, len(values))
		for _, value := range values {
			options = append(options, ui.SettingOption{Value: value, Label: value})
		}
		return options
	default:
		return nil
	}
	options := make([]ui.SettingOption, 0, len(values))
	for _, value := range values {
		prefix := "config.option." + key + "." + value
		option := ui.SettingOption{Value: value, Label: i18n.T(prefix)}
		if key != "language" {
			option.Help = i18n.T(prefix + ".help")
		}
		options = append(options, option)
	}
	return options
}

// configSettingsStore is the ui.SettingsStore of config.json at path.
type configSettingsStore struct {
	// overriddenBy maps a key to the flag of this run winning over the file, e.g. "--region".
	overriddenBy map[string]string
	org          *config.Organization
	path         string
	// projectDir is the working directory, where AI clients are detected; empty when unknown.
	projectDir string
	// managed: a managed file sets the organization source, organizationConfig is not the user's.
	managed bool
}

var _ ui.SettingsStore = (*configSettingsStore)(nil)

// newConfigSettingsStore returns the store of path over org; flags are those of this run.
func newConfigSettingsStore(path string, org *config.Organization, flags *pflag.FlagSet) *configSettingsStore {
	overriddenBy := map[string]string{}
	for key, name := range settingFlagNames {
		if flag := flags.Lookup(name); flag != nil && flag.Changed {
			overriddenBy[key] = "--" + name
		}
	}
	projectDir, err := os.Getwd()
	if err != nil {
		projectDir = "" // the detection is then unknown.
	}
	return &configSettingsStore{path: path, org: org, overriddenBy: overriddenBy, projectDir: projectDir}
}

// Snapshot lists every key of the file with its effective value, its source and how it is edited.
func (s *configSettingsStore) Snapshot() (ui.SettingsSnapshot, error) {
	settings, err := config.Settings(s.path, s.org)
	if err != nil {
		return ui.SettingsSnapshot{}, err
	}
	cfg, err := config.Load(s.path, s.org)
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
			FromOrganization: setting.Source == config.SourceOrganization, Locked: setting.Locked,
		}
		view.Section, view.Label = settingSection(setting.Name), i18n.T("config.label."+setting.Name)
		view.Kind, view.Optional = settingKind(setting.Name), optionalSettings[setting.Name]
		view.Options = settingOptions(setting.Name)
		// The mode, language, gateway and its port, and the daemon timeout are read at start only.
		view.NextLaunch = nextLaunchSettings[setting.Name]
		views = append(views, view)
	}
	snapshot := ui.SettingsSnapshot{Path: s.path, Settings: views, Environments: cfg.EnvironmentSet()}
	if s.projectDir != "" {
		snapshot.DetectedClients = mcpserve.DetectClients(s.projectDir)
	}
	if s.org != nil {
		snapshot.Organization = s.org.Source
	}
	return snapshot, nil
}

// Set validates value and writes it for key, keeping the other keys of the file.
func (s *configSettingsStore) Set(key, value string) error {
	if err := refuseManagedSource(key, s.managed); err != nil {
		return err
	}
	return config.Set(s.path, s.org, key, value)
}

// Validate reports why value is not valid for key, by the rule Set applies.
func (s *configSettingsStore) Validate(key, value string) error {
	if err := refuseManagedSource(key, s.managed); err != nil {
		return err
	}
	return config.Validate(key, value)
}

// Unset removes key from the file, so that its default applies again.
func (s *configSettingsStore) Unset(key string) error {
	if err := refuseManagedSource(key, s.managed); err != nil {
		return err
	}
	_, err := config.Unset(s.path, s.org, key)
	return err
}
