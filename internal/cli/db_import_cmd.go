package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/spf13/cobra"
)

func newDBImportCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "import <string>",
		Short: "Add or update direct databases from a string of `airlock db export`, after showing what it changes",
		Long:  "Adds the databases of the string and updates those of the same name; it never removes a database.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openDatabaseStore()
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
			printDatabasePlan(cmd, store.Path(), &plan)
			if !plan.HasChanges() {
				outf(cmd, "Nothing to change: %s already has these databases.\n", store.Path())
				return nil
			}
			if !yes && !confirm(cmd, "Apply these changes to "+store.Path()+"? [y/N] ") {
				return errors.New("not applied")
			}
			if err := directdb.ApplyImport(store, &plan); err != nil {
				return err
			}
			if err := forgetChangedConnections(cmd, store, &plan); err != nil {
				return err
			}
			outf(cmd, "Imported %s into %s\n", pluralDatabases(&plan), store.Path())
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Apply without asking")
	return cmd
}

// databaseChangeSigns mark each kind of change in the import summary.
var databaseChangeSigns = map[directdb.ChangeKind]string{
	directdb.ChangeAdded:     "+",
	directdb.ChangeUpdated:   "~",
	directdb.ChangeUnchanged: "=",
}

// printDatabasePlan lists what the import does to every database: the string itself is opaque, so
// this is where the importer reviews it.
func printDatabasePlan(cmd *cobra.Command, path string, plan *directdb.ImportPlan) {
	outf(cmd, "Import into %s:\n\n", path)
	for idx := range plan.Changes {
		change := &plan.Changes[idx]
		line := fmt.Sprintf("  %s %s (%s)", databaseChangeSigns[change.Kind], change.Database.Name, change.Kind)
		if change.Kind == directdb.ChangeUpdated {
			fields := make([]string, 0, len(change.Fields))
			for _, field := range change.Fields {
				fields = append(fields, fmt.Sprintf("%s %s -> %s", field.Field, field.Old, field.New))
			}
			line += ": " + strings.Join(fields, ", ")
		}
		if change.ConnectionChanged() {
			line += " (remembered password will be forgotten)"
		}
		outf(cmd, "%s\n", line)
	}
	outf(cmd, "\n")
}

// forgetChangedConnections forgets the prefs and keychain password of every database the import
// reaches differently: a password must never follow a string to another host, port or user.
func forgetChangedConnections(cmd *cobra.Command, store *directdb.Store, plan *directdb.ImportPlan) error {
	for idx := range plan.Changes {
		change := &plan.Changes[idx]
		if !change.ConnectionChanged() {
			continue
		}
		if err := forgetDirectConnection(cmd.Context(), cmd, store, change.Database.Name); err != nil {
			return fmt.Errorf("imported %s, but the connection remembered for %s was not forgotten: %w",
				pluralDatabases(plan), change.Database.Name, err)
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
