package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/cobra"
)

func newConnectionImportCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "import <string>",
		Short: "Add or update direct databases from a string of `airlock connection export`, after showing what it changes",
		Long: "Adds the databases of the string and updates those of the same name; it never removes a database. " +
			"In a terminal, every change is then listed, all selected, to uncheck those to leave out; without a " +
			"terminal it asks to apply them all.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openDatabases(cmd)
			if err != nil {
				return err
			}
			current, err := store.List()
			if err != nil {
				return err
			}
			plan, err := directdb.PlanImport(current, args[0])
			if err != nil {
				return err
			}
			replaced, err := invalidEntriesReplaced(store, &plan)
			if err != nil {
				return err
			}
			cfg, _, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			printDatabasePlan(cmd, store.Path(), &plan, replaced, cfg.EnvironmentSet())
			if !plan.HasChanges() {
				outf(cmd, "Nothing to change: %s already has these databases.\n", store.Path())
				return nil
			}
			if !yes {
				kept, asked, err := pickDatabaseImports(cmd, &plan, replaced, cfg.EnvironmentSet())
				switch {
				case errors.Is(err, ui.ErrExportCanceled):
					return errors.New("not applied")
				case err != nil:
					return err
				case asked:
					plan.Changes = kept
				case !confirm(cmd, "Apply these changes to "+store.Path()+"? [y/N] "):
					return errors.New("not applied")
				}
			}
			canonicalizeProduction(&plan, cfg.EnvironmentSet())
			if err := forgetChangedConnections(cmd, store, &plan, replaced); err != nil {
				return err
			}
			if err := directdb.ApplyImport(store, &plan); err != nil {
				return err
			}
			outf(cmd, "Imported %s into %s\n", pluralDatabases(&plan), store.Path())
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Apply without asking")
	return cmd
}

// canonicalizeProduction renames the production environment of every imported database to its
// alias: it is always saved as prod, whatever the sender's setting called it.
func canonicalizeProduction(plan *directdb.ImportPlan, envs environment.Set) {
	for idx := range plan.Changes {
		db := &plan.Changes[idx].Database
		if envs.Lookup(db.Environment).Production {
			db.Environment = environment.ProductionAlias
		}
	}
}

// databaseChangeSigns mark each kind of change in the import summary.
var databaseChangeSigns = map[directdb.ChangeKind]string{
	directdb.ChangeAdded:     "+",
	directdb.ChangeUpdated:   "~",
	directdb.ChangeUnchanged: "=",
}

// invalidEntriesReplaced are the invalid entries, by name, that the databases plan adds replace:
// the plan only knows the valid definitions, so a name held by an invalid entry reads as added.
func invalidEntriesReplaced(store *connections.Databases, plan *directdb.ImportPlan) (map[string]connections.Entry, error) {
	entries, err := store.Store().List()
	if err != nil {
		return nil, err
	}
	invalid := map[string]connections.Entry{}
	for idx := range entries {
		if entries[idx].Invalid != nil {
			invalid[entries[idx].Key] = entries[idx]
		}
	}
	replaced := map[string]connections.Entry{}
	for idx := range plan.Changes {
		name := plan.Changes[idx].Database.Name
		if entry, found := invalid[name]; found && plan.Changes[idx].Kind == directdb.ChangeAdded {
			replaced[name] = entry
		}
	}
	return replaced, nil
}

// printDatabasePlan lists what the import does to every database: the string itself is opaque, so
// this is where the importer reviews it. A database replacing an invalid entry of replaced is told
// apart from an added one.
func printDatabasePlan(cmd *cobra.Command, path string, plan *directdb.ImportPlan, replaced map[string]connections.Entry, envs environment.Set) {
	outf(cmd, "Import into %s:\n\n", path)
	for idx := range plan.Changes {
		change := &plan.Changes[idx]
		sign, detail := describeDatabaseChange(change, replaced)
		outf(cmd, "  %s %s %s\n", sign, change.Database.Name, detail)
		if env := envs.Lookup(change.Database.Environment); !env.IsZero() && !env.Configured {
			outf(cmd, "      environment %s is not in your environments setting: shown gray, not production\n", env.Name)
		}
	}
	outf(cmd, "\n")
}

// describeDatabaseChange tells what the import does to change: its sign and, to follow its name,
// how. A database replacing an invalid entry of replaced is told apart from an added one.
func describeDatabaseChange(change *directdb.ImportChange, replaced map[string]connections.Entry) (sign, detail string) {
	sign, detail = databaseChangeSigns[change.Kind], "("+string(change.Kind)+")"
	if change.Kind == directdb.ChangeUpdated {
		fields := make([]string, 0, len(change.Fields))
		for _, field := range change.Fields {
			fields = append(fields, fmt.Sprintf("%s %s -> %s", field.Field, field.Old, field.New))
		}
		detail += ": " + strings.Join(fields, ", ")
	}
	forgets := change.ConnectionChanged()
	if entry, found := replaced[change.Database.Name]; found {
		sign, detail = databaseChangeSigns[directdb.ChangeUpdated], "(replaces an invalid entry)"
		user, _ := entry.DirectUser()
		forgets = user != ""
	}
	if forgets {
		detail += " (remembered password will be forgotten)"
	}
	return sign, detail
}

// pickDatabaseImports lets the user uncheck the databases of plan to leave out, and returns the
// changes kept; asked is false without a terminal.
func pickDatabaseImports(cmd *cobra.Command, plan *directdb.ImportPlan, replaced map[string]connections.Entry, envs environment.Set) (kept []directdb.ImportChange, asked bool, err error) {
	var changed []directdb.ImportChange
	for idx := range plan.Changes {
		if plan.Changes[idx].Kind != directdb.ChangeUnchanged {
			changed = append(changed, plan.Changes[idx])
		}
	}
	choices := make([]ui.ExportChoice, len(changed))
	for idx := range changed {
		sign, detail := describeDatabaseChange(&changed[idx], replaced)
		db := &changed[idx].Database
		choices[idx] = ui.ExportChoice{Name: db.Name, Detail: sign + " " + detail, Environment: envs.Lookup(db.Environment)}
	}
	indexes, asked, err := pickExports(cmd, "Import direct databases", "import", choices)
	if err != nil || !asked {
		return nil, asked, err
	}
	kept = make([]directdb.ImportChange, len(indexes))
	for pos, idx := range indexes {
		kept[pos] = changed[idx]
	}
	return kept, true, nil
}

// forgetChangedConnections forgets the prefs and keychain password of every database the import
// reaches differently, and the keychain password of every invalid entry of replaced: a password
// must never follow a string to another host, port or user.
func forgetChangedConnections(cmd *cobra.Command, store *connections.Databases, plan *directdb.ImportPlan, replaced map[string]connections.Entry) error {
	for idx := range plan.Changes {
		change := &plan.Changes[idx]
		name := change.Database.Name
		var err error
		if entry, found := replaced[name]; found {
			err = forgetInvalidDirectPassword(cmd, store, &entry)
		} else if change.ConnectionChanged() {
			err = forgetDirectConnection(cmd.Context(), cmd, store, name)
		}
		if err != nil {
			return fmt.Errorf("nothing imported: the connection remembered for %s was not forgotten: %w", name, err)
		}
	}
	return nil
}

func pluralDatabases(plan *directdb.ImportPlan) string {
	count := 0
	for idx := range plan.Changes {
		if plan.Changes[idx].Kind != directdb.ChangeUnchanged {
			count++
		}
	}
	if count == 1 {
		return "1 database"
	}
	return strconv.Itoa(count) + " databases"
}
