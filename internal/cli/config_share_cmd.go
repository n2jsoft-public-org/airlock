package cli

import (
	"bufio"
	"errors"
	"strings"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/cobra"
)

func newConfigExportCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "export",
		Short: i18n.T("cli.config_export.short"),
		Long:  i18n.T("cli.config_export.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := appdir.ConfigFile()
			if err != nil {
				return err
			}
			var keys []string
			if !all {
				if keys, err = pickSettings(cmd, path); err != nil {
					return err
				}
			}
			shared, err := config.Export(path, keys...)
			if err != nil {
				return err
			}
			outf(cmd, "%s\n", shared)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, i18n.T("cli.config_export.flag.all"))
	return cmd
}

// pickSettings lets the user uncheck the keys of path to leave out; nil, every key, without a
// terminal or when path sets none, which config.Export then reports.
func pickSettings(cmd *cobra.Command, path string) ([]string, error) {
	settings, err := config.Settings(path)
	if err != nil {
		return nil, err
	}
	var set []config.Setting
	for idx := range settings {
		if settings[idx].Source == config.SourceFile {
			set = append(set, settings[idx])
		}
	}
	choices := make([]ui.ExportChoice, len(set))
	for idx := range set {
		choices[idx] = ui.ExportChoice{Name: set[idx].Name, Detail: set[idx].Value}
	}
	indexes, asked, err := pickExports(cmd, i18n.T("cli.config_share.export_config_json_settings"), i18n.T("ui.hint.export"), choices)
	if err != nil || !asked {
		return nil, err
	}
	keys := make([]string, len(indexes))
	for pos, idx := range indexes {
		keys[pos] = set[idx].Name
	}
	return keys, nil
}

func newConfigImportCmd() *cobra.Command {
	var replace, yes bool
	cmd := &cobra.Command{
		Use:   "import <string>",
		Short: i18n.T("cli.config_import.short"),
		Long:  i18n.T("cli.config_import.long"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := appdir.ConfigFile()
			if err != nil {
				return err
			}
			plan, err := config.PlanImport(path, args[0], replace)
			if err != nil {
				return err
			}
			printPlan(cmd, &plan)
			if !plan.HasChanges() {
				outf(cmd, i18n.T("cli.config_share.nothing_to_change_already"), path)
				return nil
			}
			if !yes {
				keys, asked, err := pickImports(cmd, &plan)
				switch {
				case errors.Is(err, ui.ErrExportCanceled):
					return errors.New(i18n.T("cli.config_share.not_applied"))
				case err != nil:
					return err
				case asked:
					if plan, err = config.PlanImport(path, args[0], replace, keys...); err != nil {
						return err
					}
				case !confirm(cmd, i18n.Format("cli.apply_changes_confirm", path)):
					return errors.New(i18n.T("cli.config_share.not_applied"))
				}
			}
			if err := config.ApplyImport(&plan); err != nil {
				return err
			}
			outf(cmd, i18n.T("cli.config_share.applied"), pluralChanges(&plan), path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&replace, "replace", false, i18n.T("cli.config_import.flag.replace"))
	cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("cli.config_import.flag.yes"))
	return cmd
}

// pickImports lets the user uncheck the changes of plan to leave out, and returns the keys kept;
// asked is false without a terminal.
func pickImports(cmd *cobra.Command, plan *config.Plan) (keys []string, asked bool, err error) {
	var changed []config.Change
	for _, c := range plan.Changes {
		if c.Kind != config.ChangeUnchanged {
			changed = append(changed, c)
		}
	}
	choices := make([]ui.ExportChoice, len(changed))
	for idx, c := range changed {
		choices[idx] = ui.ExportChoice{Name: c.Name, Detail: changeSigns[c.Kind] + " " + shownValue(c.Old, c.OldDefault) + " → " + shownValue(c.New, c.NewDefault)}
	}
	indexes, asked, err := pickExports(cmd, i18n.Format("cli.config_share.import_into_title", plan.Path), i18n.T("ui.hint.import"), choices)
	if err != nil || !asked {
		return nil, asked, err
	}
	keys = make([]string, len(indexes))
	for pos, idx := range indexes {
		keys[pos] = changed[idx].Name
	}
	return keys, true, nil
}

// changeSigns mark each kind of change in the import summary.
var changeSigns = map[config.ChangeKind]string{
	config.ChangeAdded:     "+",
	config.ChangeUpdated:   "~",
	config.ChangeRemoved:   "-",
	config.ChangeUnchanged: "=",
}

// printPlan lists what the import does to every key, explaining the keys it changes: the string
// itself is opaque, so this is where the importer reviews it.
func printPlan(cmd *cobra.Command, plan *config.Plan) {
	width := 0
	for _, c := range plan.Changes {
		width = max(width, len(c.Name))
	}
	outf(cmd, i18n.T("cli.config_share.import_into"), plan.Path)
	for _, c := range plan.Changes {
		value := shownValue(c.Old, c.OldDefault) + " → " + shownValue(c.New, c.NewDefault)
		if c.Kind == config.ChangeUnchanged {
			value = shownValue(c.New, c.NewDefault) + " (unchanged)"
		}
		outf(cmd, "  %s %-*s   %s\n", changeSigns[c.Kind], width, c.Name, value)
		if c.Kind != config.ChangeUnchanged {
			outf(cmd, "      %s\n", c.Description())
		}
	}
	outf(cmd, "\n")
}

// shownValue renders a value of the import summary, telling airlock's defaults apart.
func shownValue(value string, isDefault bool) string {
	switch {
	case value == "" && isDefault:
		return i18n.T("cli.config_share.none_default")
	case value == "":
		return i18n.T("cli.config_share.none")
	case isDefault:
		return i18n.Format("cli.config_share.value_default", value)
	default:
		return value
	}
}

// confirm asks question and reports whether the answer is yes; no answer is a no.
func confirm(cmd *cobra.Command, question string) bool {
	outf(cmd, "%s", question)
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && answer == "" {
		outf(cmd, "\n")
		return false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func pluralChanges(plan *config.Plan) string {
	count := 0
	for _, c := range plan.Changes {
		if c.Kind != config.ChangeUnchanged {
			count++
		}
	}
	return i18n.Plural(count, "cli.count.change")
}
