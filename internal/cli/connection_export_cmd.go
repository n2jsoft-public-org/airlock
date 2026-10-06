package cli

import (
	"fmt"
	"strings"

	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/cobra"
)

func newConnectionExportCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "export [name...]",
		Short: "Print direct databases as one string to share (never their passwords)",
		Long: "Prints the direct databases as one string for teammates to import with `airlock connection import <string>`, " +
			"which shows every change before applying it. Passwords are never part of it. Without names, a terminal " +
			"lists every database, all selected, to uncheck those to leave out; --all, or no terminal, exports them all.",
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
			if len(args) == 0 && !all {
				if selected, err = pickDatabases(cmd, selected); err != nil {
					return err
				}
			}
			shared, err := directdb.Export(selected)
			if err != nil {
				return err
			}
			outf(cmd, "%s\n", shared)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Export every direct database without asking")
	return cmd
}

// pickDatabases lets the user uncheck the databases to leave out; all of dbs without a terminal.
func pickDatabases(cmd *cobra.Command, dbs []directdb.Database) ([]directdb.Database, error) {
	choices := make([]ui.ExportChoice, len(dbs))
	for idx := range dbs {
		choices[idx] = ui.ExportChoice{Name: dbs[idx].Name, Detail: describeExport(&dbs[idx]), Production: dbs[idx].Production}
	}
	indexes, asked, err := pickExports(cmd, "Export direct databases", choices)
	if err != nil || !asked {
		return dbs, err
	}
	picked := make([]directdb.Database, len(indexes))
	for pos, idx := range indexes {
		picked[pos] = dbs[idx]
	}
	return picked, nil
}

// describeExport is the line of db in the export picker.
func describeExport(db *directdb.Database) string {
	parts := []string{string(db.Engine), db.Address()}
	if db.Database != "" {
		parts = append(parts, db.Database)
	}
	if db.User != "" {
		parts = append(parts, db.User)
	}
	return strings.Join(parts, " · ")
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
