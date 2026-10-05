package cli

import (
	"errors"
	"fmt"

	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/spf13/cobra"
)

func newConnectionRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a saved connection, its remembered choices and its keychain password",
		Long: "Names a direct database by its name, a target reached through a jumphost by its key " +
			"(<account>/<region>/<type>/<target>) or by <type>/<target> when only one matches.",
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
			if !yes && !confirm(cmd, fmt.Sprintf("Remove %s? [y/N] ", describeConnection(&entry))) {
				return errors.New("not removed")
			}
			if err := removeConnection(cmd, store, &entry); err != nil {
				return err
			}
			outf(cmd, "Removed %s from %s\n", entry.ShortName(), store.Path())
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Remove without asking")
	return cmd
}

// removeConnection forgets the keychain password and the choices of entry, then removes it: a
// failure to forget leaves it in place, so the password is never orphaned. Forgetting removes a
// jumphost entry by itself; an invalid entry has no choices, only the password of the user its
// JSON names.
func removeConnection(cmd *cobra.Command, store *connections.Store, entry *connections.Entry) error {
	if entry.Invalid != nil {
		if err := forgetInvalidDirectPassword(cmd, store.Databases(), entry); err != nil {
			return fmt.Errorf("%s not removed: its remembered password was not forgotten: %w", entry.ShortName(), err)
		}
		return store.Delete(entry.Key)
	}
	if err := newConnectionRunner(cmd, store.Databases()).ForgetConnection(cmd.Context(), targetRefOf(entry)); err != nil {
		return fmt.Errorf("%s not removed: its remembered connection was not forgotten: %w", entry.ShortName(), err)
	}
	if entry.Direct != nil {
		return store.Databases().Delete(entry.Key)
	}
	return nil
}
