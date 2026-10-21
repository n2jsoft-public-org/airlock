package cachemcp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckSignInWithoutAuth(t *testing.T) {
	m := startRedis(t)
	if err := CheckSignIn(context.Background(), ConnConfig{LocalPort: portOf(t, m)}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSignInWithRBACUser(t *testing.T) {
	m := startRedis(t)
	m.RequireUserAuth("app", "s3cret")
	port := portOf(t, m)

	if err := CheckSignIn(context.Background(), ConnConfig{LocalPort: port, Credentials: PasswordCredentials("app", "s3cret")}); err != nil {
		t.Fatalf("good password: %v", err)
	}
	err := CheckSignIn(context.Background(), ConnConfig{LocalPort: port, Credentials: PasswordCredentials("app", "wrong")})
	if !errors.Is(err, ErrSignInRejected) || !strings.Contains(err.Error(), "WRONGPASS") {
		t.Fatalf("wrong password: %v", err)
	}
	if err := CheckSignIn(context.Background(), ConnConfig{LocalPort: port}); !errors.Is(err, ErrSignInRejected) {
		t.Fatalf("no credentials: %v", err)
	}
}

func TestCheckSignInOverTLSChecksTheServerName(t *testing.T) {
	m := startTLSRedis(t, "master.sessions.abc123.euw3.cache.amazonaws.com")
	port := portOf(t, m)
	if err := CheckSignIn(context.Background(), ConnConfig{LocalPort: port, TLS: true, ServerName: "master.sessions.abc123.euw3.cache.amazonaws.com"}); err != nil {
		t.Fatalf("matching server name: %v", err)
	}
	if err := CheckSignIn(context.Background(), ConnConfig{LocalPort: port, TLS: true, ServerName: "other.example.com"}); err == nil {
		t.Fatal("a certificate for another host must be refused")
	}
}

func TestClientsPerDatabaseAndCredentialSwitch(t *testing.T) {
	m := startRedis(t)
	m.RequireUserAuth("app", "one")
	c := newClients(ConnConfig{LocalPort: portOf(t, m), Credentials: PasswordCredentials("app", "one")})
	t.Cleanup(func() {
		if err := c.close(); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()

	if err := c.get(1).Set(ctx, "k", "v", 0).Err(); err != nil {
		t.Fatal(err)
	}
	first, again, other := c.get(1), c.get(1), c.get(0)
	if first != again || other == first {
		t.Fatal("one client per database expected")
	}
	got, err := m.DB(1).Get("k")
	if err != nil || got != "v" {
		t.Fatalf("db 1 k = %q, %v", got, err)
	}

	m.RequireUserAuth("app", "two")
	if err := c.setCredentials(PasswordCredentials("app", "two")); err != nil {
		t.Fatal(err)
	}
	if err := c.get(0).Ping(ctx).Err(); err != nil {
		t.Fatalf("new credentials not used: %v", err)
	}
}

func TestMapError(t *testing.T) {
	cases := map[string]string{
		"NOPERM this user has no permissions to run the 'get' command": "refused by the cache user's ACL",
		"NOAUTH Authentication required.":                              "rejected the credentials",
	}
	for raw, want := range cases {
		if got := mapError(errors.New(raw)); !strings.Contains(got.Error(), want) {
			t.Errorf("mapError(%q) = %v, want %q", raw, got, want)
		}
	}
	// The server's final period would end the middle of a longer message ("required.: set …").
	//goland:noinspection GoErrorStringFormat // the server's own message, verbatim
	if got := mapError(errors.New("NOAUTH Authentication required.")); got.Error() != "the cache rejected the credentials: NOAUTH Authentication required" {
		t.Errorf("rejection = %q", got)
	}
	if err := mapError(errors.New("ERR syntax error")); err.Error() != "ERR syntax error" {
		t.Fatalf("other errors must pass through: %v", err)
	}
	if mapError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestClientsDialHost(t *testing.T) {
	cases := map[string]string{"": "localhost:6380", "127.0.0.1": "127.0.0.1:6380", "::1": "[::1]:6380"}
	for host, want := range cases {
		c := newClients(ConnConfig{Host: host, LocalPort: 6380})
		if got := c.get(0).Options().Addr; got != want {
			t.Errorf("host %q: addr = %q, want %q", host, got, want)
		}
		if err := c.close(); err != nil {
			t.Fatal(err)
		}
	}
}
