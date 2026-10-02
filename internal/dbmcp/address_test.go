package dbmcp

import (
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	mssql "github.com/microsoft/go-mssqldb"
)

func TestTunnelConnectionSettingsAreUnchanged(t *testing.T) {
	addr := TunnelAddress(15432)
	if got := postgresConnString(addr); got != "host=127.0.0.1 port=15432 sslmode=require application_name=airlock-mcp" {
		t.Fatalf("postgres conn string = %q", got)
	}
	got := sqlServerDSN(addr, Credentials{User: "app", Password: "pw", Database: "Billing"})
	want := "sqlserver://app:pw@127.0.0.1:15432?TrustServerCertificate=true&app+name=airlock-mcp&database=Billing&encrypt=true"
	if got != want {
		t.Fatalf("sql server DSN = %q, want %q", got, want)
	}
}

func TestPostgresConnStringTLSModes(t *testing.T) {
	for _, tc := range []struct {
		mode        TLSMode
		sslmode     string
		checkedHost string
		tls         bool
		skipVerify  bool
	}{
		// pgx sets ServerName (SNI) to the host even when it does not verify the certificate.
		{mode: TLSRequire, sslmode: "sslmode=require", tls: true, skipVerify: true, checkedHost: "billing.db.internal"},
		{mode: "", sslmode: "sslmode=require", tls: true, skipVerify: true, checkedHost: "billing.db.internal"},
		{mode: TLSVerify, sslmode: "sslmode=verify-full", tls: true, checkedHost: "billing.db.internal"},
		{mode: TLSDisable, sslmode: "sslmode=disable"},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			addr := Address{Host: "billing.db.internal", Port: 5432, TLS: tc.mode}
			conn := postgresConnString(addr)
			if conn != "host=billing.db.internal port=5432 "+tc.sslmode+" application_name=airlock-mcp" {
				t.Fatalf("conn string = %q", conn)
			}
			config, err := pgx.ParseConfig(conn)
			if err != nil {
				t.Fatal(err)
			}
			if (config.TLSConfig != nil) != tc.tls {
				t.Fatalf("TLS = %v, want %v", config.TLSConfig != nil, tc.tls)
			}
			if !tc.tls {
				return
			}
			if config.TLSConfig.InsecureSkipVerify != tc.skipVerify || config.TLSConfig.ServerName != tc.checkedHost {
				t.Fatalf("TLS config: skip verify %v, server name %q", config.TLSConfig.InsecureSkipVerify, config.TLSConfig.ServerName)
			}
		})
	}
}

func TestSQLServerDSNTLSModes(t *testing.T) {
	for _, tc := range []struct {
		mode    TLSMode
		encrypt string
		trust   string
	}{
		{mode: TLSRequire, encrypt: "true", trust: "true"},
		{mode: "", encrypt: "true", trust: "true"},
		{mode: TLSVerify, encrypt: "true", trust: "false"},
		{mode: TLSDisable, encrypt: "disable", trust: ""},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			dsn := sqlServerDSN(Address{Host: "billing.db.internal", Port: 1433, TLS: tc.mode}, Credentials{User: "mcp_ro", Password: "pw", Database: "Billing"})
			parsed, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			query := parsed.Query()
			if parsed.Host != "billing.db.internal:1433" || query.Get("encrypt") != tc.encrypt || query.Get("TrustServerCertificate") != tc.trust {
				t.Fatalf("DSN = %q", dsn)
			}
			if _, err := mssql.NewConnector(dsn); err != nil {
				t.Fatalf("go-mssqldb refuses %q: %v", dsn, err)
			}
		})
	}
}

func TestSQLServerDSNEscapesPassword(t *testing.T) {
	const password = "p@ss/w?rd%#&="
	dsn := sqlServerDSN(Address{Host: "billing.db.internal", Port: 1433, TLS: TLSRequire}, Credentials{User: "mcp ro", Password: password, Database: "Billing"})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := parsed.User.Password()
	if got != password || parsed.User.Username() != "mcp ro" || parsed.Host != "billing.db.internal:1433" || parsed.Query().Get("database") != "Billing" {
		t.Fatalf("DSN %q parses to user %q, password %q, host %q", dsn, parsed.User.Username(), got, parsed.Host)
	}
	if _, err := mssql.NewConnector(dsn); err != nil {
		t.Fatalf("go-mssqldb refuses %q: %v", dsn, err)
	}
}

func TestAddressStringBracketsIPv6(t *testing.T) {
	addr := Address{Host: "fd00::12", Port: 5432, TLS: TLSRequire}
	if got := addr.String(); got != "[fd00::12]:5432" {
		t.Fatalf("String() = %q", got)
	}
	config, err := pgx.ParseConfig(postgresConnString(addr))
	if err != nil || config.Host != "fd00::12" || config.Port != 5432 {
		t.Fatalf("postgres config = %+v, %v", config, err)
	}
	parsed, err := url.Parse(sqlServerDSN(Address{Host: "::1", Port: 1433}, Credentials{User: "u", Password: "p"}))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "[::1]:1433" {
		t.Fatalf("sql server host = %q", parsed.Host)
	}
}
