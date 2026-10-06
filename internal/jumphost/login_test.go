package jumphost

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// installExpiredAWS puts an aws executable on PATH whose identity check fails until signedIn exists.
func installExpiredAWS(t *testing.T) (signedIn string) {
	t.Helper()
	dir := t.TempDir()
	signedIn = filepath.Join(dir, "signed-in")
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*get-caller-identity*) if [ -f '" + signedIn + "' ]; then echo arn:aws:iam::111111111111:user/dev; else echo 'Token has expired' >&2; exit 255; fi ;;\n" +
		"*) exit 1 ;;\n" +
		"esac\n"
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return signedIn
}

func TestExpiredIdentitySignsInThroughTheInteractiveRunner(t *testing.T) {
	signedIn := installExpiredAWS(t)
	var got []string
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: func(_ context.Context, purpose, name string, args ...string) error {
		if purpose == "" {
			t.Error("the sign-in does not say why the terminal is needed")
		}
		got = append([]string{name}, args...)
		return os.WriteFile(signedIn, nil, 0o600)
	}})

	if err := runner.ensureIdentity(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"}, true); err != nil {
		t.Fatal(err)
	}
	if want := []string{"aws", "sso", "login", "--profile", "dev"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("interactive command = %v, want %v", got, want)
	}
}

func TestConcurrentExpiredIdentityChecksSignInOnce(t *testing.T) {
	signedIn := installExpiredAWS(t)
	var logins atomic.Int32
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: func(context.Context, string, string, ...string) error {
		logins.Add(1)
		time.Sleep(50 * time.Millisecond)
		return os.WriteFile(signedIn, nil, 0o600)
	}})

	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if err := runner.ensureIdentity(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"}, true); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := logins.Load(); n != 1 {
		t.Fatalf("signed in %d times, want once: each sign-in opens the browser", n)
	}
}

func TestUnattendedExpiredIdentityNeverSignsIn(t *testing.T) {
	installExpiredAWS(t)
	var logins atomic.Int32
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: func(context.Context, string, string, ...string) error {
		logins.Add(1)
		return nil
	}})

	_, err := runner.resolveAWSConfig(context.Background(), &Options{AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if !errors.Is(err, ErrSignInRequired) || !strings.Contains(err.Error(), "dev") {
		t.Fatalf("err = %v, want ErrSignInRequired naming the profile", err)
	}
	if n := logins.Load(); n != 0 {
		t.Fatalf("signed in %d times, want never: nobody may be there to answer", n)
	}
}
