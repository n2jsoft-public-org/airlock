package jumphost

import (
	"context"
	"fmt"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/dbmcp"
	"github.com/n2jsoft-public-org/airlock/internal/directdb"
	"github.com/n2jsoft-public-org/airlock/internal/dockerdb"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// resourceDocker is a database running in a local Docker container, found live: nothing about
// it is remembered.
const resourceDocker = "docker"

// DockerContainers finds the running database containers (internal/dockerdb in the CLI).
type DockerContainers interface {
	List(ctx context.Context) ([]dockerdb.Container, error)
	Get(ctx context.Context, name string) (dockerdb.Container, bool, error)
}

// prepareDocker opens the database or cache (routed to openDockerCache) of the running container
// options.Name, read again now: its published port, user, database, and password unless
// options.DBPassword gives one. Nothing is remembered: no prefs, no keychain.
func (r *Runner) prepareDocker(ctx context.Context, options *Options) (*Session, error) {
	if r.containers == nil {
		return nil, i18n.NewError("jumphost.docker.docker_databases_are_not")
	}
	name := strings.TrimSpace(options.Name)
	c, found, err := r.containers.Get(ctx, name)
	switch {
	case err != nil:
		return nil, err
	case !found:
		return nil, i18n.NewError("jumphost.docker.no_running_database_or", name)
	case c.Problem != "":
		return nil, fmt.Errorf("%s: %s", name, c.Problem)
	}
	password := options.DBPassword
	if password == "" {
		password = c.Password
	}
	if password == "" && !c.Trust {
		return nil, i18n.NewError("jumphost.docker.for_set_on_container", ErrPasswordRequired, name, c.PasswordHint())
	}
	if c.Cache() {
		return r.openDockerCache(options, &c, password)
	}
	engine, ok := dbmcp.EngineFor(string(c.Engine))
	if !ok {
		return nil, fmt.Errorf("%w: %s", dbmcp.ErrEngineUnsupported, c.Engine)
	}
	db := directdb.Database{Name: name, Engine: directdb.Engine(c.Engine), Host: c.Host, Port: c.Port, Database: c.Database, User: c.User, TLS: directdb.TLSDisable}
	return r.openDatabase(options, &db, engine, password, &databaseSpec{
		resourceType: resourceDocker, label: "DOCKER " + name, title: "docker " + name,
		intro: fmt.Sprintf("Docker container %s (%s) at %s", name, c.Engine, directAddress(&db)),
	})
}
