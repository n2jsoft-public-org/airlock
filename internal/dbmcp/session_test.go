package dbmcp

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft/airlock/internal/awstag"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

func testSessionConfig(t *testing.T, engine string) *SessionConfig {
	t.Helper()
	return &SessionConfig{
		AWS:        aws.Config{Region: "eu-west-3"},
		ProjectDir: t.TempDir(),
		Profile:    "dev", Region: "eu-west-3", Target: "Archive_DB",
		AWSEngine: engine, RDSHost: "archive.rds.amazonaws.com", RDSPort: 5432,
		DBUser: "acme_ro", IAMAuth: true, LocalPort: 15432,
		ServerPrefix: "acme", ReadOnlySecretTag: awstag.Tag{Key: "AccessRole", Value: "ro-access"},
	}
}

func TestStartSkipsUnsupportedEngine(t *testing.T) {
	handle, err := Start(mcpserve.NewHost(), testSessionConfig(t, "mysql"))
	if err != nil || handle != nil {
		t.Fatalf("handle = %v, err = %v; want nil, nil", handle, err)
	}
}

func TestStartRegistersAndCloseCleansUp(t *testing.T) {
	useIsolatedLocks(t)
	cfg := testSessionConfig(t, "postgres")
	if err := os.WriteFile(filepath.Join(cfg.ProjectDir, ".mcp.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := mcpserve.NewHost()

	handle, err := Start(host, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(handle.Display(), "read-only · acme-dev-archive-db -> http://127.0.0.1:") || !strings.HasSuffix(handle.Display(), "(.mcp.json)") {
		t.Fatalf("display = %q", handle.Display())
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

func TestHandleReportsWriteAccess(t *testing.T) {
	useIsolatedLocks(t)
	for _, writes := range []bool{false, true} {
		cfg := testSessionConfig(t, "postgres")
		cfg.AllowWrites = writes
		handle, err := Start(mcpserve.NewHost(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if handle.AllowsWrites() != writes {
			t.Fatalf("AllowsWrites() = %v, want %v", handle.AllowsWrites(), writes)
		}
		handle.Close()
	}
}

func TestStartWithoutClientConfigPrintsToken(t *testing.T) {
	useIsolatedLocks(t)
	handle, err := Start(mcpserve.NewHost(), testSessionConfig(t, "sqlserver-se"))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if !strings.Contains(handle.Display(), "no AI client config written; header Authorization: Bearer ") {
		t.Fatalf("display = %q", handle.Display())
	}
}

func TestStartFlagsWrites(t *testing.T) {
	useIsolatedLocks(t)
	cfg := testSessionConfig(t, "postgres")
	cfg.AllowWrites = true
	handle, err := Start(mcpserve.NewHost(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if !strings.HasPrefix(handle.Display(), "WRITES ENABLED · acme-dev-archive-db -> ") {
		t.Fatalf("display = %q", handle.Display())
	}
}

func TestCredentialProviderSelection(t *testing.T) {
	cfg := testSessionConfig(t, "postgres")
	if _, ok := credentialProvider(cfg, EnginePostgres).(*iamTokenProvider); !ok {
		t.Fatal("postgres read-only must use IAM tokens")
	}
	if _, ok := credentialProvider(cfg, EngineSQLServer).(*readOnlySecretsProvider); !ok {
		t.Fatal("sql server read-only must use the read-only secrets")
	}
	cfg.AllowWrites = true
	iam, ok := credentialProvider(cfg, EnginePostgres).(*iamTokenProvider)
	if !ok || iam.user != cfg.DBUser {
		t.Fatalf("postgres writes must sign in as the chosen user with IAM tokens, got %#v", credentialProvider(cfg, EnginePostgres))
	}
	provider, ok := credentialProvider(cfg, EngineSQLServer).(*masterSecretProvider)
	if !ok || provider.defaultDatabase != "master" {
		t.Fatalf("sql server writes must use the master secret on master, got %#v", provider)
	}
}

func TestCredentialProviderPostgresWritesWithoutIAMAuthNeverReadsMasterSecret(t *testing.T) {
	cfg := testSessionConfig(t, "postgres")
	cfg.IAMAuth, cfg.AllowWrites = false, true

	if _, ok := credentialProvider(cfg, EnginePostgres).(*iamDisabledProvider); !ok {
		t.Fatalf("provider = %T, want an iamDisabledProvider", credentialProvider(cfg, EnginePostgres))
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

func TestStartRefusesReadOnlySQLServerWithoutSecretTag(t *testing.T) {
	useIsolatedLocks(t)
	cfg := testSessionConfig(t, "sqlserver-se")
	cfg.ReadOnlySecretTag = awstag.Tag{}
	handle, err := Start(mcpserve.NewHost(), cfg)
	if !errors.Is(err, ErrReadOnlySecretTagUnset) || handle != nil {
		t.Fatalf("Start = %v, %v; want ErrReadOnlySecretTagUnset", handle, err)
	}

	cfg.AllowWrites = true
	cfg.MasterSecretARN = "arn:aws:secretsmanager:eu-west-3:1:secret:master"
	handle, err = Start(mcpserve.NewHost(), cfg)
	if err != nil || handle == nil {
		t.Fatalf("writes mode does not need the read-only tag: %v, %v", handle, err)
	}
	handle.Close()
}

func connectEndpoint(t *testing.T, endpoint mcpserve.Endpoint) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   endpoint.URL,
		HTTPClient: &http.Client{Transport: bearerTransport{token: endpoint.Token}, Timeout: 10 * time.Second},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() }) //nolint:errcheck // the test may have stopped the server already.
	return session
}

func TestStartRecordsToolCallsForSubscribersAndAuditLog(t *testing.T) {
	useIsolatedLocks(t)
	cfg := testSessionConfig(t, "postgres")
	cfg.LogDir = filepath.Join(t.TempDir(), "logs")
	handle, err := Start(mcpserve.NewHost(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if handle.QueryLogDir() != cfg.LogDir {
		t.Fatalf("QueryLogDir() = %q, want %q", handle.QueryLogDir(), cfg.LogDir)
	}

	events := make(chan mcpserve.ToolEvent, 1)
	handle.Subscribe(func(e mcpserve.ToolEvent) { events <- e })
	// The read-only guard rejects the statement before any connection is attempted.
	if result := call(t, connectEndpoint(t, handle.Endpoint()), "query", map[string]any{"sql": "DELETE FROM users"}); !result.IsError {
		t.Fatalf("DELETE must be rejected, got %s", resultText(result))
	}

	var e mcpserve.ToolEvent
	select {
	case e = <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("no query event recorded")
	}
	if e.Server != "acme-dev-archive-db" || e.Target != "Archive_DB" || e.Access != "read-only" || e.Tool != "query" || e.SQL != "DELETE FROM users" || e.Error == "" {
		t.Fatalf("event = %+v", e)
	}
	if e.LogError != "" {
		t.Fatalf("audit log write failed: %s", e.LogError)
	}
	files, err := filepath.Glob(filepath.Join(cfg.LogDir, "mcp-queries-*.jsonl"))
	if err != nil || len(files) != 1 || !strings.Contains(readFile(t, files[0]), `"sql":"DELETE FROM users"`) {
		t.Fatalf("audit log files = %v, err = %v", files, err)
	}
}

func TestCredentialProviderPrefersThePasswordSecret(t *testing.T) {
	cfg := testSessionConfig(t, "postgres")
	cfg.IAMAuth, cfg.DBSecretARN, cfg.DBPassword = false, "arn:app-password", "ignored"
	if _, ok := credentialProvider(cfg, EnginePostgres).(*secretPasswordProvider); !ok {
		t.Fatalf("provider = %T, want the Secrets Manager password", credentialProvider(cfg, EnginePostgres))
	}
	cfg.AllowWrites = true
	if _, ok := credentialProvider(cfg, EngineSQLServer).(*secretPasswordProvider); !ok {
		t.Fatalf("sql server writes provider = %T, want the Secrets Manager password, not the master secret", credentialProvider(cfg, EngineSQLServer))
	}
}

func TestStartAcceptsReadOnlySQLServerWithPasswordSecret(t *testing.T) {
	useIsolatedLocks(t)
	cfg := testSessionConfig(t, "sqlserver-se")
	cfg.ReadOnlySecretTag, cfg.DBSecretARN = awstag.Tag{}, "arn:app-password"
	handle, err := Start(mcpserve.NewHost(), cfg)
	if err != nil {
		t.Fatalf("Start = %v, want the password secret accepted without the read-only tag", err)
	}
	if handle != nil {
		handle.Close()
	}
}

func TestCredentialProviderWiresPasswordRejection(t *testing.T) {
	called := false
	cfg := &SessionConfig{Target: "archive", DBUser: "app_user", DBPassword: "s3cret", OnPasswordRejected: func() { called = true }}
	provider, ok := credentialProvider(cfg, EnginePostgres).(*passwordProvider)
	if !ok {
		t.Fatalf("provider = %T", credentialProvider(cfg, EnginePostgres))
	}
	provider.rejected()
	if !called {
		t.Fatal("OnPasswordRejected was not wired to the password provider")
	}
}
