package cli

import (
	"github.com/spf13/cobra"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

func newConnectionAddCmd() *cobra.Command {
	var flags dbFlags
	cmd := &cobra.Command{
		Use:     "add <name>",
		Short:   i18n.T("cli.connection_add.short"),
		Example: "  airlock connection add legacy-billing --engine sqlserver --host billing.db.internal --user mcp_ro --environment prod",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openDatabases(cmd)
			if err != nil {
				return err
			}
			db := flags.definition(args[0])
			if err := validDatabase(cmd, &db, true); err != nil {
				return err
			}
			if err := store.Put("", db); err != nil {
				return err
			}
			outf(cmd, i18n.T("cli.connection_add.added"), db.Name, db.Engine, db.Address(), store.Path())
			return nil
		},
	}
	flags.register(cmd)
	for _, name := range []string{"engine", "host", "user"} {
		cobra.CheckErr(cmd.MarkFlagRequired(name))
	}
	return cmd
}
