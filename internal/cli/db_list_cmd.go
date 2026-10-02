package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newDBListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the direct databases of databases.json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := openDatabaseStore()
			if err != nil {
				return err
			}
			dbs, err := store.List()
			if err != nil {
				return err
			}
			if len(dbs) == 0 {
				outf(cmd, "No direct database: add one with `airlock db add <name> --engine postgres --host <host> --user <user>`\n")
				return nil
			}
			table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
			if _, err = fmt.Fprintln(table, "NAME\tENGINE\tADDRESS\tDATABASE\tUSER\tTLS\tPRODUCTION"); err != nil {
				return err
			}
			for idx := range dbs {
				db := &dbs[idx]
				database := db.Database
				if database == "" {
					database = "(default)"
				}
				production := ""
				if db.Production {
					production = "yes"
				}
				if _, err = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					db.Name, db.Engine, db.Address(), database, db.User, db.EffectiveTLS(), production); err != nil {
					return err
				}
			}
			return table.Flush()
		},
	}
}
