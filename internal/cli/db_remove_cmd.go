package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func newDBRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:               "remove <name>",
		Short:             "Remove a direct database, its remembered choices and its keychain password",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeDirectDatabase,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openDatabaseStore()
			if err != nil {
				return err
			}
			db, err := getDatabase(store, args[0])
			if err != nil {
				return err
			}
			if !yes && !confirm(cmd, fmt.Sprintf("Remove direct database %s (%s)? [y/N] ", db.Name, db.Address())) {
				return errors.New("not removed")
			}
			if err := store.Delete(db.Name); err != nil {
				return err
			}
			if err := forgetDirectConnection(cmd.Context(), cmd, store, db.Name); err != nil {
				return fmt.Errorf("removed %s, but its remembered connection was not forgotten: %w", db.Name, err)
			}
			outf(cmd, "Removed %s from %s\n", db.Name, store.Path())
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Remove without asking")
	return cmd
}
