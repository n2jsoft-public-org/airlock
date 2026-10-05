package cli

import (
	"errors"
	"fmt"

	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/spf13/cobra"
)

func newConnectionEditCmd() *cobra.Command {
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
			store, err := openDatabases(cmd)
			if err != nil {
				return err
			}
			previous, err := getDatabase(store, args[0])
			if err != nil {
				return err
			}
			db := previous
			if !flags.applyTo(cmd, &db) {
				return errors.New("nothing to change: give at least one flag (see airlock connection edit --help)")
			}
			if err := validDatabase(&db); err != nil {
				return err
			}
			// A rename onto an existing name fails in Put: refuse before forgetting anything.
			if db.Name != previous.Name {
				if _, taken, getErr := store.Store().Get(db.Name); getErr != nil {
					return getErr
				} else if taken {
					return fmt.Errorf("database %q %w", db.Name, directdb.ErrExists)
				}
			}
			// The prefs and the keychain password are keyed by name and user: those of a former name or
			// user would stay orphaned, or be offered for the new user; those of a former server would be
			// sent to the new one. They are forgotten first, so a failure leaves the definition as it was.
			if directdb.ConnectionChanged(&previous, &db) {
				if err := forgetDirectConnection(cmd.Context(), cmd, store, previous.Name); err != nil {
					return fmt.Errorf("%s not updated: the connection remembered for it was not forgotten: %w", previous.Name, err)
				}
			}
			if err := store.Put(previous.Name, db); err != nil {
				return err
			}
			if db.Name != previous.Name {
				outf(cmd, "Renamed %s to %s in %s\n", previous.Name, db.Name, store.Path())
				return nil
			}
			outf(cmd, "Updated %s in %s\n", db.Name, store.Path())
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&flags.name, "name", "", "New name of the database")
	return cmd
}
