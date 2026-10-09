package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/n2jsoft/airlock/internal/i18n"
)

// savedConnectionsStore is the store of saved-connections.json, without moving older files into
// it; completion uses it, being no place to print a migration notice.
func savedConnectionsStore() (*connections.Store, error) {
	path, err := appdir.SavedConnectionsFile()
	if err != nil {
		return nil, err
	}
	return connections.NewStore(path), nil
}

// openSavedConnections opens saved-connections.json, moving databases.json and connections.json of
// an earlier airlock into it first; the notice and the warnings go to warnings. Only internal/cli
// opens it.
func openSavedConnections(warnings io.Writer) (*connections.Store, error) {
	// The former jumphost directory comes first: its connections.json is what Migrate moves.
	if legacy, err := appdir.MigrateLegacy(); err != nil {
		//nolint:errcheck // a warning that cannot be written must not fail the command
		_, _ = fmt.Fprintf(warnings, i18n.T("cli.warning"), err)
	} else if legacy {
		//nolint:errcheck // a notice that cannot be written must not fail the command
		_, _ = fmt.Fprintln(warnings, i18n.T("cli.saved_connections.copied_saved_connections_and"))
	}
	store, err := savedConnectionsStore()
	if err != nil {
		return nil, err
	}
	databasesPath, databasesErr := appdir.DatabasesFile()
	connectionsPath, connectionsErr := appdir.ConnectionsFile()
	if pathErr := errors.Join(databasesErr, connectionsErr); pathErr != nil {
		return nil, pathErr
	}
	migrated, err := store.Migrate(databasesPath, connectionsPath)
	if err != nil && !migrated {
		return nil, i18n.NewError("cli.saved_connections.saved_connections_were_not", store.Path(), err)
	}
	if migrated {
		//nolint:errcheck // a notice that cannot be written must not fail the command
		_, _ = fmt.Fprintf(warnings, i18n.T("cli.saved_connections.moved_databases_json_and"), store.Path())
	}
	if err != nil {
		//nolint:errcheck // a warning that cannot be written must not fail the command
		_, _ = fmt.Fprintf(warnings, i18n.T("cli.warning"), err)
	}
	return store, nil
}
