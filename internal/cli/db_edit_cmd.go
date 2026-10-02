package cli

import (
	"errors"
	"fmt"

	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/spf13/cobra"
)

func newDBEditCmd() *cobra.Command {
	var flags dbFlags
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Change a direct database: only the given flags change",
		Long: "Changes only the flags given. --port 0 and --database \"\" restore their defaults, --production=false clears the flag, " +
			"--name renames the database; any change but --production (name, engine, host, port, database, user, TLS) " +
			"forgets the password remembered before.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeDirectDatabase,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openDatabaseStore()
			if err != nil {
				return err
			}
			previous, err := getDatabase(store, args[0])
			if err != nil {
				return err
			}
			db := previous
			if !flags.applyTo(cmd, &db) {
				return errors.New("nothing to change: give at least one flag (see airlock db edit --help)")
			}
			if err := validDatabase(&db); err != nil {
				return err
			}
			if err := store.Put(previous.Name, db); err != nil {
				return err
			}
			renamed := db.Name != previous.Name
			if !directdb.ConnectionChanged(&previous, &db) {
				outf(cmd, "Updated %s in %s\n", db.Name, store.Path())
				return nil
			}
			// The prefs and the keychain password are keyed by name and user: those of a former name or
			// user would stay orphaned, or be offered for the new user; those of a former server would be
			// sent to the new one.
			if err := forgetDirectConnection(cmd.Context(), cmd, store, previous.Name); err != nil {
				return fmt.Errorf("updated %s, but the connection remembered for %s was not forgotten: %w", db.Name, previous.Name, err)
			}
			if !renamed {
				outf(cmd, "Updated %s in %s\n", db.Name, store.Path())
				return nil
			}
			outf(cmd, "Renamed %s to %s in %s\n", previous.Name, db.Name, store.Path())
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&flags.name, "name", "", "New name of the database")
	return cmd
}
