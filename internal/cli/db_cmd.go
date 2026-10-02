package cli

import (
	"context"
	"io"
	"strings"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/keychain"
	"github.com/spf13/cobra"
)

// newKeychain returns the store of the passwords airlock remembers; tests replace it.
var newKeychain = keychain.NewOSStore

// newDBCmd groups the commands managing the direct databases of databases.json.
func newDBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Manage the direct databases (reached without jumphost) of databases.json",
		Long: "Direct databases are PostgreSQL and SQL Server databases airlock signs in to directly, with a user and a password, " +
			"instead of through an SSM jumphost. Open one with `airlock direct <name>` or from the dashboard; " +
			"passwords are never written to databases.json, only to the OS keychain when asked.",
		// Runnable, so that cobra checks the arguments: an unknown subcommand is an error, not the help.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newDBAddCmd(), newDBEditCmd(), newDBListCmd(), newDBRemoveCmd(),
		newDBTestCmd(), newDBExportCmd(), newDBImportCmd())
	return cmd
}

// openDatabaseStore returns the store of databases.json; only internal/cli opens it.
func openDatabaseStore() (*directdb.Store, error) {
	path, err := appdir.DatabasesFile()
	if err != nil {
		return nil, err
	}
	return directdb.NewStore(path), nil
}

// newDBRunner returns a runner reaching the connection prefs, the keychain and the direct
// databases of store, printing nothing.
func newDBRunner(cmd *cobra.Command, store *directdb.Store) (*jumphost.Runner, error) {
	prefsPath, err := appdir.ConnectionsFile()
	if err != nil {
		return nil, err
	}
	return jumphost.NewRunner(jumphost.RunnerOptions{
		In: cmd.InOrStdin(), Out: io.Discard, Err: io.Discard,
		PrefsPath: prefsPath, Keychain: newKeychain(), Databases: store,
	}), nil
}

// getDatabase returns the definition of name, or an error telling how to list them.
func getDatabase(store *directdb.Store, name string) (directdb.Database, error) {
	name = strings.TrimSpace(name)
	db, found, err := store.Get(name)
	if err != nil {
		return directdb.Database{}, err
	}
	if !found {
		return directdb.Database{}, directdb.NotFoundError(name)
	}
	return db, nil
}

// forgetDirectConnection removes the remembered choices and the keychain password of name.
func forgetDirectConnection(ctx context.Context, cmd *cobra.Command, store *directdb.Store, name string) error {
	runner, err := newDBRunner(cmd, store)
	if err != nil {
		return err
	}
	return runner.ForgetConnection(ctx, jumphost.DirectTargetRef(name))
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
