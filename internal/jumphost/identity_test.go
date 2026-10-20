package jumphost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
)

const (
	callerIdentityXML = `<GetCallerIdentityResponse><GetCallerIdentityResult><Arn>arn:aws:iam::111111111111:user/dev</Arn>` +
		`<Account>111111111111</Account><UserId>AIDATEST</UserId></GetCallerIdentityResult></GetCallerIdentityResponse>`
	assumeRoleXML = `<AssumeRoleResponse><AssumeRoleResult><Credentials><AccessKeyId>ASIATEST</AccessKeyId>` +
		`<SecretAccessKey>secret</SecretAccessKey><SessionToken>token</SessionToken><Expiration>2100-01-01T00:00:00Z</Expiration>` +
		`</Credentials></AssumeRoleResult></AssumeRoleResponse>`
	expiredTokenXML = `<ErrorResponse><Error><Type>Sender</Type><Code>ExpiredToken</Code>` +
		`<Message>The security token included in the request is expired</Message></Error></ErrorResponse>`
)

// installExpiredSTS points the SDK at an STS that rejects the credentials of profiles (and dev)
// until signedIn is set.
func installExpiredSTS(t *testing.T, profiles ...string) (signedIn *atomic.Bool) {
	t.Helper()
	signedIn = &atomic.Bool{}
	awsapitest.Env(t, func(w http.ResponseWriter, r *http.Request) {
		if !signedIn.Load() {
			awsapitest.RespondXML(t, w, http.StatusForbidden, expiredTokenXML)
			return
		}
		switch op := awsapitest.Operation(r); op {
		case "GetCallerIdentity":
			awsapitest.RespondXML(t, w, http.StatusOK, callerIdentityXML)
		case "AssumeRole":
			awsapitest.RespondXML(t, w, http.StatusOK, assumeRoleXML)
		default:
			t.Errorf("unexpected operation %s", op)
			w.WriteHeader(http.StatusBadRequest)
		}
	}, append([]string{"dev"}, profiles...)...)
	return signedIn
}

// serveSignedIn answers the identity check and the jumphost lookup (i-0123) of a signed-in
// profile; false for the other operations.
func serveSignedIn(t testing.TB, w http.ResponseWriter, r *http.Request) bool {
	switch awsapitest.Operation(r) {
	case "GetCallerIdentity":
		awsapitest.RespondXML(t, w, http.StatusOK, callerIdentityXML)
	case "DescribeInstances":
		awsapitest.RespondXML(t, w, http.StatusOK, instancePageXML)
	default:
		return false
	}
	return true
}

// installAWSAPI points the SDK of profile dev at a fake AWS where it is signed in, a jumphost runs
// and serve answers the other operations. The aws on PATH only passes the checks that the CLI is
// installed: it fails if run.
func installAWSAPI(t *testing.T, serve http.HandlerFunc) {
	t.Helper()
	awsapitest.Env(t, func(w http.ResponseWriter, r *http.Request) {
		if !serveSignedIn(t, w, r) {
			serve(w, r)
		}
	}, "dev")
	dir := t.TempDir()
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte("#!/bin/sh\necho \"unexpected aws $*\" >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// appendAWSConfig adds sections to the shared config file written by awsapitest.Env.
func appendAWSConfig(t *testing.T, sections string) {
	t.Helper()
	path := os.Getenv("AWS_CONFIG_FILE")
	existing, err := os.ReadFile(path) // #nosec G304 G703 -- the test's own config file.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(existing, "\n"+sections...), 0o600); err != nil { // #nosec G703 -- the test's own config file.
		t.Fatal(err)
	}
}

func countingInteractive(logins *atomic.Int32) InteractiveFunc {
	return func(context.Context, string, string, ...string) error {
		logins.Add(1)
		return nil
	}
}

func TestCheckIdentityReportsAnExpiredSignInWithoutSigningIn(t *testing.T) {
	signedIn := installExpiredSTS(t)
	var logins atomic.Int32
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: countingInteractive(&logins)})

	if err := runner.CheckIdentity(context.Background(), "dev", "eu-west-3"); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("err = %v, want ErrSignInRequired", err)
	}
	if logins.Load() != 0 {
		t.Fatal("checking the identity must never sign in")
	}
	signedIn.Store(true)
	if err := runner.CheckIdentity(context.Background(), "dev", "eu-west-3"); err != nil {
		t.Fatal(err)
	}
}

// unreachableAWS makes AWS out of reach of profile dev: the connection refused, or dropped
// before any response.
var unreachableAWS = map[string]func(t *testing.T){
	"closed":  func(t *testing.T) { awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev").Close() },
	"dropped": func(t *testing.T) { awsapitest.Env(t, awsapitest.DropConnection(t), "dev") },
}

const awsUnreachableText = "AWS could not be reached to check the credentials of profile dev"

func TestCheckIdentityKeepsUnreachableErrors(t *testing.T) {
	for name, install := range unreachableAWS {
		t.Run(name, func(t *testing.T) {
			install(t)
			runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
			err := runner.CheckIdentity(context.Background(), "dev", "eu-west-3")
			if err == nil || errors.Is(err, ErrSignInRequired) || !strings.Contains(err.Error(), awsUnreachableText) {
				t.Fatalf("err = %v, want AWS out of reach, not ErrSignInRequired", err)
			}
		})
	}
}

func TestEnsureIdentityNeverSignsInWhenAWSIsOutOfReach(t *testing.T) {
	for name, install := range unreachableAWS {
		t.Run(name, func(t *testing.T) {
			install(t)
			var logins atomic.Int32
			runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: countingInteractive(&logins)})
			for _, interactive := range []bool{true, false} {
				err := runner.ensureIdentity(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"}, interactive)
				if err == nil || errors.Is(err, ErrSignInRequired) || !strings.Contains(err.Error(), awsUnreachableText) {
					t.Errorf("interactive %v: err = %v, want AWS out of reach, not ErrSignInRequired", interactive, err)
				}
			}
			if logins.Load() != 0 {
				t.Fatal("AWS out of reach must never trigger a sign-in")
			}
		})
	}
}

func TestCheckIdentityReportsAnExpiredSSOToken(t *testing.T) {
	signedIn := installExpiredSTS(t)
	signedIn.Store(true) // STS would answer: only the missing SSO token fails.
	appendAWSConfig(t, "[profile sso]\nregion = eu-west-3\nsso_session = corp\nsso_account_id = 111111111111\nsso_role_name = Dev\n\n"+
		"[sso-session corp]\nsso_start_url = https://example.awsapps.com/start\nsso_region = eu-west-3\n")
	var logins atomic.Int32
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: countingInteractive(&logins)})

	if err := runner.CheckIdentity(context.Background(), "sso", "eu-west-3"); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("err = %v, want ErrSignInRequired", err)
	}
	if logins.Load() != 0 {
		t.Fatal("checking the identity must never sign in")
	}
}

func TestCheckIdentityReportsConfigErrors(t *testing.T) {
	installExpiredSTS(t)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	if err := runner.CheckIdentity(context.Background(), "unknown", "eu-west-3"); err == nil || errors.Is(err, ErrSignInRequired) {
		t.Fatalf("err = %v, want a configuration error, not ErrSignInRequired", err)
	}
}

func TestMFAProfilesAreNotSupported(t *testing.T) {
	installExpiredSTS(t)
	appendAWSConfig(t, "[profile mfa]\nregion = eu-west-3\nrole_arn = arn:aws:iam::111111111111:role/app\nsource_profile = dev\n"+
		"mfa_serial = arn:aws:iam::111111111111:mfa/dev\n")
	var logins atomic.Int32
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: countingInteractive(&logins)})

	check := func(name string, err error) {
		t.Helper()
		if err == nil || errors.Is(err, ErrSignInRequired) || !strings.Contains(err.Error(), "mfa_serial") || !strings.Contains(err.Error(), "profile mfa ") {
			t.Errorf("%s: err = %v, want the unsupported MFA profile named", name, err)
		}
	}
	check("CheckIdentity", runner.CheckIdentity(context.Background(), "mfa", "eu-west-3"))
	check("ensureIdentity", runner.ensureIdentity(context.Background(), awsConfig{Profile: "mfa", Region: "eu-west-3"}, true))
	check("unattended ensureIdentity", runner.ensureIdentity(context.Background(), awsConfig{Profile: "mfa", Region: "eu-west-3"}, false))
	if logins.Load() != 0 {
		t.Fatal("an MFA profile must never trigger a sign-in")
	}
}

func TestSignInRenewsAnExpiredSignIn(t *testing.T) {
	signedIn := installExpiredSTS(t)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	ctx := WithInteractive(context.Background(), func(context.Context, string, string, ...string) error {
		signedIn.Store(true)
		return nil
	})
	if err := runner.SignIn(ctx, "dev", "eu-west-3"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureIdentityPrintsTheCallerArn(t *testing.T) {
	installExpiredSTS(t).Store(true)
	var out strings.Builder
	runner := NewRunner(&RunnerOptions{Out: &out, Err: io.Discard})
	if err := runner.ensureIdentity(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"}, false); err != nil {
		t.Fatal(err)
	}
	if want := "Using AWS identity... arn:aws:iam::111111111111:user/dev\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestAccountForAsksSTS(t *testing.T) {
	installExpiredSTS(t).Store(true)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	if got := runner.accountFor(context.Background(), "dev"); got != "111111111111" {
		t.Fatalf("account = %q, want 111111111111", got)
	}
}
