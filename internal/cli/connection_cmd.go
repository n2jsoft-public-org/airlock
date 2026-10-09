package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
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
		Short:   i18n.T("cli.connection.short"),
		Long:    i18n.T("cli.connection.long"),
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
	return jumphost.NewRunner(&jumphost.RunnerOptions{
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
		return i18n.NewError("cli.connection.is_reached_through_jumphost", name)
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
		return connections.Entry{}, i18n.NewError("cli.connection.no_saved_connection_named", name)
	case 1:
		return matches[0], nil
	}
	keys := make([]string, len(matches))
	for idx := range matches {
		keys[idx] = matches[idx].Key
	}
	return connections.Entry{}, i18n.NewError("cli.connection.matches_several_connections_name", name, strings.Join(keys, ", "))
}

// describeConnection names entry in a question or a completion.
func describeConnection(entry *connections.Entry) string {
	switch {
	case entry.Direct != nil:
		return i18n.Format("cli.connection.direct_database", entry.Key, entry.Direct.Address())
	case entry.Jumphost != nil:
		return fmt.Sprintf("%s %s (%s/%s)", entry.Jumphost.Type, entry.Jumphost.Target, entry.Jumphost.Account, entry.Jumphost.Region)
	default:
		return i18n.Format("cli.connection.invalid", entry.Key)
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

	environment string
}

func (f *dbFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.engine, "engine", "", i18n.T("cli.connection.flag.engine"))
	cmd.Flags().StringVar(&f.host, "host", "", i18n.T("cli.connection.flag.host"))
	cmd.Flags().IntVar(&f.port, "port", 0, i18n.T("cli.connection.flag.port"))
	cmd.Flags().StringVar(&f.database, "database", "", i18n.T("cli.connection.flag.database"))
	cmd.Flags().StringVar(&f.user, "user", "", i18n.T("cli.connection.flag.user"))
	cmd.Flags().StringVar(&f.tls, "tls", "", i18n.T("cli.connection.flag.tls"))
	cmd.Flags().StringVar(&f.environment, "environment", "", i18n.T("cli.connection.flag.environment"))
	cmd.Flags().BoolVar(&f.production, "production", false, i18n.T("cli.connection.flag.production"))
	cobra.CheckErr(cmd.Flags().MarkHidden("production"))
	cmd.MarkFlagsMutuallyExclusive("environment", "production")
}

// definition returns the definition the flags describe, named name.
func (f *dbFlags) definition(name string) directdb.Database {
	return directdb.Database{
		Name: name, Engine: directdb.Engine(f.engine), Host: f.host, Port: f.port, Database: f.database,
		User: f.user, TLS: directdb.TLSMode(f.tls), Environment: f.chosenEnvironment(),
	}
}

// chosenEnvironment is the environment the flags give: --environment, else --production's.
func (f *dbFlags) chosenEnvironment() string {
	if f.environment != "" {
		return f.environment
	}
	return productionEnvironment(f.production)
}

// checkEnvironment refuses a name that is neither configured nor prod; "" is none.
func checkEnvironment(envs environment.Set, name string) error {
	if env := envs.Lookup(name); env.IsZero() || env.Configured {
		return nil
	}
	return i18n.NewError("cli.connection.environment_is_not_in",
		strings.ToLower(strings.TrimSpace(name)), strings.Join(envs.Names(), ", "))
}

// productionEnvironment is the environment --production stands for.
func productionEnvironment(production bool) string {
	if production {
		return environment.ProductionAlias
	}
	return ""
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
	set("environment", func() { db.Environment = f.environment })
	set("production", func() { db.Environment = productionEnvironment(f.production) })
	set("name", func() { db.Name = f.name })
	return changed
}

// validDatabase normalizes db and checks its name and fields, and its environment when
// checkEnv is set: an imported database may hold an environment the setting lacks, which an
// edit of another field must not refuse. The production environment is saved as prod, whatever
// the team calls it: a teammate's production may be named differently.
func validDatabase(cmd *cobra.Command, db *directdb.Database, checkEnv bool) error {
	db.Normalize()
	if err := directdb.ValidName(db.Name); err != nil {
		return err
	}
	if err := db.Validate(); err != nil {
		return err
	}
	cfg, _, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	envs := cfg.EnvironmentSet()
	if checkEnv {
		if err := checkEnvironment(envs, db.Environment); err != nil {
			return err
		}
	}
	if envs.Lookup(db.Environment).Production {
		db.Environment = environment.ProductionAlias
	}
	return nil
}
