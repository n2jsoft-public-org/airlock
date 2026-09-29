package jumphost

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/acme-cli/internal/dbmcp"
)

func testRunner(t *testing.T) *Runner {
	t.Helper()
	return NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, ProjectDir: t.TempDir(), MCPHost: dbmcp.NewHost()})
}

func TestStartMCPForPostgres(t *testing.T) {
	runner := testRunner(t)
	target := ideTarget{Engine: "postgres", Database: "app", RDSHost: "db.rds.amazonaws.com", RDSPort: 5432, IAMAuth: true}

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
