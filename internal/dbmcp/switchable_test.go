package dbmcp

import (
	"context"
	"database/sql/driver"
	"reflect"
	"testing"
)

// userCreds signs in as user, recording nothing else.
type userCreds string

func (u userCreds) Credentials(_ context.Context, database string) (Credentials, error) {
	return Credentials{User: string(u), Password: "p", Database: database}, nil
}

func (userCreds) Databases(context.Context) ([]string, error) { return nil, nil }

func TestSwitchingCredentialsDropsThePoolsOpenedWithTheFormerOnes(t *testing.T) {
	creds := &switchableProvider{current: staticCreds{fallback: "app"}}
	q, db, opened := newTestQuerier(&sqlServerDialect, creds)
	db.answer("SELECT 1", []string{"n"}, []driver.Value{int64(1)})
	ctx := context.Background()

	if _, err := q.Query(ctx, "", "SELECT 1", 1); err != nil {
		t.Fatal(err)
	}
	creds.set(userCreds("app_ro_integ"))
	if err := q.reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Query(ctx, "", "SELECT 1", 1); err != nil {
		t.Fatal(err)
	}
	// The second default database comes from the new credentials ("" for userCreds).
	if !reflect.DeepEqual(*opened, []string{"app", ""}) {
		t.Fatalf("opened = %q, want a new pool for the new credentials", *opened)
	}
	if got, err := creds.Credentials(ctx, ""); err != nil || got.User != "app_ro_integ" {
		t.Fatalf("user = %q, %v, want the new credentials", got.User, err)
	}
}

func TestSwitchableProviderKeepsTheRefreshOfItsCurrentProvider(t *testing.T) {
	secret := newSecretPasswordProvider((&fakeAWS{}).run, "arn:secret", "u", "")
	secret.creds = &Credentials{User: "u", Password: "old"}
	creds := &switchableProvider{current: secret}

	if _, ok := unwrapProvider(creds).(credentialRefresher); !ok {
		t.Fatal("a secret behind the switch must still be refreshed after a rejection")
	}
	creds.set(userCreds("u"))
	if _, ok := unwrapProvider(creds).(credentialRefresher); ok {
		t.Fatal("a provider without refresh must not be retried")
	}
}

func TestHandleUseSecretSwitchesTheServerCredentials(t *testing.T) {
	useIsolatedLocks(t)
	handle, err := Start(NewHost(), testSessionConfig(t, "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	if err := handle.UseSecret("arn:aws:secretsmanager:eu-west-3:111:secret:ro-AbCdEf", "app_ro"); err != nil {
		t.Fatal(err)
	}
	provider, ok := handle.creds.get().(*secretPasswordProvider)
	if !ok || provider.secretID != "arn:aws:secretsmanager:eu-west-3:111:secret:ro-AbCdEf" || provider.user != "app_ro" || provider.defaultDatabase != "postgres" {
		t.Fatalf("provider = %#v, want the secret with the default database", handle.creds.get())
	}
}
