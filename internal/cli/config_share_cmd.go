package cli

import (
	"bufio"
	"errors"
	"strconv"
	"strings"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/cobra"
)

func newConfigExportCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Print the settings of config.json as one string to share",
		Long: "Prints the keys set in config.json as one string, for teammates to import with " +
			"`airlock config import <string>`, which shows every change before applying it. A terminal first " +
			"lists every key set, all selected, to uncheck those to leave out; --all, or no terminal, exports them all.",
		Args: cobra.NoArgs,
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
	cmd.Flags().BoolVar(&all, "all", false, "Export every key set without asking")
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
	indexes, asked, err := pickExports(cmd, "Export config.json settings", choices)
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
		Short: "Apply a string of `airlock config export`, after showing what it changes",
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
				outf(cmd, "Nothing to change: %s already has these values.\n", path)
				return nil
			}
			if !yes && !confirm(cmd, "Apply these changes to "+path+"? [y/N] ") {
				return errors.New("not applied")
			}
			if err := config.ApplyImport(&plan); err != nil {
				return err
			}
			outf(cmd, "Applied %s to %s\n", pluralChanges(&plan), path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&replace, "replace", false, "Make config.json exactly the imported settings, removing the others")
	cmd.Flags().BoolVar(&yes, "yes", false, "Apply without asking")
	return cmd
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
	outf(cmd, "Import into %s:\n\n", plan.Path)
	for _, c := range plan.Changes {
		value := shownValue(c.Old, c.OldDefault) + " → " + shownValue(c.New, c.NewDefault)
		if c.Kind == config.ChangeUnchanged {
			value = shownValue(c.New, c.NewDefault) + " (unchanged)"
		}
		outf(cmd, "  %s %-*s   %s\n", changeSigns[c.Kind], width, c.Name, value)
		if c.Kind != config.ChangeUnchanged {
			outf(cmd, "      %s\n", c.Description)
		}
	}
	outf(cmd, "\n")
}

// shownValue renders a value of the import summary, telling airlock's defaults apart.
func shownValue(value string, isDefault bool) string {
	switch {
	case value == "" && isDefault:
		return "(none, default)"
	case value == "":
		return "(none)"
	case isDefault:
		return value + " (default)"
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
	if count == 1 {
		return "1 change"
	}
	return strconv.Itoa(count) + " changes"
}
