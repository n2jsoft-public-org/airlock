package cli

import (
	"errors"

	"github.com/n2jsoft-public-org/airlock/internal/connections"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/spf13/cobra"
)

func newConnectionRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:               "remove <name>",
		Short:             i18n.T("cli.connection_remove.short"),
		Long:              i18n.T("cli.connection_remove.long"),
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSavedConnection,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openSavedConnections(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			entry, err := findConnection(store, args[0])
			if err != nil {
				return err
			}
			if !yes && !confirm(cmd, i18n.Format("cli.connection_remove.confirm", describeConnection(&entry))) {
				return errors.New(i18n.T("cli.connection_remove.not_removed"))
			}
			if err := removeConnection(cmd, store, &entry); err != nil {
				return err
			}
			outf(cmd, i18n.T("cli.connection_remove.removed"), entry.ShortName(), store.Path())
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("cli.connection_remove.flag.yes"))
	return cmd
}

// removeConnection forgets the keychain password and the choices of entry, then removes it: a
// failure to forget leaves it in place, so the password is never orphaned. Forgetting removes a
// jumphost entry by itself; an invalid entry has no choices, only the password of the user its
// JSON names.
func removeConnection(cmd *cobra.Command, store *connections.Store, entry *connections.Entry) error {
	if entry.Invalid != nil {
		if err := forgetInvalidDirectPassword(cmd, store.Databases(), entry); err != nil {
			return i18n.NewError("cli.connection_remove.not_removed_its_remembered", entry.ShortName(), err)
		}
		return store.Delete(entry.Key)
	}
	if err := newConnectionRunner(cmd, store.Databases()).ForgetConnection(cmd.Context(), targetRefOf(entry)); err != nil {
		return i18n.NewError("cli.connection_remove.not_removed_its_remembered_2", entry.ShortName(), err)
	}
	if entry.Direct != nil {
		return store.Databases().Delete(entry.Key)
	}
	return nil
}
