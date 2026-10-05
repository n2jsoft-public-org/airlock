package cli

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/spf13/cobra"
)

func newConnectionListCmd() *cobra.Command {
	var directOnly, jumphostOnly bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the saved connections, most recently used first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := openSavedConnections(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			entries, err := store.List()
			if err != nil {
				return err
			}
			entries = filterConnections(entries, directOnly, jumphostOnly)
			if len(entries) == 0 {
				outf(cmd, "No saved connection: add a direct database with `airlock connection add <name> --engine postgres --host <host> --user <user>`, or connect to an AWS target\n")
				return nil
			}
			sortConnections(entries)
			return printConnections(cmd.OutOrStdout(), entries)
		},
	}
	cmd.Flags().BoolVar(&directOnly, "direct", false, "List only the direct databases")
	cmd.Flags().BoolVar(&jumphostOnly, "jumphost", false, "List only the targets reached through a jumphost")
	cmd.MarkFlagsMutuallyExclusive("direct", "jumphost")
	return cmd
}

// filterConnections keeps the direct or the jumphost entries when asked; every entry otherwise.
func filterConnections(entries []connections.Entry, directOnly, jumphostOnly bool) []connections.Entry {
	if !directOnly && !jumphostOnly {
		return entries
	}
	kept := entries[:0]
	for idx := range entries {
		if (directOnly && entries[idx].Direct != nil) || (jumphostOnly && entries[idx].Jumphost != nil) {
			kept = append(kept, entries[idx])
		}
	}
	return kept
}

// listRank orders the used connections first, then those never opened, then the invalid ones.
func listRank(entry *connections.Entry) int {
	switch {
	case entry.Invalid != nil:
		return 2
	case entry.Choices.UsedAt.IsZero():
		return 1
	default:
		return 0
	}
}

// sortConnections orders entries most recently used first, then by key.
func sortConnections(entries []connections.Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := &entries[i], &entries[j]
		if rankA, rankB := listRank(a), listRank(b); rankA != rankB {
			return rankA < rankB
		}
		if !a.Choices.UsedAt.Equal(b.Choices.UsedAt) {
			return a.Choices.UsedAt.After(b.Choices.UsedAt)
		}
		return a.Key < b.Key
	})
}

// printConnections writes the table of entries; a short name several entries share gives way to the key.
func printConnections(w io.Writer, entries []connections.Entry) error {
	shared := map[string]int{}
	for idx := range entries {
		shared[entries[idx].ShortName()]++
	}
	table := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(table, "NAME\tVIA\tTYPE\tTARGET\tUSER\tLAST USED\tPRODUCTION"); err != nil {
		return err
	}
	for idx := range entries {
		entry := &entries[idx]
		name := entry.ShortName()
		if shared[name] > 1 {
			name = entry.Key
		}
		lastUsed := "never"
		if !entry.Choices.UsedAt.IsZero() {
			lastUsed = entry.Choices.UsedAt.Local().Format("2006-01-02 15:04")
		}
		via, kind, target, user, production := connectionColumns(entry)
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, via, kind, target, user, lastUsed, production); err != nil {
			return err
		}
	}
	return table.Flush()
}

// connectionColumns are the VIA, TYPE, TARGET, USER and PRODUCTION columns of entry.
func connectionColumns(entry *connections.Entry) (via, kind, target, user, production string) {
	switch {
	case entry.Direct != nil:
		if entry.Direct.Production {
			production = "yes"
		}
		return "direct", string(entry.Direct.Engine), entry.Direct.Address(), entry.Direct.User, production
	case entry.Jumphost != nil:
		where := entry.Jumphost.Target + " @ " + entry.Jumphost.Account + "/" + entry.Jumphost.Region
		return "jumphost", entry.Jumphost.Type, where, entry.Choices.User, ""
	default:
		return "?", "?", "invalid: " + entry.Invalid.Error(), "", ""
	}
}
