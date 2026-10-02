package directdb

import (
	"errors"
	"strings"
	"testing"
)

// testDatabase is a valid PostgreSQL definition named name, with every optional field left out.
func testDatabase(name string) Database {
	return Database{Name: name, Engine: EnginePostgres, Host: "db.internal", User: "app"}
}

func TestDefaultPort(t *testing.T) {
	for engine, want := range map[Engine]int{EnginePostgres: 5432, EngineSQLServer: 1433, "mysql": 0} {
		if got := DefaultPort(engine); got != want {
			t.Fatalf("DefaultPort(%q) = %d, want %d", engine, got, want)
		}
	}
}

func TestEffectiveValuesApplyTheDefaults(t *testing.T) {
	db := testDatabase("billing")
	if db.EffectivePort() != 5432 || db.EffectiveTLS() != TLSRequire || db.Address() != "db.internal:5432" {
		t.Fatalf("defaults: port %d, tls %q, address %q", db.EffectivePort(), db.EffectiveTLS(), db.Address())
	}
	db.Engine, db.Port, db.TLS = EngineSQLServer, 0, TLSVerify
	if db.EffectivePort() != 1433 || db.EffectiveTLS() != TLSVerify {
		t.Fatalf("sqlserver: port %d, tls %q", db.EffectivePort(), db.EffectiveTLS())
	}
	db.Port = 14330
	if db.EffectivePort() != 14330 || db.Address() != "db.internal:14330" {
		t.Fatalf("explicit port: %d, %q", db.EffectivePort(), db.Address())
	}
}

func TestNormalizeTrimsAndLowerCases(t *testing.T) {
	db := Database{
		Name: " billing ", Engine: " SQLServer ", Host: " db.internal ", Database: " Billing ",
		User: " mcp_ro ", TLS: " Verify ",
	}
	db.Normalize()
	want := Database{Name: "billing", Engine: EngineSQLServer, Host: "db.internal", Database: "Billing", User: "mcp_ro", TLS: TLSVerify}
	if db != want {
		t.Fatalf("normalized = %+v, want %+v", db, want)
	}
}

func TestValidateAcceptsValidDatabases(t *testing.T) {
	minimal := testDatabase("billing")
	full := Database{
		Name: "legacy-billing-2", Engine: EngineSQLServer, Host: "10.0.3.12", Database: "Billing",
		User: "mcp_ro", TLS: TLSDisable, Port: 14330, Production: true,
	}
	for _, db := range []Database{minimal, full} {
		if err := db.Validate(); err != nil {
			t.Fatalf("Validate(%+v) = %v, want nil", db, err)
		}
	}
}

func TestValidateAcceptsIPv6Host(t *testing.T) {
	for host, address := range map[string]string{"::1": "[::1]:5432", "fd00::12": "[fd00::12]:5432"} {
		db := testDatabase("local")
		db.Host = host
		if err := db.Validate(); err != nil {
			t.Fatalf("Validate(host %q) = %v, want nil", host, err)
		}
		if db.Address() != address {
			t.Fatalf("Address() = %q, want %q", db.Address(), address)
		}
	}
}

func TestValidateNamesTheInvalidField(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*Database)
		field  string
	}{
		"empty name":     {func(d *Database) { d.Name = "" }, "name"},
		"upper case":     {func(d *Database) { d.Name = "Billing" }, "name"},
		"unknown engine": {func(d *Database) { d.Engine = "mysql" }, "engine"},
		"empty engine":   {func(d *Database) { d.Engine = "" }, "engine"},
		"empty host":     {func(d *Database) { d.Host = "" }, "host"},
		"scheme":         {func(d *Database) { d.Host = "postgres://db.internal" }, "host"},
		"port in host":   {func(d *Database) { d.Host = "db.internal:5432" }, "host"},
		"path":           {func(d *Database) { d.Host = "db.internal/billing" }, "host"},
		"space":          {func(d *Database) { d.Host = "db internal" }, "host"},
		"brackets":       {func(d *Database) { d.Host = "[::1]" }, "host"},
		"newline":        {func(d *Database) { d.Host = "db.internal\nsslmode=disable" }, "host"},
		"carriage":       {func(d *Database) { d.Host = "db.internal\r" }, "host"},
		"delete char":    {func(d *Database) { d.Host = "db.internal\x7f" }, "host"},
		"equals":         {func(d *Database) { d.Host = "db.internal=x" }, "host"},
		"single quote":   {func(d *Database) { d.Host = "db'.internal" }, "host"},
		"double quote":   {func(d *Database) { d.Host = `db".internal` }, "host"},
		"backslash":      {func(d *Database) { d.Host = `db\internal` }, "host"},
		"negative port":  {func(d *Database) { d.Port = -1 }, "port"},
		"port too large": {func(d *Database) { d.Port = 65536 }, "port"},
		"host C1 escape": {func(d *Database) { d.Host = "db\u009b.internal" }, "host"},
		"empty user":     {func(d *Database) { d.User = "" }, "user"},
		"user escape":    {func(d *Database) { d.User = "ro\x1b[1A\x1b[2K\rmcp" }, "user"},
		"user newline":   {func(d *Database) { d.User = "ro\nmcp" }, "user"},
		"user C1 escape": {func(d *Database) { d.User = "ro\u009b2K" }, "user"},
		"user delete":    {func(d *Database) { d.User = "ro\x7f" }, "user"},
		"db newline":     {func(d *Database) { d.Database = "a\nb" }, "database"},
		"db escape":      {func(d *Database) { d.Database = "a\x1b[2Kb" }, "database"},
		"db C1 escape":   {func(d *Database) { d.Database = "a\u0085b" }, "database"},
		"db semicolon":   {func(d *Database) { d.Database = "billing;encrypt=disable" }, "database"},
		"db question":    {func(d *Database) { d.Database = "billing?sslmode=disable" }, "database"},
		"db ampersand":   {func(d *Database) { d.Database = "billing&x" }, "database"},
		"db equals":      {func(d *Database) { d.Database = "billing=x" }, "database"},
		"db slash":       {func(d *Database) { d.Database = "billing/x" }, "database"},
		"db backslash":   {func(d *Database) { d.Database = `billing\x` }, "database"},
		"unknown tls":    {func(d *Database) { d.TLS = "strict" }, "tls"},
	} {
		db := testDatabase("billing")
		tc.change(&db)
		err := db.Validate()
		fieldErr, ok := errors.AsType[*FieldError](err)
		if !ok || fieldErr.Field != tc.field {
			t.Fatalf("%s: err = %v, want a %s field error", name, err, tc.field)
		}
		if !strings.HasPrefix(fieldErr.Error(), tc.field+": ") {
			t.Fatalf("%s: message %q must start with the field", name, fieldErr)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, name := range []string{"billing", "legacy-billing", "db2", "a-1-b", "a" + strings.Repeat("b", 62)} {
		if err := ValidName(name); err != nil {
			t.Fatalf("ValidName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{"", "2db", "-billing", "billing-", "legacy--billing", "Legacy", "legacy_billing", "a" + strings.Repeat("b", 63)} {
		var fieldErr *FieldError
		if err := ValidName(name); !errors.As(err, &fieldErr) || fieldErr.Field != "name" {
			t.Fatalf("ValidName(%q) = %v, want a name field error", name, err)
		}
	}
}

func TestConnectionChanged(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*Database)
		want   bool
	}{
		"nothing":         {func(*Database) {}, false},
		"production only": {func(d *Database) { d.Production = true }, false},
		"name":            {func(d *Database) { d.Name = "billing2" }, true},
		"engine":          {func(d *Database) { d.Engine = EngineSQLServer }, true},
		"host":            {func(d *Database) { d.Host = "db2.internal" }, true},
		"port":            {func(d *Database) { d.Port = 6432 }, true},
		"database":        {func(d *Database) { d.Database = "events" }, true},
		"user":            {func(d *Database) { d.User = "writer" }, true},
		"tls":             {func(d *Database) { d.TLS = TLSDisable }, true},
	} {
		previous := testDatabase("billing")
		next := previous
		tc.change(&next)
		if got := ConnectionChanged(&previous, &next); got != tc.want {
			t.Fatalf("%s: ConnectionChanged = %v, want %v", name, got, tc.want)
		}
	}
}
