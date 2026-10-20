package jumphost

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/awstag"
	"github.com/n2jsoft/airlock/internal/dbmcp"
)

const (
	integSecret = "arn:aws:secretsmanager:eu-west-3:111111111111:secret:dev/rds/read-only/archive-integ-uLQ3NB"
	mssqlSecret = "arn:aws:secretsmanager:eu-west-3:111111111111:secret:dev/rds/read-only/archive-mssql-xyKAqI"
)

// newTestSignIn signs in against a Secrets Manager holding the two read-only secrets.
func newTestSignIn(t *testing.T, target *ideTarget) *sessionSignIn {
	t.Helper()
	fake := installSecretsManager(t, map[string]string{
		integSecret: `{"username":"app_ro_integ","password":"integ-Pa55"}`,
		mssqlSecret: `{"username":"app_ro_mssql","password":"mssql-Pa55"}`,
	}, mssqlSecret, integSecret)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, ReadOnlySecretTag: awstag.Tag{Key: "Role", Value: "read-only"}})
	return &sessionSignIn{
		runner: runner, session: &Session{}, awsCfg: fake.cfg,
		cfg:          awsConfig{Profile: "dev", Region: "eu-west-3", Args: []string{"--profile", "dev", "--region", "eu-west-3"}},
		target:       *target,
		resourceType: resourceRDS, instance: "archive", localPort: "1433",
	}
}

func TestSignInCandidatesFollowTheSignInMethod(t *testing.T) {
	for name, tc := range map[string]struct { //nolint:govet // test struct
		target ideTarget
		want   []SignInCandidate
	}{
		"picked secret": {ideTarget{Engine: "postgres", DBUser: "app", DBSecretARN: integSecret}, []SignInCandidate{{Label: "archive-integ", User: "app", SecretARN: integSecret}}},
		"password":      {ideTarget{Engine: "postgres", DBUser: "app", DBPassword: "typed"}, []SignInCandidate{{Label: "password", User: "app"}}},
		"AWS IAM":       {ideTarget{Engine: "postgres", DBUser: "app_ro", IAMAuth: true}, []SignInCandidate{{Label: "AWS IAM", User: "app_ro"}}},
		"auto": {ideTarget{Engine: "sqlserver-web", InstanceARN: secretsInstanceARN, MasterSecretARN: "arn:master"}, []SignInCandidate{
			{Label: "archive-integ", User: "app_ro_integ", SecretARN: integSecret, Discovered: true},
			{Label: "archive-mssql", User: "app_ro_mssql", SecretARN: mssqlSecret, Discovered: true},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := newTestSignIn(t, &tc.target).Candidates(context.Background())
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("Candidates = %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
}

func TestSignInCheckSignsInWithTheCandidate(t *testing.T) {
	var got dbmcp.Credentials
	var gotEngine string
	var gotPort int
	restore := checkSignIn
	checkSignIn = func(ctx context.Context, engine string, creds dbmcp.CredentialProvider, port int) error {
		gotEngine, gotPort = engine, port
		var err error
		got, err = creds.Credentials(ctx, "")
		return err
	}
	t.Cleanup(func() { checkSignIn = restore })

	for name, tc := range map[string]struct {
		candidate SignInCandidate
		want      dbmcp.Credentials
		target    ideTarget
	}{
		"secret":   {SignInCandidate{User: "app_ro_integ", SecretARN: integSecret}, dbmcp.Credentials{User: "app_ro_integ", Password: "integ-Pa55"}, ideTarget{Engine: "sqlserver-web"}},
		"password": {SignInCandidate{User: "app"}, dbmcp.Credentials{User: "app", Password: "typed", Database: "postgres"}, ideTarget{Engine: "postgres", DBUser: "app", DBPassword: "typed"}},
		"AWS IAM":  {SignInCandidate{User: "app_ro"}, dbmcp.Credentials{User: "app_ro", Password: "iam-token", Database: "app"}, ideTarget{Engine: "postgres", DBUser: "app_ro", IAMAuth: true, RDSHost: "archive.rds", RDSPort: 5432, Database: "app"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := newTestSignIn(t, &tc.target).Check(context.Background(), tc.candidate); err != nil {
				t.Fatal(err)
			}
			if tc.target.IAMAuth { // the token is signed: check it names the endpoint, then compare the rest.
				if !strings.HasPrefix(got.Password, "archive.rds:5432?Action=connect&DBUser=app_ro") {
					t.Fatalf("IAM token = %q", got.Password)
				}
				got.Password = "iam-token"
			}
			if got != tc.want || gotEngine != tc.target.Engine || gotPort != 1433 {
				t.Fatalf("signed in with %+v on %s:%d, want %+v on %s:1433", got, gotEngine, gotPort, tc.want, tc.target.Engine)
			}
		})
	}
}

func TestSignInUseSwitchesTheSessionToTheSecret(t *testing.T) {
	s := newTestSignIn(t, &ideTarget{Engine: "sqlserver-web", InstanceARN: secretsInstanceARN})
	var ideUser string
	s.updateIDE = func(user string) { ideUser = user }

	options := Options{Name: "archive", SignIn: SignInAuto, LocalPortNumber: 1433}
	s.Use(SignInCandidate{Label: "archive-integ", User: "app_ro_integ", SecretARN: integSecret, Discovered: true}, &options)

	if options.SignIn != SignInSecret || options.DBSecretARN != integSecret || options.DBUser != "app_ro_integ" || options.Name != "archive" {
		t.Fatalf("options = %+v, want the secret sign-in", options)
	}
	details := s.session.Details
	if details.Auth != "Secrets Manager · dev/rds/read-only/archive-integ · app_ro_integ" {
		t.Fatalf("Auth = %q", details.Auth)
	}
	if !strings.Contains(details.RestartCommand, "--db-secret "+integSecret) || !strings.Contains(details.RestartCommand, "--db-user app_ro_integ") {
		t.Fatalf("RestartCommand = %q, want the secret and its user", details.RestartCommand)
	}
	if ideUser != "app_ro_integ" {
		t.Fatalf("IDE user = %q", ideUser)
	}
	creds, err := s.session.ReadCredentials(context.Background())
	if err != nil || len(creds) != 1 || creds[0].Password != "integ-Pa55" {
		t.Fatalf("p reads %+v, %v, want the chosen secret only", creds, err)
	}
}

func TestSignInUseKeepsASessionAlreadySigningInWithTheCandidate(t *testing.T) {
	s := newTestSignIn(t, &ideTarget{Engine: "postgres", DBUser: "app_ro", IAMAuth: true})
	s.session.Details.Auth = "AWS IAM token (generated by airlock) · app_ro"
	options := Options{Name: "archive", DBUser: "app_ro"}
	got := options

	s.Use(SignInCandidate{Label: "AWS IAM", User: "app_ro"}, &got)
	if got != options {
		t.Fatalf("options = %+v, want them unchanged", got)
	}
	if s.session.Details.Auth != "AWS IAM token (generated by airlock) · app_ro" {
		t.Fatalf("Auth = %q, want it unchanged", s.session.Details.Auth)
	}
}

func TestSignInForgetsOnlyARejectedSavedSecretWhereAutoApplies(t *testing.T) {
	saved := SignInCandidate{Label: "archive-integ", User: "app_ro_integ", SecretARN: integSecret}
	options := Options{Name: "archive", SignIn: SignInSecret, DBSecretARN: integSecret, DBUser: "app_ro_integ"}

	sqlServer := newTestSignIn(t, &ideTarget{Engine: "sqlserver-web", DBSecretARN: integSecret, DBUser: "app_ro_integ", InstanceARN: secretsInstanceARN})
	got := options
	ok := sqlServer.Forget(saved, &got)
	if !ok || got.SignIn != SignInAuto || got.DBSecretARN != "" || got.DBUser != "" || got.Name != "archive" {
		t.Fatalf("Forget = %+v, %v, want the Auto sign-in", got, ok)
	}
	// The next candidates are the read-only secrets, no longer the forgotten one alone.
	if candidates, err := sqlServer.Candidates(context.Background()); err != nil || len(candidates) != 2 {
		t.Fatalf("Candidates after Forget = %+v, %v, want the read-only secrets", candidates, err)
	}

	unchanged := options
	if newTestSignIn(t, &ideTarget{Engine: "postgres", DBSecretARN: integSecret}).Forget(saved, &unchanged) || unchanged != options {
		t.Fatal("PostgreSQL has no Auto sign-in to fall back to")
	}
	discovered := saved
	discovered.Discovered = true
	if sqlServer.Forget(discovered, &unchanged) {
		t.Fatal("a discovered secret was never saved")
	}
}

func TestSignInAppliesToQueryableSessionsOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		target ideTarget
		writes bool
		want   bool
	}{
		"postgres IAM":              {ideTarget{Engine: "postgres", IAMAuth: true}, false, true},
		"sql server auto":           {ideTarget{Engine: "sqlserver-web"}, false, true},
		"sql server auto, writes":   {ideTarget{Engine: "sqlserver-web"}, true, false},
		"sql server secret, writes": {ideTarget{Engine: "sqlserver-web", DBSecretARN: integSecret}, true, true},
		"mysql password":            {ideTarget{Engine: "mysql", DBPassword: "typed"}, false, true},
		"oracle":                    {ideTarget{Engine: "oracle-ee", DBPassword: "typed"}, false, false},
	} {
		if got := signInApplies(&tc.target, tc.writes); got != tc.want {
			t.Errorf("%s: signInApplies = %v, want %v", name, got, tc.want)
		}
	}
}

func TestSignInKeepsARejectedSecretWhenMCPWritesAreOn(t *testing.T) {
	// Auto with writes signs in with the master user secret: falling back to it would silently
	// hand the MCP server administrator access on the next reconnection.
	s := newTestSignIn(t, &ideTarget{Engine: "sqlserver-web", DBSecretARN: integSecret, DBUser: "app_ro_integ", InstanceARN: secretsInstanceARN})
	options := Options{Name: "archive", SignIn: SignInSecret, DBSecretARN: integSecret, DBUser: "app_ro_integ", MCPAllowWrites: true}
	got := options

	if s.Forget(SignInCandidate{Label: "archive-integ", User: "app_ro_integ", SecretARN: integSecret}, &got) || got != options {
		t.Fatalf("Forget = %+v, want the saved secret kept with MCP writes", got)
	}
}
