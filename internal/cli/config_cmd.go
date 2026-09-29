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
	"github.com/spf13/cobra"
)

// newConfigCmd groups the commands showing and changing config.json.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or change airlock's settings (config.json)",
		Long: "airlock's settings are the conventions of the AWS accounts it connects to, such as the jumphost tag. " +
			"They live in config.json; flags of the connect command win over them.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newConfigPathCmd(), newConfigShowCmd(), newConfigInitCmd(), newConfigSetCmd(), newConfigUnsetCmd())
	return cmd
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the path of config.json",
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
		Short: "Show every setting, its value and where it comes from",
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
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the effective values as JSON")
	return cmd
}

func printSettings(cmd *cobra.Command, path string, settings []config.Setting) error {
	header := "Config file: " + path
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		header += " (not created: run `airlock config init`)"
	}
	outf(cmd, "%s\n", header)
	outf(cmd, "\n")

	table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
	for _, s := range settings {
		value := s.Value
		if value == "" {
			value = "(none)"
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\n", s.Name, value, s.Source); err != nil {
			return err
		}
	}
	return table.Flush()
}

func printSettingsJSON(cmd *cobra.Command, settings []config.Setting) error {
	values := make(map[string]string, len(settings))
	for _, s := range settings {
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
		Short: "Write config.json with every setting and its default value",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := appdir.ConfigFile()
			if err != nil {
				return err
			}
			if err := config.WriteTemplate(path, force); err != nil {
				if errors.Is(err, config.ErrFileExists) {
					return fmt.Errorf("%s already exists: use --force to overwrite", path)
				}
				return err
			}
			if force {
				outf(cmd, "Wrote %s (the previous file, if any, is kept as %s.bak)\n", path, path)
			} else {
				outf(cmd, "Wrote %s\n", path)
			}
			return printKeyHelp(cmd)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing config.json, keeping a .bak copy")
	return cmd
}

// printKeyHelp explains every setting, since JSON files carry no comments.
func printKeyHelp(cmd *cobra.Command) error {
	outf(cmd, "\n")
	table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
	for _, key := range config.Keys {
		if _, err := fmt.Fprintf(table, "  %s\t%s\n", key.Name, key.Description); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	outf(cmd, "\n")
	outf(cmd, "Change a value with `airlock config set <key> <value>`; check them with `airlock config show`.\n")
	return nil
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a setting in config.json, validated first",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := appdir.ConfigFile()
			if err != nil {
				return err
			}
			if err := config.Set(path, args[0], args[1]); err != nil {
				return err
			}
			outf(cmd, "%s = %s in %s\n", args[0], strings.TrimSpace(args[1]), path)
			return nil
		},
	}
}

func newConfigUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove a setting from config.json, so that its default applies",
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
				outf(cmd, "Removed %s from %s: the default applies\n", args[0], path)
			} else {
				outf(cmd, "%s was not set in %s\n", args[0], path)
			}
			return nil
		},
	}
}

// outf prints to the command's standard output, where scripts read results; cobra's own Print
// helpers write to standard error.
func outf(cmd *cobra.Command, format string, args ...any) {
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), format, args...); err != nil {
		cmd.PrintErrf("failed to write the output: %v\n", err)
	}
}
