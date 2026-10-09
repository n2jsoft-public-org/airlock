// Package dockerdb finds the PostgreSQL, SQL Server, Redis and Valkey servers running in local
// Docker containers, with the credentials their configuration gives. Nothing is saved: every call
// asks docker again.
package dockerdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Engine is the server a container runs.
type Engine string

// The engines airlock opens in containers.
const (
	EnginePostgres  Engine = "postgres"
	EngineSQLServer Engine = "sqlserver"
	EngineRedis     Engine = "redis"
	EngineValkey    Engine = "valkey"
)

// Container is a running database container, as airlock reaches it from the host.
type Container struct {
	// Name is the container name, without the leading "/".
	Name   string
	Image  string
	Engine Engine
	User   string
	// Password is "" when the container does not give it (a *_FILE secret, or Trust).
	Password string
	Database string
	// Problem says why the container cannot be opened; "" when it can.
	Problem string
	// Host and Port are the published host address of the engine port; Port is 0 when unpublished.
	Host string
	Port int
	// Trust reports a server accepting User without password.
	Trust bool
}

// Cache reports a Redis or Valkey container, opened with the cache tools.
func (c *Container) Cache() bool {
	return c.Engine == EngineRedis || c.Engine == EngineValkey
}

// PasswordHint names where the container's password is set, for messages asking to set it.
func (c *Container) PasswordHint() string {
	switch c.Engine {
	case EngineSQLServer:
		return "MSSQL_SA_PASSWORD"
	case EngineRedis:
		return "REDIS_PASSWORD (or --requirepass)"
	case EngineValkey:
		return "VALKEY_PASSWORD (or --requirepass)"
	default:
		return "POSTGRES_PASSWORD"
	}
}

// Address is host:port, an IPv6 host bracketed.
func (c *Container) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Description is the engine, address, database and image, or the engine and the problem.
func (c *Container) Description() string {
	if c.Problem != "" {
		return string(c.Engine) + " · " + c.Problem
	}
	parts := []string{string(c.Engine), c.Address()}
	if c.Database != "" {
		parts = append(parts, c.Database)
	}
	return strings.Join(append(parts, c.Image), " · ")
}

// ErrDockerNotFound reports a machine without the docker CLI.
var ErrDockerNotFound = errors.New("docker CLI not found")

// timeout bounds each docker command: a hung daemon must not hang airlock.
const timeout = 5 * time.Second

// runDocker runs the docker CLI and returns its standard output, also on failure (docker inspect
// prints the containers it found before failing on a missing one); a package variable so tests
// replace it.
var runDocker = func(ctx context.Context, args ...string) ([]byte, error) {
	path, err := exec.LookPath("docker")
	if err != nil {
		return nil, ErrDockerNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- fixed docker subcommands and container ids.
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if line, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n"); line != "" {
			return out, fmt.Errorf("docker %s: %s", args[0], line)
		}
		return out, fmt.Errorf("docker %s: %w", args[0], err)
	}
	return out, nil
}

// Available reports whether the docker CLI is on the PATH.
func Available() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// Client finds the database containers through the docker CLI of the active Docker context.
type Client struct{}

// List returns the running PostgreSQL, SQL Server, Redis and Valkey containers, sorted by name.
func (Client) List(ctx context.Context) ([]Container, error) {
	ids, err := runDocker(ctx, "ps", "-q")
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(ids))
	if len(fields) == 0 {
		return nil, nil
	}
	data, err := runDocker(ctx, append([]string{"inspect"}, fields...)...)
	// A container removed since docker ps fails docker inspect, which still prints the others.
	if err != nil && len(bytes.TrimSpace(data)) == 0 {
		return nil, err
	}
	return parseContainers(data)
}

// Get returns the running database container named name; false when there is none.
func (c Client) Get(ctx context.Context, name string) (Container, bool, error) {
	containers, err := c.List(ctx)
	if err != nil {
		return Container{}, false, err
	}
	for idx := range containers {
		if containers[idx].Name == name {
			return containers[idx], true, nil
		}
	}
	return Container{}, false, nil
}

// binding is one host binding of a container port in docker inspect.
type binding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// inspected is what airlock reads of a docker inspect entry.
type inspected struct {
	NetworkSettings struct {
		Ports map[string][]binding `json:"Ports"`
	} `json:"NetworkSettings"`
	Name   string `json:"Name"`
	Config struct {
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
		Image        string              `json:"Image"`
		Env          []string            `json:"Env"`
		Cmd          []string            `json:"Cmd"`
		Entrypoint   []string            `json:"Entrypoint"`
	} `json:"Config"`
}

// enginePorts maps the container port of each engine, in detection order.
var enginePorts = []struct {
	port   string
	engine Engine
}{
	{port: "5432/tcp", engine: EnginePostgres},
	{port: "1433/tcp", engine: EngineSQLServer},
	{port: "6379/tcp", engine: EngineRedis},
}

// parseContainers reads docker inspect output, keeping the database containers sorted by name.
func parseContainers(data []byte) ([]Container, error) {
	var raw []inspected
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("docker inspect: unreadable output: %w", err)
	}
	var containers []Container
	for idx := range raw {
		if c, ok := containerOf(&raw[idx]); ok {
			containers = append(containers, c)
		}
	}
	slices.SortFunc(containers, func(a, b Container) int { return strings.Compare(a.Name, b.Name) })
	return containers, nil
}

// containerOf reads the database container of raw; false when it runs no supported engine.
func containerOf(raw *inspected) (Container, bool) {
	port, engine, ok := databasePort(raw)
	if !ok {
		return Container{}, false
	}
	if engine == EngineRedis && strings.Contains(strings.ToLower(raw.Config.Image), "valkey") {
		engine = EngineValkey
	}
	env := environ(raw.Config.Env)
	c := Container{Name: strings.TrimPrefix(raw.Name, "/"), Image: raw.Config.Image, Engine: engine}
	c.Host, c.Port = publishedAddress(raw.NetworkSettings.Ports[port])
	if c.Port == 0 {
		number := strings.TrimSuffix(port, "/tcp")
		c.Problem = fmt.Sprintf("port %s is not published: run the container with -p <host-port>:%s", number, number)
	}
	switch engine {
	case EnginePostgres:
		c.User = firstSet(env, "POSTGRES_USER", "POSTGRESQL_USERNAME")
		if c.User == "" {
			c.User = "postgres"
		}
		c.Password = firstSet(env, "POSTGRES_PASSWORD", "POSTGRESQL_PASSWORD")
		c.Database = firstSet(env, "POSTGRES_DB", "POSTGRESQL_DATABASE")
		if c.Database == "" {
			c.Database = c.User
		}
		c.Trust = strings.EqualFold(env["POSTGRES_HOST_AUTH_METHOD"], "trust") || strings.EqualFold(env["ALLOW_EMPTY_PASSWORD"], "yes")
	case EngineSQLServer:
		c.User = "sa"
		c.Password = firstSet(env, "MSSQL_SA_PASSWORD", "SA_PASSWORD")
	case EngineRedis, EngineValkey:
		c.User = "default"
		c.Password = requirePass(raw.Config.Entrypoint, raw.Config.Cmd)
		if c.Password == "" {
			c.Password = firstSet(env, "REDIS_PASSWORD", "VALKEY_PASSWORD")
		}
		// Bitnami refuses to start without a password unless ALLOW_EMPTY_PASSWORD: none is no auth.
		c.Trust = c.Password == ""
	}
	return c, true
}

// databasePort finds the engine port of raw among its published, then exposed, ports.
func databasePort(raw *inspected) (string, Engine, bool) {
	for _, candidate := range enginePorts {
		_, published := raw.NetworkSettings.Ports[candidate.port]
		_, exposed := raw.Config.ExposedPorts[candidate.port]
		if published || exposed {
			return candidate.port, candidate.engine, true
		}
	}
	return "", "", false
}

// publishedAddress is the first usable host binding: an unspecified address (empty, 0.0.0.0, ::)
// is reached on 127.0.0.1. Port 0 when none is usable.
func publishedAddress(bindings []binding) (host string, port int) {
	for _, b := range bindings {
		port, err := strconv.Atoi(b.HostPort)
		if err != nil || port <= 0 {
			continue
		}
		ip := net.ParseIP(b.HostIP)
		switch {
		case b.HostIP == "" || ip.IsUnspecified():
			return "127.0.0.1", port
		case ip != nil:
			return b.HostIP, port
		}
	}
	return "", 0
}

// environ maps the KEY=value entries of a container, split on the first "=".
func environ(entries []string) map[string]string {
	env := make(map[string]string, len(entries))
	for _, entry := range entries {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	return env
}

// firstSet is the value of the first of keys set to a non-empty value; "" when none is.
func firstSet(env map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := env[key]; value != "" {
			return value
		}
	}
	return ""
}

// requirePass is the value of --requirepass in the first of lists giving it; "" when none does. A
// command given as one shell string is not parsed.
func requirePass(lists ...[]string) string {
	for _, list := range lists {
		for idx, arg := range list {
			if value, ok := strings.CutPrefix(arg, "--requirepass="); ok {
				return value
			}
			if arg == "--requirepass" && idx+1 < len(list) {
				return list[idx+1]
			}
		}
	}
	return ""
}
