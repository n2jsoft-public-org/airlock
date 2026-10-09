package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/cobra"
)

func newConnectionImportCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "import <string>",
		Short: i18n.T("cli.connection_import.short"),
		Long:  i18n.T("cli.connection_import.long"),
		Args:  cobra.ExactArgs(1),
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
				outf(cmd, i18n.T("cli.connection_import.nothing_to_change_already"), store.Path())
				return nil
			}
			if !yes {
				kept, asked, err := pickDatabaseImports(cmd, &plan, replaced, cfg.EnvironmentSet())
				switch {
				case errors.Is(err, ui.ErrExportCanceled):
					return errors.New(i18n.T("cli.connection_import.not_applied"))
				case err != nil:
					return err
				case asked:
					plan.Changes = kept
				case !confirm(cmd, i18n.Format("cli.apply_changes_confirm", store.Path())):
					return errors.New(i18n.T("cli.connection_import.not_applied"))
				}
			}
			canonicalizeProduction(&plan, cfg.EnvironmentSet())
			if err := forgetChangedConnections(cmd, store, &plan, replaced); err != nil {
				return err
			}
			if err := directdb.ApplyImport(store, &plan); err != nil {
				return err
			}
			outf(cmd, i18n.T("cli.connection_import.imported"), pluralDatabases(&plan), store.Path())
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("cli.connection_import.flag.yes"))
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
	outf(cmd, i18n.T("cli.connection_import.import_into"), path)
	for idx := range plan.Changes {
		change := &plan.Changes[idx]
		sign, detail := describeDatabaseChange(change, replaced)
		outf(cmd, "  %s %s %s\n", sign, change.Database.Name, detail)
		if env := envs.Lookup(change.Database.Environment); !env.IsZero() && !env.Configured {
			outf(cmd, i18n.T("cli.connection_import.environment_is_not_in"), env.Name)
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
		sign, detail = databaseChangeSigns[directdb.ChangeUpdated], i18n.T("cli.connection_import.replaces_invalid")
		user, _ := entry.DirectUser()
		forgets = user != ""
	}
	if forgets {
		detail += i18n.T("cli.connection_import.password_forgotten")
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
	indexes, asked, err := pickExports(cmd, i18n.T("cli.connection_import.import_direct_databases"), i18n.T("ui.hint.import"), choices)
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
			return i18n.NewError("cli.connection_import.nothing_imported_connection_remembered", name, err)
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
	return i18n.Plural(count, "cli.count.database")
}
