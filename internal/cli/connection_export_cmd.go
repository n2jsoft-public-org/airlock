package cli

import (
	"fmt"
	"strings"

	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/spf13/cobra"
)

func newConnectionExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export [name...]",
		Short: "Print direct databases as one string to share (never their passwords)",
		Long: "Prints every direct database, or the named ones, as one string for teammates to import with " +
			"`airlock connection import <string>`, which shows every change before applying it. Passwords are never part of it.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeDirectDatabases,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openDatabases(cmd)
			if err != nil {
				return err
			}
			dbs, err := store.List()
			if err != nil {
				return err
			}
			err = refuseJumphostNames(store, dbs, args)
			if err != nil {
				return err
			}
			selected, err := selectDatabases(dbs, args)
			if err != nil {
				return err
			}
			shared, err := directdb.Export(selected)
			if err != nil {
				return err
			}
			outf(cmd, "%s\n", shared)
			return nil
		},
	}
}

// selectDatabases keeps the databases named in names, in that order; all of them when names is empty.
func selectDatabases(dbs []directdb.Database, names []string) ([]directdb.Database, error) {
	if len(names) == 0 {
		return dbs, nil
	}
	byName := make(map[string]int, len(dbs))
	for idx := range dbs {
		byName[dbs[idx].Name] = idx
	}
	selected := make([]directdb.Database, 0, len(names))
	for _, name := range names {
		idx, ok := byName[strings.TrimSpace(name)]
		if !ok {
			return nil, fmt.Errorf("no direct database named %s (see airlock connection list)", strings.TrimSpace(name))
		}
		selected = append(selected, dbs[idx])
	}
	return selected, nil
}

// refuseJumphostNames returns the error of the first of names that is no direct database but
// designates a target reached through a jumphost.
func refuseJumphostNames(store *connections.Databases, dbs []directdb.Database, names []string) error {
	direct := make(map[string]bool, len(dbs))
	for idx := range dbs {
		direct[dbs[idx].Name] = true
	}
	for _, name := range names {
		if direct[strings.TrimSpace(name)] {
			continue
		}
		if err := refuseJumphostEntry(store, name); err != nil {
			return err
		}
	}
	return nil
}
