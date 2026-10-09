package cli

import (
	"github.com/spf13/cobra"

	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/i18n"
)

// resolveLanguage returns the language of config path when set, else the system's. A config that
// cannot be read is left to the command, which reports it as before.
func resolveLanguage(path string, getenv func(string) string, osLocale func() string) string {
	if cfg, err := config.Load(path); err == nil && cfg.Language != config.LanguageAuto {
		return cfg.Language
	}
	return i18n.Detect(getenv, osLocale)
}

// localizeCobra translates the text Cobra writes itself: the usage template, the -h flag, and the
// help and completion commands. English keeps Cobra's own text.
func localizeCobra(root *cobra.Command) {
	if i18n.Language() == "en" {
		return
	}
	root.SetUsageTemplate(i18n.T("cli.cobra.usage_template"))
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	for _, sub := range root.Commands() {
		switch sub.Name() {
		case "help":
			sub.Short, sub.Long = i18n.T("cli.cobra.help_short"), i18n.Format("cli.cobra.help_long", root.Name())
		case "completion":
			sub.Short = i18n.T("cli.cobra.completion_short")
			for _, shell := range sub.Commands() {
				shell.Short = i18n.Format("cli.cobra.completion_shell_short", shell.Name())
			}
		}
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		c.InitDefaultHelpFlag()
		c.Flags().Lookup("help").Usage = i18n.Format("cli.cobra.help_flag", c.DisplayName())
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}
