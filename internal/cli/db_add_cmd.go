package cli

import (
	"github.com/spf13/cobra"
)

func newDBAddCmd() *cobra.Command {
	var flags dbFlags
	cmd := &cobra.Command{
		Use:     "add <name>",
		Short:   "Add a direct database to databases.json",
		Example: "  airlock db add legacy-billing --engine sqlserver --host billing.db.internal --user mcp_ro --production",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openDatabaseStore()
			if err != nil {
				return err
			}
			db := flags.definition(args[0])
			if err := validDatabase(&db); err != nil {
				return err
			}
			if err := store.Put("", db); err != nil {
				return err
			}
			outf(cmd, "Added %s (%s, %s) to %s\n", db.Name, db.Engine, db.Address(), store.Path())
			return nil
		},
	}
	flags.register(cmd)
	for _, name := range []string{"engine", "host", "user"} {
		cobra.CheckErr(cmd.MarkFlagRequired(name))
	}
	return cmd
}
