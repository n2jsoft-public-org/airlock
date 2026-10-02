package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// stubDirectSignIn replaces the sign-in check, which needs a database, and records its calls as
// "<name>:<password>".
func stubDirectSignIn(t *testing.T, result error) *[]string {
	t.Helper()
	var calls []string
	previous := checkDirectSignIn
	checkDirectSignIn = func(_ context.Context, _ *jumphost.Runner, name, password string) error {
		calls = append(calls, name+":"+password)
		return result
	}
	t.Cleanup(func() { checkDirectSignIn = previous })
	return &calls
}

func TestDBTestUsesTheKeychainPassword(t *testing.T) {
	store, memory := testDatabases(t)
	putDatabase(t, store, &directdb.Database{Name: "archive", Engine: directdb.EnginePostgres, Host: "archive.internal", User: "reader"})
	rememberDirectPassword(t, memory, "archive", "reader")
	calls := stubDirectSignIn(t, nil)

	out, err := runAirlock(t, "db", "test", "archive")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(*calls, ",") != "archive:s3cret" {
		t.Fatalf("calls = %q, want the keychain password", *calls)
	}
	if out != "OK: signed in to archive (archive.internal:5432) as reader\n" {
		t.Fatalf("out = %q", out)
	}
}

func TestDBTestAsksThePasswordWhenNoneIsRemembered(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &directdb.Database{Name: "archive", Engine: directdb.EnginePostgres, Host: "archive.internal", User: "reader"})
	calls := stubDirectSignIn(t, nil)

	out, err := runAirlockWithInput(t, "typed-pass\n", "db", "test", "archive")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(*calls, ",") != "archive:typed-pass" {
		t.Fatalf("calls = %q, want the typed password", *calls)
	}
	if strings.Contains(out, "typed-pass") {
		t.Fatalf("out = %q must never show the password", out)
	}
}

func TestDBTestRefusesAnEmptyPassword(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &directdb.Database{Name: "archive", Engine: directdb.EnginePostgres, Host: "archive.internal", User: "reader"})
	calls := stubDirectSignIn(t, nil)

	_, err := runAirlockWithInput(t, "\n", "db", "test", "archive")
	if err == nil || err.Error() != "no password given" || len(*calls) != 0 {
		t.Fatalf("err = %v, calls = %q", err, *calls)
	}
}

func TestDBTestReportsTheSignInError(t *testing.T) {
	store, memory := testDatabases(t)
	putDatabase(t, store, &directdb.Database{Name: "archive", Engine: directdb.EnginePostgres, Host: "archive.internal", User: "reader"})
	rememberDirectPassword(t, memory, "archive", "reader")
	unreachable := errors.New("archive.internal:5432 is not reachable (VPN or firewall?)")
	stubDirectSignIn(t, unreachable)

	out, err := runAirlock(t, "db", "test", "archive")
	if !errors.Is(err, unreachable) || strings.Contains(out, "OK") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestDBTestWarnsAboutSQLServerReadOnlyAccess(t *testing.T) {
	store, memory := testDatabases(t)
	putDatabase(t, store, &billing)
	rememberDirectPassword(t, memory, "legacy-billing", "mcp_ro")
	stubDirectSignIn(t, nil)

	out, err := runAirlock(t, "db", "test", "legacy-billing")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Warning: SQL Server: read-only access relies on the permissions of mcp_ro") {
		t.Fatalf("out = %q, want the read-only warning", out)
	}
}

func TestDBTestUnknownName(t *testing.T) {
	testDatabases(t)
	stubDirectSignIn(t, nil)
	if _, err := runAirlock(t, "db", "test", "nope"); err == nil || err.Error() != "no direct database named nope (see airlock db list)" {
		t.Fatalf("err = %v", err)
	}
}
