package jumphost

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/dockerdb"
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
		return nil, errors.New("docker databases are not available: the docker CLI was not found")
	}
	name := strings.TrimSpace(options.Name)
	c, found, err := r.containers.Get(ctx, name)
	switch {
	case err != nil:
		return nil, err
	case !found:
		return nil, fmt.Errorf("no running database or cache container named %s (see airlock docker)", name)
	case c.Problem != "":
		return nil, fmt.Errorf("%s: %s", name, c.Problem)
	}
	password := options.DBPassword
	if password == "" {
		password = c.Password
	}
	if password == "" && !c.Trust {
		return nil, fmt.Errorf("%w for %s: set %s on the container", ErrPasswordRequired, name, c.PasswordHint())
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
