package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/keychain"
	"github.com/spf13/cobra"
)

// newKeychain returns the store of the passwords airlock remembers; tests replace it.
var newKeychain = keychain.NewOSStore

// newConnectionCmd groups the commands managing saved-connections.json.
func newConnectionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "connection",
		Aliases: []string{"db"},
		Short:   "Manage the saved connections: direct databases and targets reached through a jumphost",
		Long: "airlock remembers every connection in saved-connections.json: the direct databases (PostgreSQL and SQL Server " +
			"databases signed in to directly, declared with `airlock connection add`) and the RDS, EKS and ElastiCache targets " +
			"reached through an SSM jumphost, added at each connection. list and remove cover both; add, edit, test, export and " +
			"import manage the direct databases. Passwords are never written to the file, only to the OS keychain when asked.",
		// Runnable, so that cobra checks the arguments: an unknown subcommand is an error, not the help.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newConnectionAddCmd(), newConnectionEditCmd(), newConnectionListCmd(), newConnectionRemoveCmd(),
		newConnectionTestCmd(), newConnectionExportCmd(), newConnectionImportCmd())
	return cmd
}

// openDatabases returns the direct databases of saved-connections.json; only internal/cli opens it.
func openDatabases(cmd *cobra.Command) (*connections.Databases, error) {
	store, err := openSavedConnections(cmd.ErrOrStderr())
	if err != nil {
		return nil, err
	}
	return store.Databases(), nil
}

// newConnectionRunner returns a runner reaching the connection prefs and the direct databases of store and
// the keychain, printing nothing.
func newConnectionRunner(cmd *cobra.Command, store *connections.Databases) *jumphost.Runner {
	return jumphost.NewRunner(jumphost.RunnerOptions{
		In: cmd.InOrStdin(), Out: io.Discard, Err: io.Discard,
		PrefsPath: store.Path(), Keychain: newKeychain(), Databases: store,
	})
}

// getDatabase returns the definition of name, or an error telling why there is none: a target
// reached through a jumphost, or no connection of that name.
func getDatabase(store *connections.Databases, name string) (directdb.Database, error) {
	name = strings.TrimSpace(name)
	db, found, err := store.Get(name)
	if err != nil {
		return directdb.Database{}, err
	}
	if found {
		return db, nil
	}
	if err := refuseJumphostEntry(store, name); err != nil {
		return directdb.Database{}, err
	}
	return directdb.Database{}, directdb.NotFoundError(name)
}

// refuseJumphostEntry returns the error telling that name designates a target reached through a
// jumphost, which the direct database commands do not manage; nil when it designates no such target.
func refuseJumphostEntry(store *connections.Databases, name string) error {
	name = strings.TrimSpace(name)
	if matches, err := store.Store().Find(name); err == nil && len(matches) > 0 && matches[0].Jumphost != nil {
		return fmt.Errorf("%s is reached through a jumphost: it is updated at each connection", name)
	}
	return nil
}

// findConnection is the one entry name designates: a key, or <type>/<target> when exactly one
// target reached through a jumphost matches.
func findConnection(store *connections.Store, name string) (connections.Entry, error) {
	name = strings.TrimSpace(name)
	matches, err := store.Find(name)
	if err != nil {
		return connections.Entry{}, err
	}
	switch len(matches) {
	case 0:
		return connections.Entry{}, fmt.Errorf("no saved connection named %s (see airlock connection list)", name)
	case 1:
		return matches[0], nil
	}
	keys := make([]string, len(matches))
	for idx := range matches {
		keys[idx] = matches[idx].Key
	}
	return connections.Entry{}, fmt.Errorf("%s matches several connections, name one by its key: %s", name, strings.Join(keys, ", "))
}

// describeConnection names entry in a question or a completion.
func describeConnection(entry *connections.Entry) string {
	switch {
	case entry.Direct != nil:
		return fmt.Sprintf("direct database %s (%s)", entry.Key, entry.Direct.Address())
	case entry.Jumphost != nil:
		return fmt.Sprintf("%s %s (%s/%s)", entry.Jumphost.Type, entry.Jumphost.Target, entry.Jumphost.Account, entry.Jumphost.Region)
	default:
		return "invalid connection " + entry.Key
	}
}

// targetRefOf is the TargetRef keying the prefs and the keychain password of a valid entry.
func targetRefOf(entry *connections.Entry) jumphost.TargetRef {
	if entry.Direct != nil {
		return jumphost.DirectTargetRef(entry.Key)
	}
	target := entry.Jumphost
	return jumphost.TargetRef{Account: target.Account, Region: target.Region, Type: target.Type, Name: target.Target}
}

// forgetDirectConnection removes the remembered choices and the keychain password of name.
func forgetDirectConnection(ctx context.Context, cmd *cobra.Command, store *connections.Databases, name string) error {
	return newConnectionRunner(cmd, store).ForgetConnection(ctx, jumphost.DirectTargetRef(name))
}

// forgetInvalidDirectPassword deletes the keychain password an invalid direct entry may have
// remembered, for the user its JSON names: an entry that cannot be read has no choices to forget,
// but its password must not wait for the next database of that name. A target reached through a
// jumphost, or an entry naming no user, has nothing to delete.
func forgetInvalidDirectPassword(cmd *cobra.Command, store *connections.Databases, entry *connections.Entry) error {
	user, flagged := entry.DirectUser()
	if user == "" {
		return nil
	}
	return newConnectionRunner(cmd, store).ForgetDirectPassword(entry.Key, user, flagged)
}

// dbFlags are the definition flags of `db add` and `db edit`.
type dbFlags struct {
	engine     string
	host       string
	database   string
	user       string
	tls        string
	name       string
	port       int
	production bool
}

func (f *dbFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.engine, "engine", "", "Database engine: postgres or sqlserver")
	cmd.Flags().StringVar(&f.host, "host", "", "Host name or IP address of the database server")
	cmd.Flags().IntVar(&f.port, "port", 0, "Port (default 5432 for postgres, 1433 for sqlserver)")
	cmd.Flags().StringVar(&f.database, "database", "", "Database to open (default postgres on PostgreSQL, the login's default on SQL Server)")
	cmd.Flags().StringVar(&f.user, "user", "", "Database user")
	cmd.Flags().StringVar(&f.tls, "tls", "", "TLS mode: require (default, encrypted), verify (certificate and host checked) or disable")
	cmd.Flags().BoolVar(&f.production, "production", false, "Flag the database as production in the dashboard")
}

// definition returns the definition the flags describe, named name.
func (f *dbFlags) definition(name string) directdb.Database {
	return directdb.Database{
		Name: name, Engine: directdb.Engine(f.engine), Host: f.host, Port: f.port, Database: f.database,
		User: f.user, TLS: directdb.TLSMode(f.tls), Production: f.production,
	}
}

// applyTo sets the flags given on cmd over db and reports whether any was given.
func (f *dbFlags) applyTo(cmd *cobra.Command, db *directdb.Database) bool {
	changed := false
	set := func(flag string, apply func()) {
		if cmd.Flags().Changed(flag) {
			apply()
			changed = true
		}
	}
	set("engine", func() { db.Engine = directdb.Engine(f.engine) })
	set("host", func() { db.Host = f.host })
	set("port", func() { db.Port = f.port })
	set("database", func() { db.Database = f.database })
	set("user", func() { db.User = f.user })
	set("tls", func() { db.TLS = directdb.TLSMode(f.tls) })
	set("production", func() { db.Production = f.production })
	set("name", func() { db.Name = f.name })
	return changed
}

// validDatabase normalizes db and checks its name and fields.
func validDatabase(db *directdb.Database) error {
	db.Normalize()
	if err := directdb.ValidName(db.Name); err != nil {
		return err
	}
	return db.Validate()
}
