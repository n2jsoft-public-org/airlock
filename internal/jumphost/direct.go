package jumphost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/ideadatasource"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// resourceDirect is a database reached directly, without jumphost, declared with `airlock connection add`.
const resourceDirect = "direct"

// directAccount and directRegion key direct databases in the prefs and the keychain: they belong to
// no AWS account nor region.
const (
	directAccount = "local"
	directRegion  = "local"
)

// DirectDatabases finds the definitions of the direct databases (saved-connections.json, opened by the CLI).
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
	switch db.Engine {
	case directdb.EngineMySQL:
		// MySQL Connector/J.
		switch tls {
		case directdb.TLSDisable:
			return map[string]string{"sslMode": "DISABLED"}
		case directdb.TLSVerify:
			return map[string]string{"sslMode": "VERIFY_IDENTITY"}
		default:
			return map[string]string{"sslMode": "REQUIRED"}
		}
	case directdb.EngineMariaDB:
		// MariaDB Connector/J 3.
		switch tls {
		case directdb.TLSDisable:
			return map[string]string{"sslMode": "disable"}
		case directdb.TLSVerify:
			return map[string]string{"sslMode": "verify-full"}
		default:
			return map[string]string{"sslMode": "trust"}
		}
	}
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
		return directdb.Database{}, "", i18n.NewError("jumphost.direct.direct_databases_are_not")
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

	// A remembered password the database rejects is forgotten, so the next connection asks again.
	account := ""
	if (options.RememberPassword || fromKeychain) && r.KeychainAvailable() {
		account = keychainAccount(DirectTargetRef(name), db.User)
	}
	return r.openDatabase(options, &db, engine, password, &databaseSpec{
		resourceType: resourceDirect, label: directLabel(name), title: name, account: account,
		intro: fmt.Sprintf("Direct database %s (%s) at %s", name, db.Engine, directAddress(&db)),
	})
}

// databaseSpec is what tells the session of a direct database from a Docker container's.
type databaseSpec struct {
	resourceType string
	// label keys the session in the registry.
	label string
	// title names the IDE data source ("airlock <title>") and the MCP server.
	title string
	// intro is the first line of the session intro.
	intro string
	// account is the keychain entry of the password, forgotten once rejected; "" when none.
	account string
}

// openDatabase opens db signed in with password: no AWS step, no process, no local port. It
// registers the IDE data source and starts the MCP server unless options skip them.
func (r *Runner) openDatabase(options *Options, db *directdb.Database, engine dbmcp.Engine, password string, spec *databaseSpec) (*Session, error) {
	name := db.Name
	if err := r.registry.claim(spec.label, name); err != nil {
		return nil, err
	}
	addr := directAddress(db)
	session := &Session{
		Label:        spec.label,
		ResourceType: spec.resourceType,
		Target:       name,
		Environment:  db.Environment,
		Production:   r.environments.Lookup(db.Environment).Production,
		Details: SessionDetails{
			ConnectedServer: addr.String(),
			RestartCommand:  "airlock " + spec.resourceType + " " + name,
			Auth:            "password · " + db.User,
		},
		Intro: []string{spec.intro, "TLS: " + string(db.EffectiveTLS())},
	}

	var removeDataSource func() string
	if !options.SkipIDEDataSource {
		session.Details.IDEDataSource, removeDataSource = r.registerDataSource(options, &ideadatasource.DataSource{
			Key:        spec.resourceType + "|" + name,
			Name:       "airlock " + spec.title,
			Engine:     string(db.Engine),
			Host:       jdbcHost(db.Host),
			Port:       db.EffectivePort(),
			Database:   db.Database,
			User:       db.User,
			Properties: jdbcTLSProperties(db),
		}, addr.String())
		if session.Details.IDEDataSource != "" {
			session.Intro = append(session.Intro, session.Details.IDEDataSource)
		}
	}
	if !options.SkipMCP {
		handle, display := r.startDirectMCP(options, db, engine, spec.title, password, spec.account, options.MCPAllowWrites)
		r.attachMCP(session, handle, display)
	}
	session.SignIn = &directSignIn{
		forget:   r.forgetKeychainFunc(spec.account, password),
		engine:   engine,
		user:     db.User,
		password: password,
		database: dbmcp.DefaultDatabase(string(db.Engine), db.Database),
		addr:     addr,
	}
	session.client = func(context.Context, *SignInCandidate) (Client, error) {
		return Client{Engine: string(engine), Address: addr, User: db.User, Password: password, Database: dbmcp.DefaultDatabase(string(db.Engine), db.Database)}, nil
	}
	session.release = sessionRelease(func() { r.registry.release(spec.label) }, session.mcp, removeDataSource)
	return session, nil
}

// startDirectMCP serves the MCP server of a direct database and returns its display line ("not
// started: …" on failure, which never fails the session).
func (r *Runner) startDirectMCP(options *Options, db *directdb.Database, engine dbmcp.Engine, name, password, account string, allowWrites bool) (server mcpserve.Session, display string) {
	return serveMCP(context.Background(), r, options, nil, func(projectDir string, _ aws.Config) (*dbmcp.Handle, error) {
		return dbmcpStartDirect(r.mcpHost, &dbmcp.DirectConfig{
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
			SkipRegistration:   options.SkipMCPRegistration,
		})
	})
}

// directSignInTimeout bounds the sign-in check of a session served without dashboard.
const directSignInTimeout = 15 * time.Second

// serveDirect signs the session in once, then prints its intro and keeps it open until ctx ends;
// the caller releases it. A refused password or an unreachable server
// fails at once: without dashboard nobody would ask the password again, and the MCP server would
// only fail on every call.
func (r *Runner) serveDirect(ctx context.Context, session *Session) error {
	if err := checkServedSignIn(ctx, session); err != nil {
		return fmt.Errorf("%s: %w", session.Target, err)
	}
	for _, line := range session.Intro {
		fprintf(r.out, "> %s\n", line)
	}
	fprintf(r.out, "Ctrl+C to stop.\n")
	<-ctx.Done() // Run ends ctx on Ctrl+C.
	return nil
}

// checkServedSignIn checks the first sign-in candidate of session; nil without check, or when ctx
// already ended (the user interrupted).
func checkServedSignIn(ctx context.Context, session *Session) error {
	if session.SignIn == nil || ctx.Err() != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, directSignInTimeout)
	defer cancel()
	candidates, err := session.SignIn.Candidates(ctx)
	if err != nil || len(candidates) == 0 {
		return err
	}
	return session.SignIn.Check(ctx, candidates[0])
}
