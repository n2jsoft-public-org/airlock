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

	"github.com/n2jsoft/airlock/internal/awsapi"
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
// and serve answers the other operations. The PATH is empty: only tunnels need the AWS CLI.
func installAWSAPI(t *testing.T, serve http.HandlerFunc) {
	t.Helper()
	awsapitest.Env(t, func(w http.ResponseWriter, r *http.Request) {
		if !serveSignedIn(t, w, r) {
			serve(w, r)
		}
	}, "dev")
	t.Setenv("PATH", t.TempDir())
}

// installTunnelTools puts on the PATH the given tunnel tools (aws, session-manager-plugin; both
// when none is given), which only pass the checks that they are installed: they fail if run.
func installTunnelTools(t *testing.T, tools ...string) {
	t.Helper()
	if len(tools) == 0 {
		tools = []string{"aws", "session-manager-plugin"}
	}
	dir := t.TempDir()
	for _, tool := range tools {
		// #nosec G306 -- the fake tool must be executable.
		if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\necho \"unexpected "+tool+" $*\" >&2\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
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

func TestEnsureIdentityNeverSignsInWhenAWSRefusesTheConfiguration(t *testing.T) {
	refusals := map[string]string{
		"role":     "AccessDenied",         // AssumeRole: wrong role_arn, trust policy or MFA device.
		"dev":      "InvalidClientTokenId", // unknown access key.
		"badparam": "ValidationError",
	}
	awsapitest.Env(t, func(w http.ResponseWriter, r *http.Request) {
		code := refusals["dev"]
		if awsapitest.Operation(r) == "AssumeRole" {
			code = refusals[strings.TrimPrefix(r.FormValue("RoleArn"), "arn:aws:iam::111111111111:role/")]
		}
		awsapitest.RespondXML(t, w, http.StatusForbidden, `<ErrorResponse><Error><Type>Sender</Type><Code>`+code+`</Code>`+
			`<Message>refused</Message></Error></ErrorResponse>`)
	}, "dev")
	appendAWSConfig(t, "[profile role]\nregion = eu-west-3\nrole_arn = arn:aws:iam::111111111111:role/role\nsource_profile = dev\n\n"+
		"[profile badparam]\nregion = eu-west-3\nrole_arn = arn:aws:iam::111111111111:role/badparam\nsource_profile = dev\n")
	var logins atomic.Int32
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: countingInteractive(&logins)})
	for profile, code := range refusals {
		err := runner.ensureIdentity(context.Background(), awsConfig{Profile: profile, Region: "eu-west-3"}, true)
		if err == nil || errors.Is(err, ErrSignInRequired) || !strings.Contains(err.Error(), "AWS refused the credentials of profile "+profile) ||
			!strings.Contains(err.Error(), code) {
			t.Errorf("%s: err = %v, want AWS refusing the configuration with %s", profile, err, code)
		}
	}
	if logins.Load() != 0 {
		t.Fatal("a configuration AWS refuses must never trigger a sign-in")
	}
}

func TestMFAProfilesSignInWithACodeOnTheTerminal(t *testing.T) {
	installExpiredSTS(t).Store(true) // STS answers: only the missing MFA code fails.
	appendAWSConfig(t, "[profile mfa]\nregion = eu-west-3\nrole_arn = arn:aws:iam::111111111111:role/app\nsource_profile = dev\n"+
		"mfa_serial = arn:aws:iam::111111111111:mfa/dev\n")
	var unattended atomic.Int32
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Interactive: countingInteractive(&unattended)})
	ctx := context.Background()

	if err := runner.CheckIdentity(ctx, "mfa", "eu-west-3"); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("CheckIdentity: err = %v, want ErrSignInRequired", err)
	}
	if err := runner.ensureIdentity(ctx, awsConfig{Profile: "mfa", Region: "eu-west-3"}, false); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("unattended ensureIdentity: err = %v, want ErrSignInRequired", err)
	}
	if unattended.Load() != 0 {
		t.Fatal("no sign-in without a terminal")
	}

	// The terminal runs airlock sso-login for the MFA profile itself, not its source_profile.
	var args []string
	ctx = WithInteractive(ctx, func(ctx context.Context, _, _ string, a ...string) error {
		args = a
		return awsapi.SignInMFA(ctx, "mfa", func(string) (string, error) { return "123456", nil })
	})
	if err := runner.ensureIdentity(ctx, awsConfig{Profile: "mfa", Region: "eu-west-3"}, true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "sso-login --profile mfa" {
		t.Fatalf("interactive args = %v", args)
	}
	if err := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard}).CheckIdentity(context.Background(), "mfa", "eu-west-3"); err != nil {
		t.Fatalf("another runner must reuse the cached role: %v", err)
	}
}
