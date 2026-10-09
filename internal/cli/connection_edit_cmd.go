package cli

import (
	"errors"

	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/spf13/cobra"
)

func newConnectionEditCmd() *cobra.Command {
	var flags dbFlags
	cmd := &cobra.Command{
		Use:               "edit <name>",
		Short:             i18n.T("cli.connection_edit.short"),
		Long:              i18n.T("cli.connection_edit.long"),
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
				return errors.New(i18n.T("cli.connection_edit.nothing_to_change_give"))
			}
			if err := validDatabase(cmd, &db, cmd.Flags().Changed("environment") || cmd.Flags().Changed("production")); err != nil {
				return err
			}
			// A rename onto an existing name fails in Put: refuse before forgetting anything.
			if db.Name != previous.Name {
				if _, taken, getErr := store.Store().Get(db.Name); getErr != nil {
					return getErr
				} else if taken {
					return i18n.NewError("cli.connection_edit.database", db.Name, directdb.ErrExists)
				}
			}
			// The prefs and the keychain password are keyed by name and user: those of a former name or
			// user would stay orphaned, or be offered for the new user; those of a former server would be
			// sent to the new one. They are forgotten first, so a failure leaves the definition as it was.
			if directdb.ConnectionChanged(&previous, &db) {
				if err := forgetDirectConnection(cmd.Context(), cmd, store, previous.Name); err != nil {
					return i18n.NewError("cli.connection_edit.not_updated_connection_remembered", previous.Name, err)
				}
			}
			if err := store.Put(previous.Name, db); err != nil {
				return err
			}
			if db.Name != previous.Name {
				outf(cmd, i18n.T("cli.connection_edit.renamed"), previous.Name, db.Name, store.Path())
				return nil
			}
			outf(cmd, i18n.T("cli.connection_edit.updated"), db.Name, store.Path())
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&flags.name, "name", "", i18n.T("cli.connection_edit.flag.name"))
	return cmd
}
