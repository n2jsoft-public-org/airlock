package jumphost

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/dbmcp"
)

func testRunner(t *testing.T) *Runner {
	t.Helper()
	return NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, ProjectDir: t.TempDir(), MCPHost: dbmcp.NewHost()})
}

func TestStartMCPForPostgres(t *testing.T) {
	runner := testRunner(t)
	target := ideTarget{Engine: "postgres", Database: "app", RDSHost: "db.rds.amazonaws.com", RDSPort: 5432, IAMAuth: true, DBUser: DefaultRDSIAMUser}

	handle, display := runner.startMCP("archive", 15432, &target, awsConfig{Profile: "dev", Region: "eu-west-3"}, false)
	if handle == nil {
		t.Fatal("expected an MCP server for postgres")
	}
	defer handle.Close()
	if !strings.HasPrefix(handle.Display, "read-only · acme-dev-archive -> http://127.0.0.1:") {
		t.Fatalf("display = %q", handle.Display)
	}
	if display != handle.Display {
		t.Fatalf("returned display = %q, want the handle's display %q", display, handle.Display)
	}
}

func TestStartMCPSkipsUnsupportedEngine(t *testing.T) {
	runner := testRunner(t)

	handle, display := runner.startMCP("cache", 6379, &ideTarget{Engine: "redis"}, awsConfig{Profile: "dev"}, false)
	if handle != nil || display != "" {
		t.Fatalf("expected no MCP server and no display for redis, got handle=%v display=%q", handle, display)
	}
}

func TestStartMCPReportsFailureThroughTheDisplayNotStderr(t *testing.T) {
	runner := testRunner(t)
	previous := dbmcpStart
	dbmcpStart = func(*dbmcp.Host, *dbmcp.SessionConfig) (*dbmcp.Handle, error) {
		return nil, errors.New("route already mounted")
	}
	t.Cleanup(func() { dbmcpStart = previous })

	handle, display := runner.startMCP("archive", 15432, &ideTarget{Engine: "postgres"}, awsConfig{Profile: "dev"}, false)
	if handle != nil {
		t.Fatalf("expected no handle on failure, got %v", handle)
	}
	if display != "not started: route already mounted" {
		t.Fatalf("display = %q", display)
	}
}

func TestSessionReleaseStopsEverythingOnce(t *testing.T) {
	runner := testRunner(t)
	handle, _ := runner.startMCP("archive", 15432, &ideTarget{Engine: "sqlserver-se"}, awsConfig{Profile: "dev"}, false)
	portReleased := 0
	release := sessionRelease(func() { portReleased++ }, handle, func() string { return "IntelliJ data source removed" })

	message := release()
	if portReleased != 1 {
		t.Fatalf("port released %d times", portReleased)
	}
	if message != `MCP server "acme-dev-archive" stopped; IntelliJ data source removed` {
		t.Fatalf("message = %q", message)
	}
}

func TestSessionReleaseWithoutMCPOrDataSource(t *testing.T) {
	if message := sessionRelease(func() {}, nil, nil)(); message != "" {
		t.Fatalf("message = %q, want empty", message)
	}
}

func TestRDSSecretsSessionInfo(t *testing.T) {
	info := strings.Join(rdsSecretsSessionInfo(1433, "arn:aws:rds:eu-west-3:1:db:mssql", "arn:master", "admin", "--profile dev --region eu-west-3"), "\n")

	for _, want := range []string{
		"IAM authentication is not enabled",
		"host:port            = localhost:1433",
		"Values=arn:aws:rds:eu-west-3:1:db:mssql",
		"AccessRole",
		"Master credentials (admin), administrators only",
		`--secret-id "arn:master"`,
	} {
		if !strings.Contains(info, want) {
			t.Fatalf("session info misses %q:\n%s", want, info)
		}
	}
	if strings.Contains(info, "acme_ro") {
		t.Fatalf("SQL Server hints must not mention the IAM user:\n%s", info)
	}
}

func TestRDSSecretsSessionInfoWithoutMasterSecret(t *testing.T) {
	info := strings.Join(rdsSecretsSessionInfo(1433, "arn:db", "", "", ""), "\n")
	if !strings.Contains(info, "No master user secret found") {
		t.Fatalf("session info = %s", info)
	}
}

func TestAWSIAMPropertiesOnlyForIAMInstances(t *testing.T) {
	cfg := awsConfig{Profile: "dev", Region: "eu-west-3"}
	if props := awsIAMProperties(ideTarget{RDSHost: "mssql.rds.amazonaws.com", RDSPort: 1433}, cfg); props != nil {
		t.Fatalf("instance without IAM auth must not get AWS IAM properties: %v", props)
	}
	props := awsIAMProperties(ideTarget{RDSHost: "pg.rds.amazonaws.com", RDSPort: 5432, IAMAuth: true}, cfg)
	if props["AWS.RdsSigningHost"] != "pg.rds.amazonaws.com" || props["AWS.CredentialId"] != "profile:dev" {
		t.Fatalf("props = %v", props)
	}
}

func TestResolveDBUserDefaultsToReadOnlyUser(t *testing.T) {
	if user := ResolveDBUser("  "); user != DefaultRDSIAMUser {
		t.Fatalf("blank user resolved to %q, want %q", user, DefaultRDSIAMUser)
	}
	if user := ResolveDBUser(" app_ro "); user != "app_ro" {
		t.Fatalf("user = %q, want app_ro", user)
	}
}

func TestRDSIAMSessionInfoUsesChosenUser(t *testing.T) {
	info := strings.Join(rdsIAMSessionInfo(5432, "pg.rds.amazonaws.com", 5432, "app_ro", awsConfig{Profile: "dev", Region: "eu-west-3"}, "--profile dev"), "\n")

	for _, want := range []string{"user                 = app_ro", "--username app_ro --profile dev"} {
		if !strings.Contains(info, want) {
			t.Fatalf("session info misses %q:\n%s", want, info)
		}
	}
	if strings.Contains(info, DefaultRDSIAMUser) {
		t.Fatalf("session info still mentions the default user:\n%s", info)
	}
}

func TestBuildRestartCommandKeepsCustomDBUser(t *testing.T) {
	cfg := awsConfig{Profile: "dev", Region: "eu-west-3"}

	if got := buildRestartCommand("rds", "archive", "5433", "app_ro", cfg); got != "acme jumphost rds archive 5433 --profile dev --region eu-west-3 --db-user app_ro" {
		t.Fatalf("restart command = %q", got)
	}
	for _, user := range []string{"", DefaultRDSIAMUser} {
		if got := buildRestartCommand("rds", "archive", "5433", user, cfg); strings.Contains(got, "--db-user") {
			t.Fatalf("restart command with user %q = %q, want no --db-user", user, got)
		}
	}
}

func TestStartMCPLogsQueriesToTheRunnerLogDir(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, ProjectDir: t.TempDir(), MCPHost: dbmcp.NewHost(), MCPLogDir: logDir})

	handle, _ := runner.startMCP("archive", 15432, &ideTarget{Engine: "sqlserver-se"}, awsConfig{Profile: "dev"}, false)
	if handle == nil {
		t.Fatal("expected an MCP server for sql server")
	}
	defer handle.Close()
	if handle.QueryLogDir() != logDir {
		t.Fatalf("QueryLogDir() = %q, want %q", handle.QueryLogDir(), logDir)
	}
}

func TestStartMCPPassesTheEnteredPassword(t *testing.T) {
	runner := testRunner(t)
	var got *dbmcp.SessionConfig
	previous := dbmcpStart
	dbmcpStart = func(_ *dbmcp.Host, cfg *dbmcp.SessionConfig) (*dbmcp.Handle, error) {
		got = cfg
		return nil, nil
	}
	t.Cleanup(func() { dbmcpStart = previous })

	target := ideTarget{Engine: "postgres", DBUser: "app_user", DBPassword: "s3cret"}
	runner.startMCP("archive", 15432, &target, awsConfig{Profile: "dev"}, false)
	if got == nil || got.DBUser != "app_user" || got.DBPassword != "s3cret" || got.IAMAuth {
		t.Fatalf("session config = %+v, want the entered user and password without IAM", got)
	}
}

func TestRDSPasswordSessionInfoNeverShowsThePassword(t *testing.T) {
	info := strings.Join(rdsPasswordSessionInfo(5433, "app_user"), "\n")
	for _, want := range []string{"localhost:5433", "user                 = app_user", "password"} {
		if !strings.Contains(info, want) {
			t.Fatalf("session info misses %q:\n%s", want, info)
		}
	}
}

func TestRestartDBUserOnlyForIAMSessions(t *testing.T) {
	if got := restartDBUser(&ideTarget{IAMAuth: true, DBUser: "app_ro"}); got != "app_ro" {
		t.Fatalf("IAM restart user = %q, want app_ro", got)
	}
	if got := restartDBUser(&ideTarget{DBUser: "app_user", DBPassword: "s3cret"}); got != "" {
		t.Fatalf("password restart user = %q, want none", got)
	}
}

func TestDescribeAuthNamesTheSignInMode(t *testing.T) {
	if got := describeAuth(&ideTarget{IAMAuth: true, DBUser: "acme_ro"}); got != "AWS IAM token (generated by acme) · acme_ro" {
		t.Fatalf("IAM auth = %q", got)
	}
	if got := describeAuth(&ideTarget{DBUser: "acme_rw", DBPassword: "s3cret"}); got != "password · acme_rw" || strings.Contains(got, "s3cret") {
		t.Fatalf("password auth = %q", got)
	}
	if got := describeAuth(&ideTarget{Engine: "sqlserver-se"}); got != "" {
		t.Fatalf("secrets auth = %q, want none", got)
	}
}
