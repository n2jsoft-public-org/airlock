package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/spf13/cobra"
)

// newConfigCmd groups the commands showing and changing config.json.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: i18n.T("cli.config.short"),
		Long:  i18n.T("cli.config.long"),
		// Runnable, so that cobra checks the arguments: an unknown subcommand is an error, not the help.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newConfigPathCmd(), newConfigShowCmd(), newConfigInitCmd(), newConfigSetCmd(), newConfigUnsetCmd(),
		newConfigExportCmd(), newConfigImportCmd())
	return cmd
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: i18n.T("cli.config_path.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := appdir.ConfigFile()
			if err != nil {
				return err
			}
			outf(cmd, "%s\n", path)
			return nil
		},
	}
}

func newConfigShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: i18n.T("cli.config_show.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := appdir.ConfigFile()
			if err != nil {
				return err
			}
			settings, err := config.Settings(path)
			if err != nil {
				return err
			}
			if asJSON {
				return printSettingsJSON(cmd, settings)
			}
			return printSettings(cmd, path, settings)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, i18n.T("cli.config_show.flag.json"))
	return cmd
}

func printSettings(cmd *cobra.Command, path string, settings []config.Setting) error {
	header := i18n.Format("cli.config.file_header", path)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		header = i18n.Format("cli.config.file_header_missing", path)
	}
	outf(cmd, "%s\n", header)
	outf(cmd, "\n")

	table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
	for _, s := range settings {
		value := s.Value
		if value == "" {
			value = i18n.T("cli.config_share.none")
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\n", s.Name, value, i18n.T("cli.config.source."+string(s.Source))); err != nil {
			return err
		}
	}
	return table.Flush()
}

func printSettingsJSON(cmd *cobra.Command, settings []config.Setting) error {
	values := make(map[string]string, len(settings))
	for _, s := range settings {
		if s.Source == config.SourceDeprecated {
			continue
		}
		values[s.Name] = s.Value
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	outf(cmd, "%s\n", string(data))
	return nil
}

func newConfigInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: i18n.T("cli.config_init.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := appdir.ConfigFile()
			if err != nil {
				return err
			}
			if err := config.WriteTemplate(path, force); err != nil {
				if errors.Is(err, config.ErrFileExists) {
					return i18n.NewError("cli.config.already_exists_use_force", path)
				}
				return err
			}
			if force {
				outf(cmd, i18n.T("cli.config.wrote_previous_file_if"), path, path)
			} else {
				outf(cmd, i18n.T("cli.config.wrote"), path)
			}
			return printKeyHelp(cmd)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, i18n.T("cli.config_init.flag.force"))
	return cmd
}

// printKeyHelp explains every setting, since JSON files carry no comments.
func printKeyHelp(cmd *cobra.Command) error {
	outf(cmd, "\n")
	table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
	for _, key := range config.Keys {
		if _, err := fmt.Fprintf(table, "  %s\t%s\n", key.Name, key.Description()); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	outf(cmd, "\n")
	outf(cmd, "%s", i18n.T("cli.config.change_value_with_airlock"))
	return nil
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: i18n.T("cli.config_set.short"),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := appdir.ConfigFile()
			if err != nil {
				return err
			}
			if err := config.Set(path, args[0], args[1]); err != nil {
				return err
			}
			outf(cmd, i18n.T("cli.config.set_value"), args[0], strings.TrimSpace(args[1]), path)
			return nil
		},
	}
}

func newConfigUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: i18n.T("cli.config_unset.short"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := appdir.ConfigFile()
			if err != nil {
				return err
			}
			removed, err := config.Unset(path, args[0])
			if err != nil {
				return err
			}
			if removed {
				outf(cmd, i18n.T("cli.config.removed_from_default_applies"), args[0], path)
			} else {
				outf(cmd, i18n.T("cli.config.was_not_set_in"), args[0], path)
			}
			return nil
		},
	}
}

// outf prints to the command's standard output, where scripts read results; cobra's own Print
// helpers write to standard error.
func outf(cmd *cobra.Command, format string, args ...any) {
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), format, i18n.LocalizeArgs(args)...); err != nil {
		cmd.PrintErr(i18n.Format("cli.config.failed_to_write_output", err))
	}
}
