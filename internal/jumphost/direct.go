package jumphost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/ideadatasource"
)

// resourceDirect is a database reached directly, without jumphost, declared with `airlock db add`.
const resourceDirect = "direct"

// directAccount and directRegion key direct databases in the prefs and the keychain: they belong to
// no AWS account nor region.
const (
	directAccount = "local"
	directRegion  = "local"
)

// DirectDatabases finds the definitions of the direct databases (databases.json, read by the CLI).
type DirectDatabases interface {
	// Get returns the database named name; false when there is none.
	Get(name string) (directdb.Database, bool, error)
}

// DirectTargetRef identifies the direct database name in the prefs and the keychain.
func DirectTargetRef(name string) TargetRef {
	return TargetRef{Account: directAccount, Region: directRegion, Type: resourceDirect, Name: strings.TrimSpace(name)}
}

// ErrPasswordRequired reports a direct database opened with neither a typed nor a remembered password.
var ErrPasswordRequired = errors.New("password required")

// dbmcpStartDirect starts a direct database's MCP server; a package variable so tests can stub it.
var dbmcpStartDirect = dbmcp.StartDirect

// directLabel is the registry key of the session of the direct database name.
func directLabel(name string) string {
	return "DIRECT " + name
}

// DirectReadOnlyWarning warns that read-only access to db rests on its user's permissions (SQL
// Server has no read-only transaction); empty when it does not.
func DirectReadOnlyWarning(db *directdb.Database) string {
	engine, ok := dbmcp.EngineFor(string(db.Engine))
	if !ok || !dbmcp.ReadOnlyRestsOnGrants(engine) {
		return ""
	}
	return fmt.Sprintf("SQL Server: read-only access relies on the permissions of %s; prefer a read-only user", db.User)
}

// directAddress is where db is reached.
func directAddress(db *directdb.Database) dbmcp.Address {
	return dbmcp.Address{Host: db.Host, Port: db.EffectivePort(), TLS: dbmcp.TLSMode(db.EffectiveTLS())}
}

// jdbcHost is host as a JDBC URL authority: an IPv6 literal is bracketed.
func jdbcHost(host string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]"
	}
	return host
}

// jdbcTLSProperties are the JDBC properties giving the IntelliJ data source of db the TLS mode
// airlock connects with.
func jdbcTLSProperties(db *directdb.Database) map[string]string {
	tls := db.EffectiveTLS()
	if db.Engine == directdb.EngineSQLServer {
		switch tls {
		case directdb.TLSDisable:
			return map[string]string{"encrypt": "false"}
		case directdb.TLSVerify:
			return map[string]string{"encrypt": "true", "trustServerCertificate": "false"}
		default:
			return map[string]string{"encrypt": "true", "trustServerCertificate": "true"}
		}
	}
	switch tls {
	case directdb.TLSDisable:
		return map[string]string{"sslmode": "disable"}
	case directdb.TLSVerify:
		return map[string]string{"sslmode": "verify-full"}
	default:
		return map[string]string{"sslmode": "require"}
	}
}

// directDatabase returns the definition of the direct database name and its engine.
func (r *Runner) directDatabase(name string) (directdb.Database, dbmcp.Engine, error) {
	if r.databases == nil {
		return directdb.Database{}, "", errors.New("direct databases are not available")
	}
	db, found, err := r.databases.Get(name)
	if err != nil {
		return directdb.Database{}, "", err
	}
	if !found {
		return directdb.Database{}, "", directdb.NotFoundError(name)
	}
	engine, ok := dbmcp.EngineFor(string(db.Engine))
	if !ok {
		return directdb.Database{}, "", fmt.Errorf("%w: %s", dbmcp.ErrEngineUnsupported, db.Engine)
	}
	return db, engine, nil
}

// CheckDirectSignIn signs in to the direct database name with password and runs SELECT 1; a refused
// password comes back wrapped in ErrSignInRejected, an unreachable host or untrusted certificate
// with its hint.
func (r *Runner) CheckDirectSignIn(ctx context.Context, name, password string) error {
	name = strings.TrimSpace(name)
	db, engine, err := r.directDatabase(name)
	if err != nil {
		return err
	}
	creds := dbmcp.PasswordCredentials(db.User, password, dbmcp.DefaultDatabase(string(db.Engine), db.Database))
	return checkDirectSignIn(ctx, engine, creds, directAddress(&db))
}

// prepareDirect opens the direct database of options: no AWS step, no process, no local port. Its
// password is options.DBPassword, else the one remembered in the keychain. The profile, region,
// secret and local port of options do not apply and are ignored.
func (r *Runner) prepareDirect(ctx context.Context, options *Options) (*Session, error) {
	name := strings.TrimSpace(options.Name)
	db, engine, err := r.directDatabase(name)
	if err != nil {
		return nil, err
	}
	password, fromKeychain := options.DBPassword, false
	if password == "" {
		lookup := Options{ResourceType: resourceDirect, Name: name, DBUser: db.User}
		if password, fromKeychain = r.LoadKeychainPassword(ctx, lookup); !fromKeychain {
			return nil, fmt.Errorf("%w for %s", ErrPasswordRequired, name)
		}
	}

	label := directLabel(name)
	if err := r.registry.claim(label, name); err != nil {
		return nil, err
	}

	// A remembered password the database rejects is forgotten, so the next connection asks again.
	account := ""
	if (options.RememberPassword || fromKeychain) && r.KeychainAvailable() {
		account = keychainAccount(DirectTargetRef(name), db.User)
	}
	addr := directAddress(&db)
	session := &Session{
		Label:        label,
		ResourceType: resourceDirect,
		Target:       name,
		Production:   db.Production,
		Details: SessionDetails{
			ConnectedServer: addr.String(),
			RestartCommand:  "airlock direct " + name,
			Auth:            "password · " + db.User,
		},
		Intro: []string{
			fmt.Sprintf("Direct database %s (%s) at %s", name, db.Engine, addr),
			"TLS: " + string(db.EffectiveTLS()),
		},
	}

	var removeDataSource func() string
	if !options.SkipIDEDataSource {
		session.Details.IDEDataSource, removeDataSource = r.registerDataSource(&ideadatasource.DataSource{
			Key:        resourceDirect + "|" + name,
			Name:       "airlock " + name,
			Engine:     string(db.Engine),
			Host:       jdbcHost(db.Host),
			Port:       db.EffectivePort(),
			Database:   db.Database,
			User:       db.User,
			Properties: jdbcTLSProperties(&db),
		}, addr.String())
		if session.Details.IDEDataSource != "" {
			session.Intro = append(session.Intro, session.Details.IDEDataSource)
		}
	}
	if !options.SkipMCP {
		handle, display := r.startDirectMCP(&db, engine, name, password, account, options.MCPAllowWrites)
		r.attachMCP(session, handle, display)
	}
	session.SignIn = &directSignIn{
		forget:   r.forgetKeychainFunc(account, password),
		engine:   engine,
		user:     db.User,
		password: password,
		database: dbmcp.DefaultDatabase(string(db.Engine), db.Database),
		addr:     addr,
	}
	session.release = sessionRelease(func() { r.registry.release(label) }, session.mcp, removeDataSource)
	return session, nil
}

// startDirectMCP serves the MCP server of a direct database and returns its display line ("not
// started: …" on failure, which never fails the session).
func (r *Runner) startDirectMCP(db *directdb.Database, engine dbmcp.Engine, name, password, account string, allowWrites bool) (handle *dbmcp.Handle, display string) {
	projectDir, err := r.mcpProjectDir()
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	handle, err = dbmcpStartDirect(r.mcpHost, &dbmcp.DirectConfig{
		OnPasswordRejected: r.forgetKeychainFunc(account, password),
		Address:            directAddress(db),
		ProjectDir:         projectDir,
		ServerPrefix:       r.mcpServerPrefix,
		Name:               name,
		DBName:             db.Database,
		User:               db.User,
		Password:           password,
		LogDir:             r.mcpLogDir,
		Engine:             engine,
		AllowWrites:        allowWrites,
	})
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	if handle == nil {
		return nil, ""
	}
	display = handle.Display()
	return handle, display
}

// serveDirect prints the intro of a direct session and keeps it open until ctx ends or the user
// presses Ctrl+C; the caller releases it.
func (r *Runner) serveDirect(ctx context.Context, session *Session) error {
	for _, line := range session.Intro {
		fprintf(r.out, "> %s\n", line)
	}
	fprintf(r.out, "Ctrl+C to stop.\n")
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	return nil
}
