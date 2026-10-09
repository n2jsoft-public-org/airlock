package dockerdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/directdb"
)

// fixture is the docker inspect entry of a container publishing port with bindings (JSON, or null).
func fixture(t *testing.T, name, image, port, bindings string, env ...string) string {
	t.Helper()
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`{"Name":"/%s","Config":{"Image":%q,"Env":%s,"ExposedPorts":{%q:{}}},"NetworkSettings":{"Ports":{%q:%s}}}`,
		name, image, envJSON, port, port, bindings)
}

const published = `[{"HostIp":"0.0.0.0","HostPort":"55432"},{"HostIp":"::","HostPort":"55432"}]`

func parseOne(t *testing.T, entry string) Container {
	t.Helper()
	containers, err := parseContainers([]byte("[" + entry + "]"))
	if err != nil || len(containers) != 1 {
		t.Fatalf("containers = %+v, err = %v", containers, err)
	}
	return containers[0]
}

func TestParseOfficialPostgres(t *testing.T) {
	got := parseOne(t, fixture(t, "pg", "postgres:16", "5432/tcp", published, "POSTGRES_PASSWORD=secret", "POSTGRES_DB=appdb", "PATH=/usr/bin"))
	want := Container{Name: "pg", Image: "postgres:16", Engine: directdb.EnginePostgres, Host: "127.0.0.1", Port: 55432,
		User: "postgres", Password: "secret", Database: "appdb"}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if got.Address() != "127.0.0.1:55432" || got.Description() != "postgres · 127.0.0.1:55432 · appdb · postgres:16" {
		t.Fatalf("address = %q description = %q", got.Address(), got.Description())
	}
}

func TestParsePostgresVariants(t *testing.T) {
	cases := []struct {
		want func(c Container) bool
		name string
		env  []string
	}{
		{func(c Container) bool { return c.User == "app" && c.Password == "pw" && c.Database == "shop" }, "bitnami", []string{"POSTGRESQL_USERNAME=app", "POSTGRESQL_PASSWORD=pw", "POSTGRESQL_DATABASE=shop"}},
		{func(c Container) bool { return c.User == "alice" && c.Database == "alice" }, "database defaults to the user", []string{"POSTGRES_USER=alice", "POSTGRES_PASSWORD=pw"}},
		{func(c Container) bool { return c.Trust && c.Password == "" && c.Problem == "" }, "trust", []string{"POSTGRES_HOST_AUTH_METHOD=trust"}},
		{func(c Container) bool { return c.Trust }, "bitnami empty password", []string{"ALLOW_EMPTY_PASSWORD=yes"}},
		{func(c Container) bool { return c.Password == "" && !c.Trust && c.Problem == "" }, "password file is unknown", []string{"POSTGRES_PASSWORD_FILE=/run/secrets/pw"}},
		{func(c Container) bool { return c.Password == `a=b$c'd"e` }, "password kept verbatim", []string{`POSTGRES_PASSWORD=a=b$c'd"e`}},
	}
	for _, tc := range cases {
		if got := parseOne(t, fixture(t, "pg", "postgres:16", "5432/tcp", published, tc.env...)); !tc.want(got) {
			t.Errorf("%s: got %+v", tc.name, got)
		}
	}
}

func TestParseSQLServer(t *testing.T) {
	for _, variable := range []string{"MSSQL_SA_PASSWORD", "SA_PASSWORD"} {
		got := parseOne(t, fixture(t, "mssql", "mcr.microsoft.com/mssql/server:2022-latest", "1433/tcp",
			`[{"HostIp":"","HostPort":"1433"}]`, "ACCEPT_EULA=Y", variable+"=Str0ng!pass"))
		if got.Engine != directdb.EngineSQLServer || got.User != "sa" || got.Password != "Str0ng!pass" || got.Database != "" ||
			got.Host != "127.0.0.1" || got.Port != 1433 {
			t.Errorf("%s: got %+v", variable, got)
		}
	}
}

func TestParseAddresses(t *testing.T) {
	cases := map[string]string{
		`[{"HostIp":"127.0.0.1","HostPort":"55432"}]`:    "127.0.0.1:55432",
		`[{"HostIp":"192.168.1.10","HostPort":"55432"}]`: "192.168.1.10:55432",
		`[{"HostIp":"::","HostPort":"55432"}]`:           "127.0.0.1:55432",
		`[{"HostIp":"::1","HostPort":"55432"}]`:          "[::1]:55432",
	}
	for bindings, want := range cases {
		if got := parseOne(t, fixture(t, "pg", "postgres:16", "5432/tcp", bindings, "POSTGRES_PASSWORD=pw")); got.Address() != want {
			t.Errorf("%s: address = %q, want %q", bindings, got.Address(), want)
		}
	}
}

func TestParseUnpublishedPort(t *testing.T) {
	got := parseOne(t, fixture(t, "pg", "postgres:16", "5432/tcp", "null", "POSTGRES_PASSWORD=pw"))
	if got.Port != 0 || got.Problem != "port 5432 is not published: run the container with -p <host-port>:5432" {
		t.Fatalf("got %+v", got)
	}
	if got.Description() != "postgres · port 5432 is not published: run the container with -p <host-port>:5432" {
		t.Fatalf("description = %q", got.Description())
	}
}

func TestParseSkipsOtherContainersAndSorts(t *testing.T) {
	data := "[" + strings.Join([]string{
		fixture(t, "zeta", "postgres:16", "5432/tcp", published, "POSTGRES_PASSWORD=pw"),
		fixture(t, "cache", "redis:7", "6379/tcp", `[{"HostIp":"0.0.0.0","HostPort":"6379"}]`),
		fixture(t, "alpha", "postgres:16", "5432/tcp", published, "POSTGRES_PASSWORD=pw"),
	}, ",") + "]"
	containers, err := parseContainers([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(containers))
	for _, c := range containers {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{"alpha", "zeta"}) {
		t.Fatalf("names = %v", names)
	}
}

// stubDocker replaces the docker CLI with fn and records its arguments.
func stubDocker(t *testing.T, fn func(args []string) ([]byte, error)) *[][]string {
	t.Helper()
	var calls [][]string
	previous := runDocker
	runDocker = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return fn(args)
	}
	t.Cleanup(func() { runDocker = previous })
	return &calls
}

func TestListInspectsTheRunningContainers(t *testing.T) {
	entry := fixture(t, "pg", "postgres:16", "5432/tcp", published, "POSTGRES_PASSWORD=pw")
	calls := stubDocker(t, func(args []string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("abc\ndef\n"), nil
		}
		return []byte("[" + entry + "]"), nil
	})
	containers, err := Client{}.List(context.Background())
	if err != nil || len(containers) != 1 || containers[0].Name != "pg" {
		t.Fatalf("containers = %+v, err = %v", containers, err)
	}
	if len(*calls) != 2 || !slices.Equal((*calls)[0], []string{"ps", "-q"}) || !slices.Equal((*calls)[1], []string{"inspect", "abc", "def"}) {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestListWithoutContainersSkipsInspect(t *testing.T) {
	calls := stubDocker(t, func([]string) ([]byte, error) { return nil, nil })
	containers, err := Client{}.List(context.Background())
	if err != nil || containers != nil || len(*calls) != 1 {
		t.Fatalf("containers = %+v, err = %v, calls = %v", containers, err, *calls)
	}
}

func TestListReportsTheDockerError(t *testing.T) {
	stubDocker(t, func([]string) ([]byte, error) {
		return nil, errors.New("docker ps: Cannot connect to the Docker daemon")
	})
	if _, err := (Client{}).List(context.Background()); err == nil || err.Error() != "docker ps: Cannot connect to the Docker daemon" {
		t.Fatalf("err = %v", err)
	}
}

func TestListKeepsTheContainersAPartialInspectPrinted(t *testing.T) {
	entry := fixture(t, "pg", "postgres:16", "5432/tcp", published, "POSTGRES_PASSWORD=pw")
	stubDocker(t, func(args []string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("abc\ngone\n"), nil
		}
		// docker inspect exits 1 for the container removed meanwhile, and prints the others.
		return []byte("[" + entry + "]"), errors.New("docker inspect: Error: No such object: gone")
	})
	containers, err := Client{}.List(context.Background())
	if err != nil || len(containers) != 1 {
		t.Fatalf("containers = %+v, err = %v", containers, err)
	}
}

func TestGetFindsByName(t *testing.T) {
	entry := fixture(t, "pg", "postgres:16", "5432/tcp", published, "POSTGRES_PASSWORD=pw")
	stubDocker(t, func(args []string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("abc"), nil
		}
		return []byte("[" + entry + "]"), nil
	})
	if c, found, err := (Client{}).Get(context.Background(), "pg"); err != nil || !found || c.Password != "pw" {
		t.Fatalf("pg = %+v, %v, %v", c, found, err)
	}
	if _, found, err := (Client{}).Get(context.Background(), "nope"); err != nil || found {
		t.Fatalf("nope = %v, %v", found, err)
	}
}

func TestRunDockerWithoutTheCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := runDocker(context.Background(), "ps", "-q"); !errors.Is(err, ErrDockerNotFound) {
		t.Fatalf("err = %v, want ErrDockerNotFound", err)
	}
	if Available() {
		t.Fatal("Available without docker on the PATH")
	}
}
