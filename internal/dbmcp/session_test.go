package dbmcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSessionConfig(t *testing.T, engine string) *SessionConfig {
	t.Helper()
	return &SessionConfig{
		AWS:        (&fakeAWS{}).run,
		ProjectDir: t.TempDir(),
		Profile:    "dev", Region: "eu-west-3", Target: "Archive_DB",
		AWSEngine: engine, RDSHost: "archive.rds.amazonaws.com", RDSPort: 5432,
		IAMUser: "acme_ro", IAMAuth: true, LocalPort: 15432,
	}
}

func TestStartSkipsUnsupportedEngine(t *testing.T) {
	handle, err := Start(NewHost(), testSessionConfig(t, "mysql"))
	if err != nil || handle != nil {
		t.Fatalf("handle = %v, err = %v; want nil, nil", handle, err)
	}
}

func TestStartRegistersAndCloseCleansUp(t *testing.T) {
	useIsolatedLocks(t)
	cfg := testSessionConfig(t, "postgres")
	_ = os.Mkdir(filepath.Join(cfg.ProjectDir, ".claude"), 0o750)
	host := NewHost()

	handle, err := Start(host, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(handle.Display, "MCP acme-dev-archive-db -> http://127.0.0.1:") || !strings.HasSuffix(handle.Display, "(.mcp.json)") {
		t.Fatalf("display = %q", handle.Display)
	}
	config := readFile(t, filepath.Join(cfg.ProjectDir, ".mcp.json"))
	if !strings.Contains(config, `"acme-dev-archive-db"`) {
		t.Fatalf(".mcp.json = %s", config)
	}

	message := handle.Close()
	if message != `MCP server "acme-dev-archive-db" stopped and removed from .mcp.json` {
		t.Fatalf("close message = %q", message)
	}
	if strings.Contains(readFile(t, filepath.Join(cfg.ProjectDir, ".mcp.json")), "acme-dev-archive-db") {
		t.Fatal("entry must be removed on close")
	}
	if handle.Close() != message {
		t.Fatal("Close must be idempotent")
	}
}

func TestStartWithoutClientConfigPrintsToken(t *testing.T) {
	useIsolatedLocks(t)
	handle, err := Start(NewHost(), testSessionConfig(t, "sqlserver-se"))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if !strings.Contains(handle.Display, "no AI client config found; header Authorization: Bearer ") {
		t.Fatalf("display = %q", handle.Display)
	}
}

func TestStartFlagsWrites(t *testing.T) {
	useIsolatedLocks(t)
	cfg := testSessionConfig(t, "postgres")
	cfg.AllowWrites = true
	handle, err := Start(NewHost(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if !strings.HasPrefix(handle.Display, "WRITES ENABLED · MCP ") {
		t.Fatalf("display = %q", handle.Display)
	}
}

func TestCredentialProviderSelection(t *testing.T) {
	cfg := testSessionConfig(t, "postgres")
	if _, ok := credentialProvider(cfg, EnginePostgres).(*iamTokenProvider); !ok {
		t.Fatal("postgres read-only must use IAM tokens")
	}
	if _, ok := credentialProvider(cfg, EngineSQLServer).(*supportSecretsProvider); !ok {
		t.Fatal("sql server read-only must use ro-access secrets")
	}
	cfg.AllowWrites = true
	provider, ok := credentialProvider(cfg, EngineSQLServer).(*masterSecretProvider)
	if !ok || provider.defaultDatabase != "master" {
		t.Fatalf("writes must use the master secret on master, got %#v", provider)
	}
}

func TestCredentialProviderPostgresWithoutIAMAuthIsDisabled(t *testing.T) {
	cfg := testSessionConfig(t, "postgres")
	cfg.IAMAuth = false

	provider := credentialProvider(cfg, EnginePostgres)
	if _, ok := provider.(*iamDisabledProvider); !ok {
		t.Fatalf("expected an iamDisabledProvider, got %#v", provider)
	}
	if _, err := provider.Credentials(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "IAM authentication is disabled") {
		t.Fatalf("Credentials error = %v, want an IAM-disabled explanation", err)
	}
	if _, err := provider.Databases(context.Background()); err == nil || !strings.Contains(err.Error(), "IAM authentication is disabled") {
		t.Fatalf("Databases error = %v, want an IAM-disabled explanation", err)
	}
}

func TestServerName(t *testing.T) {
	cases := map[[2]string]string{
		{"dev", "archive-db"}:        "acme-dev-archive-db",
		{"ACME Prod", "Sales_DB.v2"}: "acme-acme-prod-sales-db-v2",
		{"dev", "--weird--name--"}:   "acme-dev-weird-name",
	}
	for in, want := range cases {
		if got := serverName(in[0], in[1]); got != want {
			t.Fatalf("serverName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}
