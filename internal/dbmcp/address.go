package dbmcp

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// TLSMode is how a connection encrypts its traffic and checks the server certificate.
type TLSMode string

const (
	// TLSRequire encrypts without checking the certificate: through the tunnel it names the RDS
	// host, not localhost. It is the mode of an empty TLSMode.
	TLSRequire TLSMode = "require"
	// TLSVerify encrypts and checks the certificate against the host name and the system roots.
	TLSVerify TLSMode = "verify"
	// TLSDisable sends everything in clear, for local databases.
	TLSDisable TLSMode = "disable"
)

// tunnelHost is where the local end of an SSM tunnel listens.
const tunnelHost = "127.0.0.1"

// Address is where a querier connects: the local end of a tunnel, or a database reached directly.
type Address struct {
	Host string
	TLS  TLSMode
	Port int
	// tunnel marks the local end of an SSM tunnel, whose refused connections mean it is not up yet.
	tunnel bool
}

// TunnelAddress is the local end of the SSM tunnel listening on port.
func TunnelAddress(port int) Address {
	return Address{Host: tunnelHost, Port: port, TLS: TLSRequire, tunnel: true}
}

// String is host:port, with brackets around an IPv6 host.
func (a Address) String() string {
	return net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
}

// postgresConnString is the pgx keyword/value connection string of addr; credentials and the
// database are set on the parsed config.
func postgresConnString(addr Address) string {
	return fmt.Sprintf("host=%s port=%d sslmode=%s application_name=%s", addr.Host, addr.Port, postgresSSLMode(addr.TLS), applicationName)
}

func postgresSSLMode(mode TLSMode) string {
	switch mode {
	case TLSVerify:
		return "verify-full"
	case TLSDisable:
		return "disable"
	default:
		// RDS IAM authentication requires TLS; the tunnel's certificate names the RDS host, not localhost.
		return "require"
	}
}

// mysqlConfig is the go-sql-driver configuration of addr opening database; credentials are set
// before each connection. Cleartext passwords, which RDS IAM tokens need, are only sent over TLS.
func mysqlConfig(addr Address, database string, params map[string]string) *mysql.Config {
	config := mysql.NewConfig()
	config.Net, config.Addr, config.DBName = "tcp", addr.String(), database
	config.Params = params
	config.ConnectionAttributes = "program_name:" + applicationName
	// Values are quoted by the driver: one round trip per statement through the tunnel, no prepare.
	config.InterpolateParams = true
	// The driver logs to stderr by default, which would garble the terminal UI.
	config.Logger = &mysql.NopLogger{}
	switch addr.TLS {
	case TLSVerify:
		config.TLS = &tls.Config{ServerName: addr.Host, MinVersion: tls.VersionTLS12}
	case TLSDisable:
	default:
		// The tunnel's certificate names the RDS host, not localhost: encrypt without checking it.
		config.TLS = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // as PostgreSQL's sslmode=require
	}
	config.AllowCleartextPasswords = config.TLS != nil
	return config
}

// sqlServerDSN is the go-mssqldb URL of addr signing in with c; url.UserPassword escapes the
// user and password.
func sqlServerDSN(addr Address, c Credentials) string {
	query := url.Values{
		"database": {c.Database},
		"app name": {applicationName},
	}
	switch addr.TLS {
	case TLSVerify:
		query.Set("encrypt", "true")
		query.Set("TrustServerCertificate", "false")
	case TLSDisable:
		query.Set("encrypt", "disable")
	default:
		query.Set("encrypt", "true")
		query.Set("TrustServerCertificate", "true")
	}
	dsn := url.URL{
		Scheme:   "sqlserver",
		User:     url.UserPassword(c.User, c.Password),
		Host:     addr.String(),
		RawQuery: query.Encode(),
	}
	return dsn.String()
}

// directConnectionHint explains why a database reached directly refused the connection; empty
// when the error is not about reaching it.
func directConnectionHint(addr Address, err error) string {
	text := err.Error()
	switch {
	case addr.TLS == TLSVerify && (strings.Contains(text, "x509:") || strings.Contains(text, "certificate")):
		return fmt.Sprintf("certificate of %s is not trusted: use tls require, or install its CA", addr.Host)
	case isUnreachable(err):
		return fmt.Sprintf("%s is not reachable (VPN or firewall?)", addr)
	default:
		return ""
	}
}

// isUnreachable reports whether err means the host could not be reached: unknown name, refused
// or timed out connection. Drivers do not always wrap the net error, so its text is checked too.
func isUnreachable(err error) bool {
	if dnsErr, ok := errors.AsType[*net.DNSError](err); ok && dnsErr != nil {
		return true
	}
	if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Op == "dial" {
		return true
	}
	text := err.Error()
	for _, cause := range []string{"connection refused", "i/o timeout", "no such host", "network is unreachable", "no route to host"} {
		if strings.Contains(text, cause) {
			return true
		}
	}
	return false
}
