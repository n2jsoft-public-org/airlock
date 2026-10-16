package dbmcp

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/awstag"
)

// fakeAWS answers AWS CLI calls by their first two arguments, and records every call.
type fakeAWS struct {
	responses map[string]func(args []string) (string, error)
	calls     [][]string
}

func (f *fakeAWS) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	key := strings.Join(args[:2], " ")
	if respond, ok := f.responses[key]; ok {
		return respond(args)
	}
	return "", errors.New("unexpected aws call: " + strings.Join(args, " "))
}

func argAfter(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestIAMTokenProviderCachesAndRefreshesToken(t *testing.T) {
	issued := 0
	aws := &fakeAWS{responses: map[string]func([]string) (string, error){
		"rds generate-db-auth-token": func(args []string) (string, error) {
			issued++
			if argAfter(args, "--hostname") != "db.rds.amazonaws.com" || argAfter(args, "--port") != "5432" || argAfter(args, "--username") != "acme_ro" {
				t.Fatalf("unexpected args: %v", args)
			}
			return "token-" + strconv.Itoa(issued) + "\n", nil
		},
	}}
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	provider := newIAMTokenProvider(aws.run, "db.rds.amazonaws.com", 5432, "acme_ro", "app")
	provider.now = func() time.Time { return now }

	first, err := provider.Credentials(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if first != (Credentials{User: "acme_ro", Password: "token-1", Database: "app"}) {
		t.Fatalf("unexpected credentials: %+v", first)
	}

	now = now.Add(9 * time.Minute)
	second, err := provider.Credentials(context.Background(), "other")
	if err != nil {
		t.Fatal(err)
	}
	if second.Password != "token-1" || second.Database != "other" {
		t.Fatalf("expected cached token for other database, got %+v", second)
	}

	now = now.Add(time.Minute)
	third, err := provider.Credentials(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if third.Password != "token-2" {
		t.Fatalf("expected refreshed token after 10 minutes, got %q", third.Password)
	}
}

const testInstanceARN = "arn:aws:rds:eu-west-3:123:db:mssql-1"

var testSecretTag = awstag.Tag{Key: "AccessRole", Value: "ro-access"}

func supportSecretsAWS(secrets map[string]string) *fakeAWS {
	return &fakeAWS{responses: map[string]func([]string) (string, error){
		"secretsmanager list-secrets": func(args []string) (string, error) {
			arns := make([]string, 0, len(secrets))
			for arn := range secrets {
				arns = append(arns, `"`+arn+`"`)
			}
			return "[" + strings.Join(arns, ",") + "]", nil
		},
		"secretsmanager get-secret-value": func(args []string) (string, error) {
			return secrets[argAfter(args, "--secret-id")], nil
		},
	}}
}

func TestSupportSecretsProviderSingleDatabaseIsDefault(t *testing.T) {
	aws := supportSecretsAWS(map[string]string{
		"arn:s1": `{"username":"app_ro","password":"p1","dbname":"Archive"}`,
	})
	provider := newReadOnlySecretsProvider(aws.run, "mssql-1", testInstanceARN, testSecretTag)

	creds, err := provider.Credentials(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if creds != (Credentials{User: "app_ro", Password: "p1", Database: "Archive"}) {
		t.Fatalf("unexpected credentials: %+v", creds)
	}

	list := aws.calls[0]
	if got := argAfter(list, "--query"); !strings.Contains(got, "AccessRole") || !strings.Contains(got, testInstanceARN) {
		t.Fatalf("list-secrets query does not filter on ro-access secrets of the instance: %s", got)
	}

	if _, err := provider.Credentials(context.Background(), "Archive"); err != nil {
		t.Fatal(err)
	}
	if len(aws.calls) != 2 {
		t.Fatalf("secrets must be loaded once, got %d aws calls", len(aws.calls))
	}
}

func TestSupportSecretsProviderSeveralDatabases(t *testing.T) {
	aws := supportSecretsAWS(map[string]string{
		"arn:s1": `{"username":"u1","password":"p1","dbname":"Archive"}`,
		"arn:s2": `{"username":"u2","password":"p2","database":"Accounts"}`,
		"arn:s3": `{"username":"u3","password":"p3"}`,
	})
	provider := newReadOnlySecretsProvider(aws.run, "mssql-1", testInstanceARN, testSecretTag)

	names, err := provider.Databases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "Accounts,Archive" {
		t.Fatalf("databases = %v", names)
	}

	if _, err = provider.Credentials(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "Accounts, Archive") {
		t.Fatalf("expected an error listing databases, got %v", err)
	}
	creds, err := provider.Credentials(context.Background(), "Accounts")
	if err != nil || creds.User != "u2" {
		t.Fatalf("Accounts credentials = %+v, %v", creds, err)
	}
	if _, err = provider.Credentials(context.Background(), "Nope"); err == nil || !strings.Contains(err.Error(), "available: Accounts, Archive") {
		t.Fatalf("expected unknown database error, got %v", err)
	}
}

func TestSupportSecretsProviderNoSecretIsActionableAndRetried(t *testing.T) {
	aws := supportSecretsAWS(map[string]string{})
	provider := newReadOnlySecretsProvider(aws.run, "mssql-1", testInstanceARN, testSecretTag)

	_, err := provider.Credentials(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "no usable read-only secret (AccessRole=ro-access) found for mssql-1") || !strings.Contains(err.Error(), `tag from "modules.sql.readOnlySecretTag", see `+"`airlock config show`") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err = provider.Credentials(context.Background(), ""); err == nil {
		t.Fatal("expected the retried lookup to fail again")
	}
	if len(aws.calls) != 2 {
		t.Fatalf("a failed lookup must be retried, got %d calls", len(aws.calls))
	}
}

func TestMasterSecretProvider(t *testing.T) {
	aws := &fakeAWS{responses: map[string]func([]string) (string, error){
		"secretsmanager get-secret-value": func(args []string) (string, error) {
			if argAfter(args, "--secret-id") != "arn:master" {
				t.Fatalf("unexpected secret: %v", args)
			}
			return `{"username":"admin","password":"secret"}`, nil
		},
	}}
	provider := newMasterSecretProvider(aws.run, "pg-1", "arn:master", "postgres")

	creds, err := provider.Credentials(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if creds != (Credentials{User: "admin", Password: "secret", Database: "postgres"}) {
		t.Fatalf("unexpected credentials: %+v", creds)
	}
	if _, err = provider.Credentials(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	if len(aws.calls) != 1 {
		t.Fatalf("master secret must be read once, got %d calls", len(aws.calls))
	}
}

func TestMasterSecretProviderErrors(t *testing.T) {
	missing := newMasterSecretProvider(nil, "pg-1", "", "postgres")
	if _, err := missing.Credentials(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "no RDS-managed master user secret") {
		t.Fatalf("unexpected error: %v", err)
	}

	denied := &fakeAWS{responses: map[string]func([]string) (string, error){
		"secretsmanager get-secret-value": func([]string) (string, error) { return "", errors.New("AccessDeniedException") },
	}}
	provider := newMasterSecretProvider(denied.run, "pg-1", "arn:master", "postgres")
	if _, err := provider.Credentials(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "not readable: AccessDeniedException") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCredentialProviderUsesEnteredPasswordInsteadOfIAM(t *testing.T) {
	cfg := &SessionConfig{Target: "archive", DBName: "app", DBUser: "app_user", DBPassword: "s3cret", IAMAuth: true}

	provider, ok := credentialProvider(cfg, EnginePostgres).(*passwordProvider)
	if !ok {
		t.Fatalf("provider = %T, want the entered password", credentialProvider(cfg, EnginePostgres))
	}
	creds, err := provider.Credentials(context.Background(), "")
	if err != nil || creds != (Credentials{User: "app_user", Password: "s3cret", Database: "app"}) {
		t.Fatalf("credentials = %+v, err = %v", creds, err)
	}

	// Enabling writes keeps the entered password: no master user secret is read.
	cfg.AllowWrites = true
	if _, ok := credentialProvider(cfg, EnginePostgres).(*passwordProvider); !ok {
		t.Fatalf("provider = %T, want the entered password for writes too", credentialProvider(cfg, EnginePostgres))
	}
}

func TestCredentialProviderWithoutPasswordUsesIAMTokens(t *testing.T) {
	cfg := &SessionConfig{Target: "archive", DBUser: "acme_ro", IAMAuth: true}
	if _, ok := credentialProvider(cfg, EnginePostgres).(*iamTokenProvider); !ok {
		t.Fatalf("provider = %T, want IAM tokens", credentialProvider(cfg, EnginePostgres))
	}
}

func TestMasterSecretProviderRefreshReadsRotatedSecret(t *testing.T) {
	password := "old"
	aws := &fakeAWS{responses: map[string]func([]string) (string, error){
		"secretsmanager get-secret-value": func([]string) (string, error) {
			return `{"username":"admin","password":"` + password + `"}`, nil
		},
	}}
	provider := newMasterSecretProvider(aws.run, "pg-1", "arn:master", "postgres")
	if _, err := provider.Credentials(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	password = "rotated"
	provider.refresh()
	creds, err := provider.Credentials(context.Background(), "")
	if err != nil || creds.Password != "rotated" {
		t.Fatalf("credentials = %+v, %v; want the rotated password", creds, err)
	}
}

func TestSupportSecretsProviderRefreshReadsRotatedSecrets(t *testing.T) {
	secrets := map[string]string{"arn:s1": `{"username":"app_ro","password":"old","dbname":"Archive"}`}
	aws := supportSecretsAWS(secrets)
	provider := newReadOnlySecretsProvider(aws.run, "mssql-1", testInstanceARN, testSecretTag)
	if _, err := provider.Credentials(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	secrets["arn:s1"] = `{"username":"app_ro","password":"rotated","dbname":"Archive"}`
	provider.refresh()
	creds, err := provider.Credentials(context.Background(), "Archive")
	if err != nil || creds.Password != "rotated" {
		t.Fatalf("credentials = %+v, %v; want the rotated password", creds, err)
	}
}

func TestReadOnlySecretsQueryUsesTag(t *testing.T) {
	got := ReadOnlySecretsQuery(awstag.Tag{Key: "Role", Value: "reader"}, "arn:x")
	want := "SecretList[?Tags[?Key=='Role' && Value=='reader'] && Tags[?Key=='DBInstanceArn' && Value=='arn:x']].ARN"
	if got != want {
		t.Fatalf("ReadOnlySecretsQuery = %q, want %q", got, want)
	}
}

func secretValueAWS(values ...string) *fakeAWS {
	reads := 0
	return &fakeAWS{responses: map[string]func([]string) (string, error){
		"secretsmanager get-secret-value": func(args []string) (string, error) {
			if argAfter(args, "--secret-id") != "arn:app-password" {
				return "", errors.New("unexpected secret " + argAfter(args, "--secret-id"))
			}
			value := values[min(reads, len(values)-1)]
			reads++
			return value, nil
		},
	}}
}

func TestReadPasswordSecretJSONAndPlainText(t *testing.T) {
	json, err := ReadPasswordSecret(context.Background(), secretValueAWS(`{"username":"app_user","password":"s3cret"}`).run, "arn:app-password")
	if err != nil || json != (PasswordSecret{Username: "app_user", Password: "s3cret"}) {
		t.Fatalf("JSON secret = %+v, %v", json, err)
	}
	plain, err := ReadPasswordSecret(context.Background(), secretValueAWS("s3cret").run, "arn:app-password")
	if err != nil || plain != (PasswordSecret{Password: "s3cret"}) {
		t.Fatalf("plain secret = %+v, %v", plain, err)
	}
	if _, err := ReadPasswordSecret(context.Background(), secretValueAWS(`{"username":"app_user"}`).run, "arn:app-password"); err == nil || !strings.Contains(err.Error(), "has no password") {
		t.Fatalf("JSON without password = %v", err)
	}
	if _, err := ReadPasswordSecret(context.Background(), secretValueAWS(`{"username":`).run, "arn:app-password"); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("broken JSON = %v", err)
	}
}

func TestSecretPasswordProviderUsesSecretUserElseTypedUser(t *testing.T) {
	aws := secretValueAWS(`{"username":"app_user","password":"s3cret"}`)
	provider := newSecretPasswordProvider(aws.run, "arn:app-password", "typed_user", "app")
	creds, err := provider.Credentials(context.Background(), "")
	if err != nil || creds != (Credentials{User: "app_user", Password: "s3cret", Database: "app"}) {
		t.Fatalf("credentials = %+v, %v", creds, err)
	}
	if _, err = provider.Credentials(context.Background(), "other"); err != nil {
		t.Fatalf("second read: %v", err)
	}
	if len(aws.calls) != 1 {
		t.Fatalf("the secret must be read once, got %d reads", len(aws.calls))
	}

	plain := newSecretPasswordProvider(secretValueAWS("s3cret").run, "arn:app-password", "typed_user", "app")
	creds, err = plain.Credentials(context.Background(), "")
	if err != nil || creds.User != "typed_user" || creds.Password != "s3cret" {
		t.Fatalf("plain credentials = %+v, %v", creds, err)
	}

	noUser := newSecretPasswordProvider(secretValueAWS("s3cret").run, "arn:app-password", "", "app")
	if _, err := noUser.Credentials(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "has no username") {
		t.Fatalf("plain secret without user = %v", err)
	}
}

func TestSecretPasswordProviderRefreshReadsRotatedSecret(t *testing.T) {
	aws := secretValueAWS(`{"username":"app_user","password":"old"}`, `{"username":"app_user","password":"new"}`)
	provider := newSecretPasswordProvider(aws.run, "arn:app-password", "", "app")
	first, err := provider.Credentials(context.Background(), "")
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	provider.refresh()
	second, err := provider.Credentials(context.Background(), "")
	if err != nil || first.Password != "old" || second.Password != "new" {
		t.Fatalf("first = %+v second = %+v err = %v", first, second, err)
	}
}

func TestSecretPasswordProviderExplainsFailures(t *testing.T) {
	denied := &fakeAWS{responses: map[string]func([]string) (string, error){
		"secretsmanager get-secret-value": func([]string) (string, error) { return "", errors.New("AccessDeniedException") },
	}}
	provider := newSecretPasswordProvider(denied.run, "arn:app-password", "", "app")
	if _, err := provider.Credentials(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "secretsmanager:GetSecretValue") {
		t.Fatalf("denied read = %v", err)
	}
	if hint := provider.authFailureHint(); !strings.Contains(hint, "arn:app-password") {
		t.Fatalf("hint = %q, want the secret named", hint)
	}
}

func TestReadPasswordSecretBrokenJSONQuotesNoCharacterOfTheSecret(t *testing.T) {
	// The JSON syntax error would quote the first unexpected character: here one of the password.
	_, err := ReadPasswordSecret(context.Background(), secretValueAWS(`{"username":"app","password":Zs3cret}`).run, "arn:app-password")
	if err == nil || err.Error() != "secret arn:app-password is not valid JSON" {
		t.Fatalf("broken JSON = %v", err)
	}
}

func TestReadSecretBrokenJSONQuotesNoCharacterOfTheSecret(t *testing.T) {
	_, err := readSecret(context.Background(), secretValueAWS(`{"username":"app","password":Zs3cret}`).run, "arn:app-password")
	if err == nil || err.Error() != "secret arn:app-password is not a JSON credential" {
		t.Fatalf("broken JSON = %v", err)
	}
}
