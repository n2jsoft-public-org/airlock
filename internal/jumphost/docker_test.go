package jumphost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/dbmcp"
	"github.com/n2jsoft-public-org/airlock/internal/directdb"
	"github.com/n2jsoft-public-org/airlock/internal/dockerdb"
	"github.com/n2jsoft-public-org/airlock/internal/keychain"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// fakeContainers is a DockerContainers over a map.
type fakeContainers map[string]dockerdb.Container

func (f fakeContainers) List(context.Context) ([]dockerdb.Container, error) {
	var list []dockerdb.Container
	for name := range f {
		list = append(list, f[name])
	}
	return list, nil
}

func (f fakeContainers) Get(_ context.Context, name string) (dockerdb.Container, bool, error) {
	c, ok := f[name]
	return c, ok, nil
}

func pgContainer() dockerdb.Container {
	return dockerdb.Container{Name: "pg", Image: "postgres:16", Engine: dockerdb.EnginePostgres, Host: "127.0.0.1", Port: 55432,
		User: "postgres", Password: "secret", Database: "appdb"}
}

func dockerRunner(t *testing.T, containers ...dockerdb.Container) *Runner {
	t.Helper()
	byName := fakeContainers{}
	for i := range containers {
		byName[containers[i].Name] = containers[i]
	}
	return directRunner(t, &RunnerOptions{Containers: byName})
}

func TestPrepareDockerOpensMySQLAndMariaDB(t *testing.T) {
	for engine, want := range map[dockerdb.Engine]dbmcp.Engine{dockerdb.EngineMySQL: dbmcp.EngineMySQL, dockerdb.EngineMariaDB: dbmcp.EngineMariaDB} {
		started := stubDirectMCP(t)
		c := dockerdb.Container{Name: "shop", Image: string(engine) + ":latest", Engine: engine, Host: "127.0.0.1", Port: 13306, User: "root", Trust: true}
		session, err := dockerRunner(t, c).Prepare(context.Background(), Options{ResourceType: "docker", Name: "shop"})
		if err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		session.Release()
		if len(*started) != 1 || (*started)[0].Engine != want || (*started)[0].DBName != "" || (*started)[0].Address.TLS != dbmcp.TLSDisable {
			t.Fatalf("%s: MCP config = %+v", engine, *started)
		}
	}
}

func TestPrepareDockerReadsTheContainer(t *testing.T) {
	started := stubDirectMCP(t)
	session, err := dockerRunner(t, pgContainer()).Prepare(context.Background(), Options{ResourceType: "docker", Name: "pg", MCPAllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if session.Label != "DOCKER pg" || session.ResourceType != "docker" || session.Target != "pg" || session.Production ||
		session.Environment != "" || session.LocalPort != 0 || len(session.AwsArgs) != 0 {
		t.Fatalf("session = %+v", session)
	}
	details := session.Details
	if details.ConnectedServer != "127.0.0.1:55432" || details.RestartCommand != "airlock docker pg" || details.Auth != "password · postgres" {
		t.Fatalf("details = %+v", details)
	}
	if len(session.Intro) < 2 || session.Intro[0] != "Docker container pg (postgres) at 127.0.0.1:55432" || session.Intro[1] != "TLS: disable" {
		t.Fatalf("intro = %q", session.Intro)
	}
	if strings.Contains(fmt.Sprintf("%+v %q", details, session.Intro), "secret") {
		t.Fatal("the password must never be displayed")
	}
	if len(*started) != 1 {
		t.Fatalf("started %d MCP servers", len(*started))
	}
	cfg := (*started)[0]
	if cfg.Name != "docker pg" || cfg.Engine != dbmcp.EnginePostgres || cfg.User != "postgres" || cfg.Password != "secret" ||
		cfg.DBName != "appdb" || !cfg.AllowWrites || cfg.OnPasswordRejected != nil ||
		cfg.Address != (dbmcp.Address{Host: "127.0.0.1", Port: 55432, TLS: dbmcp.TLSMode(directdb.TLSDisable)}) {
		t.Fatalf("MCP config = %+v", cfg)
	}
	if mcpserve.DirectServerName("acme", cfg.Name) != "acme-docker-pg" {
		t.Fatalf("server name = %q", mcpserve.DirectServerName("acme", cfg.Name))
	}
	if session.SignIn == nil {
		t.Fatal("a docker session checks its password")
	}
}

func TestPrepareDockerPasswords(t *testing.T) {
	trust := pgContainer()
	trust.Password, trust.Trust = "", true
	unknown := pgContainer()
	unknown.Name, unknown.Password = "unknown", ""
	cases := []struct {
		typed     string
		want      string
		container dockerdb.Container
	}{
		{container: pgContainer(), typed: "typed", want: "typed"},
		{container: pgContainer(), want: "secret"},
		{container: trust, want: ""},
	}
	for _, tc := range cases {
		started := stubDirectMCP(t)
		session, err := dockerRunner(t, tc.container).Prepare(context.Background(), Options{ResourceType: "docker", Name: tc.container.Name, DBPassword: tc.typed})
		if err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
		session.Release()
		if got := (*started)[0].Password; got != tc.want {
			t.Errorf("typed %q: password = %q, want %q", tc.typed, got, tc.want)
		}
	}
	_, err := dockerRunner(t, unknown).Prepare(context.Background(), Options{ResourceType: "docker", Name: "unknown"})
	if !errors.Is(err, ErrPasswordRequired) || err.Error() != "password required for unknown: set POSTGRES_PASSWORD on the container" {
		t.Fatalf("err = %v, want ErrPasswordRequired", err)
	}
}

func TestPrepareDockerRefusals(t *testing.T) {
	unpublished := pgContainer()
	unpublished.Port, unpublished.Problem = 0, "port 5432 is not published: run the container with -p <host-port>:5432"
	runner := dockerRunner(t, unpublished)
	if _, err := runner.Prepare(context.Background(), Options{ResourceType: "docker", Name: "nope"}); err == nil ||
		err.Error() != "no running database or cache container named nope (see airlock docker)" {
		t.Fatalf("nope = %v", err)
	}
	if _, err := runner.Prepare(context.Background(), Options{ResourceType: "docker", Name: "pg"}); err == nil ||
		err.Error() != "pg: port 5432 is not published: run the container with -p <host-port>:5432" {
		t.Fatalf("unpublished = %v", err)
	}
	withoutDocker := directRunner(t, &RunnerOptions{})
	if _, err := withoutDocker.Prepare(context.Background(), Options{ResourceType: "docker", Name: "pg"}); err == nil ||
		err.Error() != "docker databases are not available: the docker CLI was not found" {
		t.Fatalf("without docker = %v", err)
	}
}

func TestPrepareDockerRemembersNothing(t *testing.T) {
	stubDirectMCP(t)
	path := filepath.Join(t.TempDir(), "saved-connections.json")
	runner := directRunner(t, &RunnerOptions{PrefsPath: path, Keychain: keychain.NewMemoryStore(), Containers: fakeContainers{"pg": pgContainer()}})
	options := Options{ResourceType: "docker", Name: "pg", DBPassword: "secret", RememberPassword: true, SignIn: SignInPassword}
	session, err := runner.Prepare(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if err := runner.SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if _, saved := runner.LoadConnectionPrefs(context.Background(), options); saved {
		t.Fatal("a docker database must leave no prefs")
	}
	if password, found := runner.LoadKeychainPassword(context.Background(), options); found || password != "" {
		t.Fatal("a docker database must leave no keychain password")
	}
}

func TestDockerAndDirectOfTheSameNameOpenTogether(t *testing.T) {
	started := stubDirectMCP(t)
	twin := pgContainer()
	twin.Name = "legacy-billing"
	runner := directRunner(t, &RunnerOptions{Containers: fakeContainers{"legacy-billing": twin}})
	direct, err := runner.Prepare(context.Background(), Options{ResourceType: "direct", Name: "legacy-billing", DBPassword: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Release()
	docker, err := runner.Prepare(context.Background(), Options{ResourceType: "docker", Name: "legacy-billing"})
	if err != nil {
		t.Fatalf("docker twin = %v", err)
	}
	defer docker.Release()
	if (*started)[0].Name == (*started)[1].Name {
		t.Fatalf("both MCP servers are named %q", (*started)[0].Name)
	}
}

func TestPrepareDockerSlugifiesAComposeName(t *testing.T) {
	started := stubDirectMCP(t)
	c := pgContainer()
	c.Name = "My_App.db-1"
	session, err := dockerRunner(t, c).Prepare(context.Background(), Options{ResourceType: "docker", Name: "My_App.db-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if got := mcpserve.DirectServerName("acme", (*started)[0].Name); got != "acme-docker-my-app-db-1" {
		t.Fatalf("server name = %q", got)
	}
}

func TestRunDockerServesUntilCanceled(t *testing.T) {
	stubDirectMCP(t)
	var out bytes.Buffer
	runner := directRunner(t, &RunnerOptions{Out: &out, Containers: fakeContainers{"pg": pgContainer()}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Run(ctx, &Options{ResourceType: "docker", Name: "pg"}); err != nil {
		t.Fatal(err)
	}
	if output := out.String(); !strings.Contains(output, "> Docker container pg (postgres) at 127.0.0.1:55432") || strings.Contains(output, "SSM") {
		t.Fatalf("output = %s", output)
	}
	if err := runner.registry.claim("DOCKER pg", "pg"); err != nil {
		t.Fatalf("the session is still registered: %v", err)
	}
}
