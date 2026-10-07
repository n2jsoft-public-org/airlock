package jumphost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/keychain"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

const billingKeychainAccount = "local/local/direct/legacy-billing/mcp_ro"

// billingDatabase is a SQL Server database reached directly, flagged production.
func billingDatabase() directdb.Database {
	return directdb.Database{Name: "legacy-billing", Engine: directdb.EngineSQLServer, Host: "billing.db.internal", User: "mcp_ro", Environment: "prod"}
}

// fakeDirectDatabases is a DirectDatabases over a map; a non-nil err fails every lookup.
type fakeDirectDatabases struct {
	err error
	dbs map[string]directdb.Database
}

func directDatabases(dbs ...directdb.Database) fakeDirectDatabases {
	byName := map[string]directdb.Database{}
	for _, db := range dbs {
		byName[db.Name] = db
	}
	return fakeDirectDatabases{dbs: byName}
}

func (f fakeDirectDatabases) Get(name string) (directdb.Database, bool, error) {
	if f.err != nil {
		return directdb.Database{}, false, f.err
	}
	db, ok := f.dbs[name]
	return db, ok, nil
}

// directKeychainRunner remembers prefs and passwords in one saved-connections.json holding dbs; no
// AWS CLI is reachable, so any AWS lookup fails.
func directKeychainRunner(t *testing.T, dbs ...directdb.Database) (*Runner, *keychain.MemoryStore) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	path := filepath.Join(t.TempDir(), "saved-connections.json")
	saved := connections.NewStore(path).Databases()
	for _, db := range dbs {
		if err := saved.Put("", db); err != nil {
			t.Fatal(err)
		}
	}
	store := keychain.NewMemoryStore()
	runner := NewRunner(&RunnerOptions{PrefsPath: path, Keychain: store, Databases: saved})
	return runner, store
}

func directOptions(password string, remember bool) Options {
	return Options{ResourceType: "direct", Name: "legacy-billing", DBUser: "mcp_ro", DBPassword: password, RememberPassword: remember}
}

func TestDirectTargetRef(t *testing.T) {
	ref := DirectTargetRef(" legacy-billing ")
	if ref != (TargetRef{Account: "local", Region: "local", Type: "direct", Name: "legacy-billing"}) || ref.key() != "local/local/direct/legacy-billing" {
		t.Fatalf("ref = %+v (key %q)", ref, ref.key())
	}
	if got := keychainAccount(ref, " mcp_ro "); got != billingKeychainAccount {
		t.Fatalf("keychainAccount = %q", got)
	}
}

func TestSaveConnectionPrefsKeysDirectDatabasesLocally(t *testing.T) {
	runner, store := directKeychainRunner(t, billingDatabase())
	options := directOptions("s3cret", true)
	options.MCPAllowWrites = true
	if err := runner.SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}

	if got, err := store.Get(billingKeychainAccount); err != nil || got != "s3cret" {
		t.Fatalf("keychain = %q, %v; want the password", got, err)
	}
	prefs, ok := runner.LoadConnectionPrefs(context.Background(), directOptions("", false))
	if !ok || prefs.Profile != "" || prefs.ResourceType != "direct" || prefs.DBUser != "mcp_ro" ||
		prefs.SignIn != SignInPassword || !prefs.MCPAllowWrites || !prefs.PasswordInKeychain || prefs.LocalPort != 0 {
		t.Fatalf("prefs = %+v, %v", prefs, ok)
	}
	if got, ok := runner.LoadKeychainPassword(context.Background(), directOptions("", false)); !ok || got != "s3cret" {
		t.Fatalf("LoadKeychainPassword = %q, %v", got, ok)
	}
}

func TestSaveConnectionPrefsKeepsTheRememberedDirectPassword(t *testing.T) {
	runner, store := directKeychainRunner(t, billingDatabase())
	mustSave(t, runner, new(directOptions("s3cret", true)))
	// Opened again with the remembered password: nothing typed.
	mustSave(t, runner, new(directOptions("", false)))

	if got, err := store.Get(billingKeychainAccount); err != nil || got != "s3cret" {
		t.Fatalf("keychain = %q, %v; want the password kept", got, err)
	}
	if prefs, _ := runner.LoadConnectionPrefs(context.Background(), directOptions("", false)); !prefs.PasswordInKeychain {
		t.Fatalf("prefs = %+v, want PasswordInKeychain kept", prefs)
	}
}

func TestForgetConnectionForgetsTheDirectPassword(t *testing.T) {
	runner, store := directKeychainRunner(t, billingDatabase())
	mustSave(t, runner, new(directOptions("s3cret", true)))

	if err := runner.ForgetConnection(context.Background(), DirectTargetRef("legacy-billing")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(billingKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry still there: %v", err)
	}
	if _, ok := runner.LoadConnectionPrefs(context.Background(), directOptions("", false)); ok {
		t.Fatal("prefs still there")
	}
}

// directRunner opens direct databases in a temporary project; no AWS CLI is reachable.
func directRunner(t *testing.T, opts *RunnerOptions) *Runner {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	if opts.ProjectDir == "" {
		opts.ProjectDir = t.TempDir()
	}
	if opts.Databases == nil {
		opts.Databases = directDatabases(billingDatabase())
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	opts.Err, opts.MCPHost, opts.MCPServerPrefix = io.Discard, mcpserve.NewHost(), "acme"
	return NewRunner(opts)
}

// stubDirectMCP records the config of the direct MCP servers instead of starting them.
func stubDirectMCP(t *testing.T) *[]*dbmcp.DirectConfig {
	t.Helper()
	var started []*dbmcp.DirectConfig
	previous := dbmcpStartDirect
	dbmcpStartDirect = func(_ *mcpserve.Host, cfg *dbmcp.DirectConfig) (*dbmcp.Handle, error) {
		started = append(started, cfg)
		return nil, nil
	}
	t.Cleanup(func() { dbmcpStartDirect = previous })
	return &started
}

func TestPrepareDirectNeedsNoAWS(t *testing.T) {
	started := stubDirectMCP(t)
	runner := directRunner(t, &RunnerOptions{})

	session, err := runner.Prepare(context.Background(), Options{ResourceType: "DIRECT", Name: "legacy-billing", DBPassword: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if session.Label != "DIRECT legacy-billing" || session.ResourceType != "direct" || session.Target != "legacy-billing" ||
		session.Profile != "" || session.Region != "" || !session.Production || session.Environment != "prod" || session.LocalPort != 0 ||
		len(session.CommandArgs) != 0 || len(session.AwsArgs) != 0 {
		t.Fatalf("session = %+v", session)
	}
	details := session.Details
	if details.ConnectedServer != "billing.db.internal:1433" || details.RestartCommand != "airlock direct legacy-billing" ||
		details.Auth != "password · mcp_ro" || details.TokenCommand != "" {
		t.Fatalf("details = %+v", details)
	}
	if len(session.Intro) < 2 || session.Intro[0] != "Direct database legacy-billing (sqlserver) at billing.db.internal:1433" || session.Intro[1] != "TLS: require" {
		t.Fatalf("intro = %q", session.Intro)
	}
	if strings.Contains(fmt.Sprintf("%+v %q", details, session.Intro), "s3cret") {
		t.Fatal("the password must never be displayed")
	}

	if len(*started) != 1 {
		t.Fatalf("started %d MCP servers", len(*started))
	}
	cfg := (*started)[0]
	if cfg.Name != "legacy-billing" || cfg.Engine != dbmcp.EngineSQLServer || cfg.User != "mcp_ro" || cfg.Password != "s3cret" ||
		cfg.ServerPrefix != "acme" || cfg.AllowWrites || cfg.OnPasswordRejected != nil ||
		cfg.Address != (dbmcp.Address{Host: "billing.db.internal", Port: 1433, TLS: dbmcp.TLSRequire}) {
		t.Fatalf("MCP config = %+v", cfg)
	}
	if session.SignIn == nil {
		t.Fatal("a direct session checks its password")
	}
}

func TestPrepareDirectSkipsTheMCPServerWhenAsked(t *testing.T) {
	started := stubDirectMCP(t)
	session, err := directRunner(t, &RunnerOptions{}).Prepare(context.Background(), Options{ResourceType: "direct", Name: "legacy-billing", DBPassword: "s3cret", SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if len(*started) != 0 || session.MCPServer() != nil {
		t.Fatalf("started = %d, server = %v; want none", len(*started), session.MCPServer())
	}
}

func TestPrepareDirectRefusesASecondSession(t *testing.T) {
	stubDirectMCP(t)
	runner := directRunner(t, &RunnerOptions{})
	options := Options{ResourceType: "direct", Name: "legacy-billing", DBPassword: "s3cret"}

	first, err := runner.Prepare(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, secondErr := runner.Prepare(context.Background(), options); secondErr == nil || secondErr.Error() != "legacy-billing is already connected" {
		t.Fatalf("second Prepare = %v", secondErr)
	}
	first.Release()
	again, err := runner.Prepare(context.Background(), options)
	if err != nil {
		t.Fatalf("Prepare after Release = %v", err)
	}
	again.Release()
}

func TestPrepareDirectUnknownDatabase(t *testing.T) {
	runner := directRunner(t, &RunnerOptions{})
	_, err := runner.Prepare(context.Background(), Options{ResourceType: "direct", Name: "nope", DBPassword: "s3cret"})
	if err == nil || err.Error() != "no direct database named nope (see airlock connection list)" {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareDirectRequiresAPassword(t *testing.T) {
	runner := directRunner(t, &RunnerOptions{PrefsPath: filepath.Join(t.TempDir(), "saved-connections.json"), Keychain: keychain.NewMemoryStore()})
	_, err := runner.Prepare(context.Background(), Options{ResourceType: "direct", Name: "legacy-billing"})
	if !errors.Is(err, ErrPasswordRequired) || err.Error() != "password required for legacy-billing" {
		t.Fatalf("err = %v, want ErrPasswordRequired", err)
	}
	// The failed preparation holds nothing.
	if err := runner.registry.claim("DIRECT legacy-billing", "legacy-billing"); err != nil {
		t.Fatalf("claim after a failed Prepare = %v", err)
	}
}

// rememberedDirectRunner is a directRunner whose saved-connections.json holds the billing database,
// its password remembered in store as an opening with Remember leaves it.
func rememberedDirectRunner(t *testing.T, store *keychain.MemoryStore, password string) *Runner {
	t.Helper()
	path := filepath.Join(t.TempDir(), "saved-connections.json")
	saved := connections.NewStore(path).Databases()
	if err := saved.Put("", billingDatabase()); err != nil {
		t.Fatal(err)
	}
	runner := directRunner(t, &RunnerOptions{PrefsPath: path, Keychain: store, Databases: saved})
	mustSave(t, runner, new(directOptions(password, true)))
	return runner
}

func TestPrepareDirectIgnoresAKeychainPasswordTheChoicesDoNotFlag(t *testing.T) {
	stubDirectMCP(t)
	store := keychain.NewMemoryStore()
	path := filepath.Join(t.TempDir(), "saved-connections.json")
	saved := connections.NewStore(path).Databases()
	if err := saved.Put("", billingDatabase()); err != nil {
		t.Fatal(err)
	}
	runner := directRunner(t, &RunnerOptions{PrefsPath: path, Keychain: store, Databases: saved})
	// Left by an entry of the same name and user the file no longer flags (another server, maybe).
	if err := store.Set(billingKeychainAccount, "stale"); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"never opened", "opened without remembering"} {
		if step == "opened without remembering" {
			mustSave(t, runner, new(directOptions("", false)))
		}
		if got, ok := runner.LoadKeychainPassword(context.Background(), directOptions("", false)); ok || got != "" {
			t.Fatalf("%s: LoadKeychainPassword = %q, %v; want nothing unflagged", step, got, ok)
		}
		if _, err := runner.Prepare(context.Background(), Options{ResourceType: "direct", Name: "legacy-billing"}); !errors.Is(err, ErrPasswordRequired) {
			t.Fatalf("%s: Prepare = %v, want ErrPasswordRequired rather than the unflagged keychain password", step, err)
		}
	}
}

func TestPrepareDirectUsesTheKeychainPasswordAndForgetsItOnceRejected(t *testing.T) {
	started := stubDirectMCP(t)
	store := keychain.NewMemoryStore()
	runner := rememberedDirectRunner(t, store, "kept")

	session, err := runner.Prepare(context.Background(), Options{ResourceType: "direct", Name: "legacy-billing"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	cfg := (*started)[0]
	if cfg.Password != "kept" || cfg.OnPasswordRejected == nil {
		t.Fatalf("MCP config = %+v, want the keychain password and its forgetting", cfg)
	}
	cfg.OnPasswordRejected()
	if _, err := store.Get(billingKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("rejected keychain password kept: %v", err)
	}
}

func TestPrepareDirectRegistersTheIDEDataSourceOnTheRealHost(t *testing.T) {
	stubDirectMCP(t)
	projectDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectDir, ".idea"), 0o750); err != nil {
		t.Fatal(err)
	}
	runner := directRunner(t, &RunnerOptions{ProjectDir: projectDir})

	session, err := runner.Prepare(context.Background(), Options{ResourceType: "direct", Name: "legacy-billing", DBPassword: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if session.Details.IDEDataSource != `IntelliJ data source "airlock legacy-billing" -> billing.db.internal:1433 (.idea/dataSources.xml)` {
		t.Fatalf("IDE data source = %q", session.Details.IDEDataSource)
	}
	xml, err := os.ReadFile(filepath.Join(projectDir, ".idea", "dataSources.xml")) // #nosec G304 -- test file.
	if err != nil || !strings.Contains(string(xml), "billing.db.internal") || strings.Contains(string(xml), "s3cret") {
		t.Fatalf("dataSources.xml = %s, %v", xml, err)
	}
	if message := session.Release(); !strings.Contains(message, `IntelliJ data source "airlock legacy-billing" removed from .idea/dataSources.xml`) {
		t.Fatalf("release message = %q", message)
	}
}

func TestDirectSignInChecksThePasswordAndForgetsARejectedOne(t *testing.T) {
	stubDirectMCP(t)
	store := keychain.NewMemoryStore()
	runner := rememberedDirectRunner(t, store, "kept")
	var checked dbmcp.Address
	var creds dbmcp.Credentials
	previous := checkDirectSignIn
	checkDirectSignIn = func(ctx context.Context, engine dbmcp.Engine, provider dbmcp.CredentialProvider, addr dbmcp.Address) error {
		checked = addr
		var err error
		if creds, err = provider.Credentials(ctx, ""); err != nil {
			return err
		}
		return fmt.Errorf("%w: login failed", dbmcp.ErrSignInRejected)
	}
	t.Cleanup(func() { checkDirectSignIn = previous })

	session, err := runner.Prepare(context.Background(), Options{ResourceType: "direct", Name: "legacy-billing"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	candidates, err := session.SignIn.Candidates(context.Background())
	if err != nil || len(candidates) != 1 || candidates[0].Label != "password" || candidates[0].User != "mcp_ro" {
		t.Fatalf("candidates = %+v, %v", candidates, err)
	}
	if err := session.SignIn.Check(context.Background(), candidates[0]); !errors.Is(err, ErrSignInRejected) {
		t.Fatalf("Check = %v, want ErrSignInRejected", err)
	}
	if checked.String() != "billing.db.internal:1433" || creds.User != "mcp_ro" || creds.Password != "kept" || creds.Database != "" {
		t.Fatalf("checked %s as %+v", checked, creds)
	}
	if _, err := store.Get(billingKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("rejected keychain password kept: %v", err)
	}
	options := Options{}
	if session.SignIn.Forget(candidates[0], &options) || options != (Options{}) {
		t.Fatalf("Forget changed %+v", options)
	}
}

func TestRunDirectReleasesOnCancel(t *testing.T) {
	t.Cleanup(mcpserve.IsolateLocks(t.TempDir()))
	projectDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectDir, ".idea"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".mcp.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	runner := directRunner(t, &RunnerOptions{ProjectDir: projectDir, Out: &out})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Ctrl+C: the context ends.

	if err := runner.Run(ctx, &Options{ResourceType: "direct", Name: "legacy-billing", DBPassword: "s3cret"}); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{
		"> Direct database legacy-billing (sqlserver) at billing.db.internal:1433",
		`MCP server "acme-legacy-billing" stopped and removed from .mcp.json`,
		`IntelliJ data source "airlock legacy-billing" removed from .idea/dataSources.xml`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output misses %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "SSM") {
		t.Fatalf("a direct session starts no SSM session:\n%s", output)
	}
	mcpJSON, err := os.ReadFile(filepath.Join(projectDir, ".mcp.json")) // #nosec G304 -- test file.
	if err != nil || strings.Contains(string(mcpJSON), "acme-legacy-billing") {
		t.Fatalf(".mcp.json = %s, %v; want the entry removed", mcpJSON, err)
	}
	if err := runner.registry.claim("DIRECT legacy-billing", "legacy-billing"); err != nil {
		t.Fatalf("the session is still registered: %v", err)
	}
}

func TestRunnerCheckDirectSignInUsesTheDefinition(t *testing.T) {
	analytics := directdb.Database{Name: "analytics", Engine: directdb.EnginePostgres, Host: "fd00::12", User: "reader", TLS: directdb.TLSVerify}
	runner := directRunner(t, &RunnerOptions{Databases: directDatabases(analytics)})
	var engine dbmcp.Engine
	var addr dbmcp.Address
	var creds dbmcp.Credentials
	previous := checkDirectSignIn
	checkDirectSignIn = func(ctx context.Context, e dbmcp.Engine, provider dbmcp.CredentialProvider, a dbmcp.Address) error {
		engine, addr = e, a
		var err error
		creds, err = provider.Credentials(ctx, "")
		return err
	}
	t.Cleanup(func() { checkDirectSignIn = previous })

	if err := runner.CheckDirectSignIn(context.Background(), "analytics", "pw"); err != nil {
		t.Fatal(err)
	}
	if engine != dbmcp.EnginePostgres || addr.String() != "[fd00::12]:5432" || addr.TLS != dbmcp.TLSVerify ||
		creds != (dbmcp.Credentials{User: "reader", Password: "pw", Database: "postgres"}) {
		t.Fatalf("checked %s %s (%s) as %+v", engine, addr, addr.TLS, creds)
	}
	if err := runner.CheckDirectSignIn(context.Background(), "nope", "pw"); err == nil || !strings.Contains(err.Error(), "no direct database named nope") {
		t.Fatalf("unknown database = %v", err)
	}
}

func TestDirectReadOnlyWarning(t *testing.T) {
	if got := DirectReadOnlyWarning(new(billingDatabase())); got != "SQL Server: read-only access relies on the permissions of mcp_ro; prefer a read-only user" {
		t.Fatalf("SQL Server warning = %q", got)
	}
	analytics := directdb.Database{Name: "analytics", Engine: directdb.EnginePostgres, Host: "pg", User: "reader"}
	if got := DirectReadOnlyWarning(&analytics); got != "" {
		t.Fatalf("PostgreSQL warning = %q, want none", got)
	}
}

func TestPrepareDirectBracketsIPv6HostsInTheIDEDataSource(t *testing.T) {
	stubDirectMCP(t)
	projectDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectDir, ".idea"), 0o750); err != nil {
		t.Fatal(err)
	}
	analytics := directdb.Database{Name: "analytics", Engine: directdb.EnginePostgres, Host: "fd00::12", User: "reader"}
	runner := directRunner(t, &RunnerOptions{ProjectDir: projectDir, Databases: directDatabases(analytics)})

	session, err := runner.Prepare(context.Background(), Options{ResourceType: "direct", Name: "analytics", DBPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if !strings.Contains(session.Details.IDEDataSource, "-> [fd00::12]:5432") {
		t.Fatalf("IDE data source = %q", session.Details.IDEDataSource)
	}
	xml, err := os.ReadFile(filepath.Join(projectDir, ".idea", "dataSources.xml")) // #nosec G304 -- test file.
	if err != nil || !strings.Contains(string(xml), "jdbc:postgresql://[fd00::12]:5432/") {
		t.Fatalf("dataSources.xml = %s, %v", xml, err)
	}
}

func TestPrepareDirectCarriesTLSToTheIDEDataSource(t *testing.T) {
	for _, tc := range []struct {
		engine directdb.Engine
		tls    directdb.TLSMode
		want   []string
	}{
		{engine: directdb.EnginePostgres, tls: "", want: []string{`<property name="sslmode" value="require"/>`}},
		{engine: directdb.EnginePostgres, tls: directdb.TLSVerify, want: []string{`<property name="sslmode" value="verify-full"/>`}},
		{engine: directdb.EnginePostgres, tls: directdb.TLSDisable, want: []string{`<property name="sslmode" value="disable"/>`}},
		{engine: directdb.EngineSQLServer, tls: directdb.TLSRequire, want: []string{
			`<property name="encrypt" value="true"/>`, `<property name="trustServerCertificate" value="true"/>`,
		}},
		{engine: directdb.EngineSQLServer, tls: directdb.TLSVerify, want: []string{
			`<property name="encrypt" value="true"/>`, `<property name="trustServerCertificate" value="false"/>`,
		}},
		{engine: directdb.EngineSQLServer, tls: directdb.TLSDisable, want: []string{`<property name="encrypt" value="false"/>`}},
	} {
		t.Run(string(tc.engine)+"/"+string(tc.tls), func(t *testing.T) {
			stubDirectMCP(t)
			projectDir := t.TempDir()
			if err := os.Mkdir(filepath.Join(projectDir, ".idea"), 0o750); err != nil {
				t.Fatal(err)
			}
			db := directdb.Database{Name: "analytics", Engine: tc.engine, Host: "db.internal", User: "reader", TLS: tc.tls}
			runner := directRunner(t, &RunnerOptions{ProjectDir: projectDir, Databases: directDatabases(db)})

			session, err := runner.Prepare(context.Background(), Options{ResourceType: "direct", Name: "analytics", DBPassword: "pw"})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Release()
			xml, err := os.ReadFile(filepath.Join(projectDir, ".idea", "dataSources.xml")) // #nosec G304 -- test file.
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(string(xml), want) {
					t.Errorf("dataSources.xml lacks %s:\n%s", want, xml)
				}
			}
		})
	}
}

func TestForgetDirectPasswordDeletesThePasswordOfTheNamedUser(t *testing.T) {
	store := keychain.NewMemoryStore()
	runner := NewRunner(&RunnerOptions{PrefsPath: filepath.Join(t.TempDir(), "saved-connections.json"), Keychain: store})
	for _, flagged := range []bool{true, false} {
		if err := store.Set(billingKeychainAccount, "s3cret"); err != nil {
			t.Fatal(err)
		}
		if err := runner.ForgetDirectPassword(" legacy-billing ", " mcp_ro ", flagged); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(billingKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
			t.Fatalf("flagged %v: keychain entry still there: %v", flagged, err)
		}
	}
	if err := runner.ForgetDirectPassword("legacy-billing", "mcp_ro", true); err != nil {
		t.Fatalf("a missing password is not an error: %v", err)
	}
	if err := runner.ForgetDirectPassword("legacy-billing", " ", true); err != nil {
		t.Fatalf("no user, nothing to forget: %v", err)
	}

	locked := NewRunner(&RunnerOptions{PrefsPath: filepath.Join(t.TempDir(), "saved-connections.json"), Keychain: undeletableKeychain{store}})
	if err := locked.ForgetDirectPassword("legacy-billing", "mcp_ro", true); !errors.Is(err, ErrKeychain) {
		t.Fatalf("err = %v, want ErrKeychain", err)
	}
}
