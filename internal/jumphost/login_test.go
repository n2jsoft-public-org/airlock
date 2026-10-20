package jumphost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestExpiredIdentitySignsInThroughTheInteractiveRunner(t *testing.T) {
	signedIn := installExpiredSTS(t)
	var got []string
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: func(_ context.Context, purpose, name string, args ...string) error {
		if purpose == "" {
			t.Error("the sign-in does not say why the terminal is needed")
		}
		got = append([]string{name}, args...)
		signedIn.Store(true)
		return nil
	}})

	if err := runner.ensureIdentity(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"}, true); err != nil {
		t.Fatal(err)
	}
	if want := []string{executable(t), "sso-login", "--profile", "dev"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("interactive command = %v, want %v", got, want)
	}
}

func TestConcurrentExpiredIdentityChecksSignInOnce(t *testing.T) {
	signedIn := installExpiredSTS(t)
	var logins atomic.Int32
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: func(context.Context, string, string, ...string) error {
		logins.Add(1)
		time.Sleep(50 * time.Millisecond)
		signedIn.Store(true)
		return nil
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
	installExpiredSTS(t)
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

func TestEnsureIdentitySignsInTheSourceProfile(t *testing.T) {
	signedIn := installExpiredSTS(t, "corp")
	appendAWSConfig(t, "[profile app]\nregion = eu-west-3\nrole_arn = arn:aws:iam::111111111111:role/app\nsource_profile = corp\n")
	var got []string
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: func(_ context.Context, _, name string, args ...string) error {
		got = append([]string{name}, args...)
		signedIn.Store(true)
		return nil
	}})

	if err := runner.ensureIdentity(context.Background(), awsConfig{Profile: "app", Region: "eu-west-3"}, true); err != nil {
		t.Fatal(err)
	}
	if want := []string{executable(t), "sso-login", "--profile", "corp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("interactive command = %v, want %v", got, want)
	}
}

// executable is the airlock binary the runner re-runs to sign in: the test binary here.
func executable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFailedSignInKeepsItsReason(t *testing.T) {
	installExpiredSTS(t)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: func(context.Context, string, string, ...string) error {
		return fmt.Errorf("dashboard gone: %w", ErrSignInRequired)
	}})

	err := runner.ensureIdentity(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"}, true)
	if err == nil {
		t.Fatal("ensureIdentity succeeded, want the sign-in error")
	}
	if !errors.Is(err, ErrSignInRequired) || !strings.Contains(err.Error(), "dashboard gone") {
		t.Fatalf("err = %v, want the cause kept", err)
	}
}
