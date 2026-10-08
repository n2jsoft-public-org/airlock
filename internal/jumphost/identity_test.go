package jumphost

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// installUnreachableAWS puts an aws executable on PATH that cannot reach AWS.
func installUnreachableAWS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'Could not connect to the endpoint URL: \"https://sts.eu-west-3.amazonaws.com/\"' >&2\nexit 255\n"
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestCheckIdentityReportsAnExpiredSignInWithoutSigningIn(t *testing.T) {
	signedIn := installExpiredAWS(t)
	var logins atomic.Int32
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: func(context.Context, string, string, ...string) error {
		logins.Add(1)
		return nil
	}})

	if err := runner.CheckIdentity(context.Background(), "dev", "eu-west-3"); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("err = %v, want ErrSignInRequired", err)
	}
	if logins.Load() != 0 {
		t.Fatal("checking the identity must never sign in")
	}
	if err := os.WriteFile(signedIn, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runner.CheckIdentity(context.Background(), "dev", "eu-west-3"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckIdentityTellsUnreachableAWSFromAnExpiredSignIn(t *testing.T) {
	installUnreachableAWS(t)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	if err := runner.CheckIdentity(context.Background(), "dev", "eu-west-3"); err == nil || errors.Is(err, ErrSignInRequired) {
		t.Fatalf("err = %v, want an error other than ErrSignInRequired", err)
	}
}

func TestSignInRenewsAnExpiredSignIn(t *testing.T) {
	signedIn := installExpiredAWS(t)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	ctx := WithInteractive(context.Background(), func(context.Context, string, string, ...string) error {
		return os.WriteFile(signedIn, nil, 0o600)
	})
	if err := runner.SignIn(ctx, "dev", "eu-west-3"); err != nil {
		t.Fatal(err)
	}
}
