// Package directdb keeps the databases airlock reaches directly, without jumphost: their
// definitions, the reader of the databases.json of an earlier airlock, and the strings sharing them
// with teammates. Passwords are never part of them.
package directdb

import (
	"bytes"
	"encoding/json"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/n2jsoft/airlock/internal/environment"
)

// Engine is the database engine of a direct database.
type Engine string

// The engines of direct databases.
const (
	EnginePostgres  Engine = "postgres"
	EngineSQLServer Engine = "sqlserver"
)

// TLSMode is how the connection to a direct database is encrypted.
type TLSMode string

const (
	// TLSRequire encrypts without checking the server certificate.
	TLSRequire TLSMode = "require"
	// TLSVerify encrypts and checks the certificate and the host name it names.
	TLSVerify TLSMode = "verify"
	// TLSDisable does not encrypt: for local databases only.
	TLSDisable TLSMode = "disable"
)

// maxNameLength bounds names, which end up in MCP server names and keychain entries.
const maxNameLength = 63

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// Database is the definition of one direct database. Name keys it in saved-connections.json; empty Port,
// Database and TLS fields apply the defaults (EffectivePort, EffectiveTLS).
type Database struct {
	Name     string  `json:"-"`
	Engine   Engine  `json:"engine"`
	Host     string  `json:"host"`
	Database string  `json:"database,omitempty"`
	User     string  `json:"user"`
	TLS      TLSMode `json:"tls,omitempty"`
	Port     int     `json:"port,omitempty"`
	// Environment names the database's environment in the environments setting; "" for none.
	Environment string `json:"environment,omitempty"`
}

// UnmarshalJSON reads a definition, refusing unknown keys; the production flag of an earlier
// airlock reads as the production environment.
func (d *Database) UnmarshalJSON(data []byte) error {
	type plain Database
	var raw struct {
		plain
		Production bool `json:"production,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	*d = Database(raw.plain)
	if d.Environment == "" && raw.Production {
		d.Environment = environment.ProductionAlias
	}
	return nil
}

// FieldError reports the invalid field of a definition.
type FieldError struct {
	Field   string
	Message string
}

// Error reads "<field>: <message>".
func (e *FieldError) Error() string {
	return e.Field + ": " + e.Message
}

// DefaultPort is the port of engine when none is set; 0 for an unknown engine.
func DefaultPort(e Engine) int {
	switch e {
	case EnginePostgres:
		return 5432
	case EngineSQLServer:
		return 1433
	default:
		return 0
	}
}

// Normalize trims every text field and lower-cases the engine, the TLS mode and the environment.
func (d *Database) Normalize() {
	d.Name = strings.TrimSpace(d.Name)
	d.Engine = Engine(strings.ToLower(strings.TrimSpace(string(d.Engine))))
	d.Host = strings.TrimSpace(d.Host)
	d.Database = strings.TrimSpace(d.Database)
	d.User = strings.TrimSpace(d.User)
	d.TLS = TLSMode(strings.ToLower(strings.TrimSpace(string(d.TLS))))
	d.Environment = strings.ToLower(strings.TrimSpace(d.Environment))
}

// Validate returns a *FieldError naming the first invalid field, in the order name, engine, host,
// port, database, user, tls, environment; nil when the definition is valid. It does not normalize.
func (d *Database) Validate() error {
	if err := ValidName(d.Name); err != nil {
		return err
	}
	if d.Engine != EnginePostgres && d.Engine != EngineSQLServer {
		return &FieldError{Field: "engine", Message: "must be postgres or sqlserver"}
	}
	if message := hostProblem(d.Host); message != "" {
		return &FieldError{Field: "host", Message: message}
	}
	if d.Port < 0 || d.Port > 65535 {
		return &FieldError{Field: "port", Message: "must be between 1 and 65535 (leave it out for the engine's default)"}
	}
	if strings.ContainsFunc(d.Database, isForbiddenDatabaseRune) {
		return &FieldError{Field: "database", Message: `must be a database name, without control characters or ; ? & = / \`}
	}
	switch {
	case d.User == "":
		return &FieldError{Field: "user", Message: "must not be empty"}
	case strings.ContainsFunc(d.User, unicode.IsControl):
		return &FieldError{Field: "user", Message: "must not contain control characters"}
	}
	switch d.TLS {
	case "", TLSRequire, TLSVerify, TLSDisable:
	default:
		return &FieldError{Field: "tls", Message: "must be require, verify or disable"}
	}
	if d.Environment != "" {
		if err := environment.ValidName(d.Environment); err != nil {
			return &FieldError{Field: "environment", Message: "must be 1 to 12 lower-case letters, digits or -, starting with a letter"}
		}
	}
	return nil
}

// hostProblem explains why host is not a bare host name or IP address; "" when it is.
func hostProblem(host string) string {
	switch {
	case host == "":
		return "must not be empty"
	case strings.Contains(host, "://"):
		return "must not contain a scheme (write db.internal, not postgres://db.internal)"
	case strings.ContainsAny(host, "/?#@ \t"):
		return "must be a host name or IP address, without path, user or spaces"
	case strings.ContainsFunc(host, isForbiddenHostRune):
		return "must be a host name or IP address, without spaces, quotes or control characters"
	case strings.HasPrefix(host, "["):
		return "must be written without brackets (IPv6: fd00::12)"
	case strings.Contains(host, ":") && net.ParseIP(host) == nil:
		return "must not contain a port (use the port field)"
	default:
		return ""
	}
}

// isForbiddenHostRune reports the characters that would break out of a keyword/value connection
// string: control characters (C0, DEL and C1), quotes, backslashes and equal signs.
func isForbiddenHostRune(r rune) bool {
	return unicode.IsControl(r) || strings.ContainsRune("='\"\\", r)
}

// isForbiddenDatabaseRune reports the characters a database name must not hold: control
// characters, which could hide lines of the printed import plan, and the separators of URL and
// JDBC properties, which could add properties to the IDE's connection URL.
func isForbiddenDatabaseRune(r rune) bool {
	return unicode.IsControl(r) || strings.ContainsRune(";?&=/\\", r)
}

// ConnectionChanged reports whether next is reached differently from previous: any field but
// Environment differs (name, engine, host, port, database, user or TLS). A password remembered for
// previous must then be forgotten, so that it never reaches another server, database or user.
func ConnectionChanged(previous, next *Database) bool {
	before, after := *previous, *next
	before.Environment, after.Environment = "", ""
	return before != after
}

// ValidName returns a *FieldError when name cannot name a direct database.
func ValidName(name string) error {
	switch {
	case name == "":
		return &FieldError{Field: "name", Message: "must not be empty"}
	case len(name) > maxNameLength:
		return &FieldError{Field: "name", Message: "must be at most 63 characters"}
	case !namePattern.MatchString(name):
		return &FieldError{Field: "name", Message: "must be lower-case letters, digits and single dashes, starting with a letter (e.g. legacy-billing)"}
	default:
		return nil
	}
}

// EffectivePort is Port, else the engine's default port.
func (d *Database) EffectivePort() int {
	if d.Port != 0 {
		return d.Port
	}
	return DefaultPort(d.Engine)
}

// EffectiveTLS is TLS, else TLSRequire.
func (d *Database) EffectiveTLS() TLSMode {
	if d.TLS == "" {
		return TLSRequire
	}
	return d.TLS
}

// Address is host:port, IPv6 hosts in brackets.
func (d *Database) Address() string {
	return net.JoinHostPort(d.Host, strconv.Itoa(d.EffectivePort()))
}
