package dbmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft/airlock/internal/awstag"
)

// respond writes the JSON reply of a fake AWS API.
func respond(t testing.TB, w http.ResponseWriter, status int, body any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Error(err)
		return
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	if _, err := w.Write(raw); err != nil {
		t.Error(err)
	}
}

// secretIDOf is the SecretId of a GetSecretValue request.
func secretIDOf(t testing.TB, r *http.Request) string {
	t.Helper()
	var body struct {
		SecretID string `json:"SecretId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Error(err)
	}
	return body.SecretID
}

const testInstanceARN = "arn:aws:rds:eu-west-3:123:db:mssql-1"

var testSecretTag = awstag.Tag{Key: "AccessRole", Value: "ro-access"}

// secretsAWS is a fake Secrets Manager holding secrets (ARN to SecretString) that records every
// operation it serves. Every listed secret carries the tags of the read-only secrets of
// testInstanceARN.
type secretsAWS struct {
	t       testing.TB
	secrets map[string]string
	// fail maps an operation to the error code it answers.
	fail map[string]string
	ops  []string
	mu   sync.Mutex
}

func (f *secretsAWS) serve(w http.ResponseWriter, r *http.Request) {
	op := awsapitest.Operation(r)
	f.mu.Lock()
	f.ops = append(f.ops, op)
	secrets := maps.Clone(f.secrets)
	code := f.fail[op]
	f.mu.Unlock()

	switch op {
	case "GetSecretValue":
		id := secretIDOf(f.t, r)
		value, ok := secrets[id]
		switch {
		case code != "":
			respond(f.t, w, http.StatusBadRequest, map[string]string{"__type": code, "message": "denied"})
		case !ok:
			respond(f.t, w, http.StatusBadRequest, map[string]string{"__type": "ResourceNotFoundException", "message": "no such secret"})
		default:
			respond(f.t, w, http.StatusOK, map[string]string{"SecretString": value})
		}
	case "ListSecrets":
		if code != "" {
			respond(f.t, w, http.StatusBadRequest, map[string]string{"__type": code, "message": "denied"})
			return
		}
		list := make([]map[string]any, 0, len(secrets))
		for _, arn := range slices.Sorted(maps.Keys(secrets)) {
			list = append(list, map[string]any{"ARN": arn, "Tags": []map[string]string{
				{"Key": testSecretTag.Key, "Value": testSecretTag.Value}, {"Key": "DBInstanceArn", "Value": testInstanceARN},
			}})
		}
		respond(f.t, w, http.StatusOK, map[string]any{"SecretList": list})
	default:
		respond(f.t, w, http.StatusBadRequest, map[string]string{"__type": "UnknownOperationException", "message": op})
	}
}

func (f *secretsAWS) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(slices.DeleteFunc(slices.Clone(f.ops), func(o string) bool { return o != op }))
}

// newSecretsAWS points the SDK at a fake Secrets Manager holding secrets.
func newSecretsAWS(t *testing.T, secrets map[string]string) (*secretsAWS, *aws.Config) {
	t.Helper()
	fake := &secretsAWS{t: t, secrets: secrets, fail: map[string]string{}}
	return fake, testAWSConfig(t, fake.serve)
}

// testAWSConfig is the SDK configuration of profile dev, served by handler.
func testAWSConfig(t *testing.T, handler http.HandlerFunc) *aws.Config {
	t.Helper()
	awsapitest.Env(t, handler, "dev")
	cfg, err := awsapi.NewConfigs().Get(context.Background(), "dev", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	return &cfg
}

func TestIAMTokenIsSignedForTheEndpoint(t *testing.T) {
	cfg := testAWSConfig(t, func(http.ResponseWriter, *http.Request) {})
	provider := newIAMTokenProvider(cfg, "db.rds.amazonaws.com", 5432, "user", "app")

	creds, err := provider.Credentials(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(creds.Password, "db.rds.amazonaws.com:5432?Action=connect&DBUser=user") ||
		!strings.Contains(creds.Password, "X-Amz-Credential=AKIDTEST") {
		t.Fatalf("token = %q, want a token signed by AKIDTEST for the endpoint", creds.Password)
	}
}

func TestIAMTokenProviderCachesAndRefreshesToken(t *testing.T) {
	signed := 0
	cfg := &aws.Config{Region: "eu-west-3", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		signed++
		return aws.Credentials{AccessKeyID: "AKIDTEST", SecretAccessKey: "secret"}, nil
	})}
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	provider := newIAMTokenProvider(cfg, "db.rds.amazonaws.com", 5432, "acme_ro", "app")
	provider.now = func() time.Time { return now }

	first, err := provider.Credentials(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if first.User != "acme_ro" || first.Database != "app" || first.Password == "" {
		t.Fatalf("unexpected credentials: %+v", first)
	}

	now = now.Add(9 * time.Minute)
	second, err := provider.Credentials(context.Background(), "other")
	if err != nil {
		t.Fatal(err)
	}
	if second.Password != first.Password || second.Database != "other" || signed != 1 {
		t.Fatalf("expected cached token for other database, got %+v after %d signatures", second, signed)
	}

	now = now.Add(time.Minute)
	if _, err = provider.Credentials(context.Background(), ""); err != nil || signed != 2 {
		t.Fatalf("expected a new token after 10 minutes: %d signatures, %v", signed, err)
	}
}

func TestIAMTokenProviderTellsAnExpiredSignIn(t *testing.T) {
	cfg := &aws.Config{Region: "eu-west-3", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, fmt.Errorf("token expired")
	})}
	_, err := newIAMTokenProvider(cfg, "db.rds.amazonaws.com", 5432, "acme_ro", "app").Credentials(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "acme_ro") || !strings.Contains(err.Error(), "sign in again") {
		t.Fatalf("err = %v, want the user and the sign-in hint", err)
	}
}

func TestSupportSecretsProviderSingleDatabaseIsDefault(t *testing.T) {
	fake, cfg := newSecretsAWS(t, map[string]string{
		"arn:s1": `{"username":"app_ro","password":"p1","dbname":"Archive"}`,
	})
	provider := newReadOnlySecretsProvider(cfg, "mssql-1", testInstanceARN, testSecretTag)

	creds, err := provider.Credentials(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if creds != (Credentials{User: "app_ro", Password: "p1", Database: "Archive"}) {
		t.Fatalf("unexpected credentials: %+v", creds)
	}

	if _, err := provider.Credentials(context.Background(), "Archive"); err != nil {
		t.Fatal(err)
	}
	if fake.count("ListSecrets") != 1 || fake.count("GetSecretValue") != 1 {
		t.Fatalf("secrets must be loaded once, got operations %v", fake.ops)
	}
}

func TestSupportSecretsProviderSeveralDatabases(t *testing.T) {
	_, cfg := newSecretsAWS(t, map[string]string{
		"arn:s1": `{"username":"u1","password":"p1","dbname":"Archive"}`,
		"arn:s2": `{"username":"u2","password":"p2","database":"Accounts"}`,
		"arn:s3": `{"username":"u3","password":"p3"}`,
	})
	provider := newReadOnlySecretsProvider(cfg, "mssql-1", testInstanceARN, testSecretTag)

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
	fake, cfg := newSecretsAWS(t, map[string]string{})
	provider := newReadOnlySecretsProvider(cfg, "mssql-1", testInstanceARN, testSecretTag)

	_, err := provider.Credentials(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "no usable read-only secret (AccessRole=ro-access) found for mssql-1") || !strings.Contains(err.Error(), `tag from "modules.sql.readOnlySecretTag", see `+"`airlock config show`") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err = provider.Credentials(context.Background(), ""); err == nil {
		t.Fatal("expected the retried lookup to fail again")
	}
	if fake.count("ListSecrets") != 2 {
		t.Fatalf("a failed lookup must be retried, got operations %v", fake.ops)
	}
}

func TestReadOnlySecretARNsNeedsBothTags(t *testing.T) {
	tag := awstag.Tag{Key: "Role", Value: "reader"}
	entry := func(arn string, tags ...types.Tag) types.SecretListEntry {
		return types.SecretListEntry{ARN: aws.String(arn), Tags: tags}
	}
	role := types.Tag{Key: aws.String("Role"), Value: aws.String("reader")}
	owner := func(arn string) types.Tag { return types.Tag{Key: aws.String("DBInstanceArn"), Value: aws.String(arn)} }

	got := readOnlySecretARNs([]types.SecretListEntry{
		entry("both", role, owner("arn:x")),
		entry("tag-only", role),
		entry("arn-only", owner("arn:x")),
		entry("other-instance", role, owner("arn:y")),
	}, tag, "arn:x")
	if !slices.Equal(got, []string{"both"}) {
		t.Fatalf("ARNs = %v, want only the secret carrying both tags", got)
	}
}

func TestListReadOnlySecretsListsEveryPage(t *testing.T) {
	var filters []string
	tags := []map[string]string{{"Key": "AccessRole", "Value": "ro-access"}, {"Key": "DBInstanceArn", "Value": testInstanceARN}}
	cfg := testAWSConfig(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NextToken string
			Filters   []struct {
				Key    string
				Values []string
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.NextToken == "" {
			for _, f := range body.Filters {
				filters = append(filters, f.Key+"="+strings.Join(f.Values, ","))
			}
			respond(t, w, http.StatusOK, map[string]any{"SecretList": []map[string]any{{"ARN": "arn:page1", "Tags": tags}}, "NextToken": "more"})
			return
		}
		respond(t, w, http.StatusOK, map[string]any{"SecretList": []map[string]any{{"ARN": "arn:page2", "Tags": tags}}})
	})

	arns, err := listReadOnlySecrets(context.Background(), cfg, "mssql-1", testInstanceARN, testSecretTag)
	if err != nil || !slices.Equal(arns, []string{"arn:page1", "arn:page2"}) {
		t.Fatalf("ARNs = %v, %v, want both pages", arns, err)
	}
	if want := []string{"tag-key=DBInstanceArn", "tag-value=" + testInstanceARN}; !slices.Equal(filters, want) {
		t.Fatalf("filters = %v, want %v", filters, want)
	}
}

func TestMasterSecretProvider(t *testing.T) {
	fake, cfg := newSecretsAWS(t, map[string]string{"arn:master": `{"username":"admin","password":"secret"}`})
	provider := newMasterSecretProvider(cfg, "pg-1", "arn:master", "postgres")

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
	if fake.count("GetSecretValue") != 1 {
		t.Fatalf("master secret must be read once, got operations %v", fake.ops)
	}
}

func TestMasterSecretProviderErrors(t *testing.T) {
	missing := newMasterSecretProvider(nil, "pg-1", "", "postgres")
	if _, err := missing.Credentials(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "no RDS-managed master user secret") {
		t.Fatalf("unexpected error: %v", err)
	}

	fake, cfg := newSecretsAWS(t, map[string]string{"arn:master": "{}"})
	fake.fail["GetSecretValue"] = "AccessDeniedException"
	provider := newMasterSecretProvider(cfg, "pg-1", "arn:master", "postgres")
	if _, err := provider.Credentials(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "not readable") || !strings.Contains(err.Error(), "AccessDeniedException") {
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
	fake, cfg := newSecretsAWS(t, map[string]string{"arn:master": `{"username":"admin","password":"old"}`})
	provider := newMasterSecretProvider(cfg, "pg-1", "arn:master", "postgres")
	if _, err := provider.Credentials(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	fake.mu.Lock()
	fake.secrets["arn:master"] = `{"username":"admin","password":"rotated"}`
	fake.mu.Unlock()
	provider.refresh()
	creds, err := provider.Credentials(context.Background(), "")
	if err != nil || creds.Password != "rotated" {
		t.Fatalf("credentials = %+v, %v; want the rotated password", creds, err)
	}
}

func TestSupportSecretsProviderRefreshReadsRotatedSecrets(t *testing.T) {
	fake, cfg := newSecretsAWS(t, map[string]string{"arn:s1": `{"username":"app_ro","password":"old","dbname":"Archive"}`})
	provider := newReadOnlySecretsProvider(cfg, "mssql-1", testInstanceARN, testSecretTag)
	if _, err := provider.Credentials(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	fake.mu.Lock()
	fake.secrets["arn:s1"] = `{"username":"app_ro","password":"rotated","dbname":"Archive"}`
	fake.mu.Unlock()
	provider.refresh()
	creds, err := provider.Credentials(context.Background(), "Archive")
	if err != nil || creds.Password != "rotated" {
		t.Fatalf("credentials = %+v, %v; want the rotated password", creds, err)
	}
}

// passwordSecretAWS holds the secret arn:app-password with value.
func passwordSecretAWS(t *testing.T, value string) (*secretsAWS, *aws.Config) {
	t.Helper()
	return newSecretsAWS(t, map[string]string{"arn:app-password": value})
}

func TestReadPasswordSecretJSONAndPlainText(t *testing.T) {
	read := func(value string) (PasswordSecret, error) {
		_, cfg := passwordSecretAWS(t, value)
		return ReadPasswordSecret(context.Background(), cfg, "arn:app-password")
	}
	jsonSecret, err := read(`{"username":"app_user","password":"s3cret"}`)
	if err != nil || jsonSecret != (PasswordSecret{Username: "app_user", Password: "s3cret"}) {
		t.Fatalf("JSON secret = %+v, %v", jsonSecret, err)
	}
	plain, err := read("s3cret")
	if err != nil || plain != (PasswordSecret{Password: "s3cret"}) {
		t.Fatalf("plain secret = %+v, %v", plain, err)
	}
	// A secret made from a file ends with a newline: the AWS CLI used to trim it.
	if trimmed, err := read("s3cret\n"); err != nil || trimmed != (PasswordSecret{Password: "s3cret"}) {
		t.Fatalf("plain secret with newline = %+v, %v", trimmed, err)
	}
	if _, err := read(`{"username":"app_user"}`); err == nil || !strings.Contains(err.Error(), "has no password") {
		t.Fatalf("JSON without password = %v", err)
	}
	if _, err := read(`{"username":`); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("broken JSON = %v", err)
	}
}

func TestSecretPasswordProviderUsesSecretUserElseTypedUser(t *testing.T) {
	fake, cfg := passwordSecretAWS(t, `{"username":"app_user","password":"s3cret"}`)
	provider := newSecretPasswordProvider(cfg, "arn:app-password", "typed_user", "app")
	creds, err := provider.Credentials(context.Background(), "")
	if err != nil || creds != (Credentials{User: "app_user", Password: "s3cret", Database: "app"}) {
		t.Fatalf("credentials = %+v, %v", creds, err)
	}
	if _, err = provider.Credentials(context.Background(), "other"); err != nil {
		t.Fatalf("second read: %v", err)
	}
	if fake.count("GetSecretValue") != 1 {
		t.Fatalf("the secret must be read once, got operations %v", fake.ops)
	}

	_, plainCfg := passwordSecretAWS(t, "s3cret")
	plain := newSecretPasswordProvider(plainCfg, "arn:app-password", "typed_user", "app")
	creds, err = plain.Credentials(context.Background(), "")
	if err != nil || creds.User != "typed_user" || creds.Password != "s3cret" {
		t.Fatalf("plain credentials = %+v, %v", creds, err)
	}

	_, noUserCfg := passwordSecretAWS(t, "s3cret")
	noUser := newSecretPasswordProvider(noUserCfg, "arn:app-password", "", "app")
	if _, err := noUser.Credentials(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "has no username") {
		t.Fatalf("plain secret without user = %v", err)
	}
}

func TestSecretPasswordProviderRefreshReadsRotatedSecret(t *testing.T) {
	fake, cfg := passwordSecretAWS(t, `{"username":"app_user","password":"old"}`)
	provider := newSecretPasswordProvider(cfg, "arn:app-password", "", "app")
	first, err := provider.Credentials(context.Background(), "")
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	fake.mu.Lock()
	fake.secrets["arn:app-password"] = `{"username":"app_user","password":"new"}`
	fake.mu.Unlock()
	provider.refresh()
	second, err := provider.Credentials(context.Background(), "")
	if err != nil || first.Password != "old" || second.Password != "new" {
		t.Fatalf("first = %+v second = %+v err = %v", first, second, err)
	}
}

func TestSecretPasswordProviderExplainsFailures(t *testing.T) {
	fake, cfg := passwordSecretAWS(t, "s3cret")
	fake.fail["GetSecretValue"] = "AccessDeniedException"
	provider := newSecretPasswordProvider(cfg, "arn:app-password", "", "app")
	if _, err := provider.Credentials(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "secretsmanager:GetSecretValue") {
		t.Fatalf("denied read = %v", err)
	}
	if hint := provider.authFailureHint(); !strings.Contains(hint, "arn:app-password") {
		t.Fatalf("hint = %q, want the secret named", hint)
	}
}

func TestReadPasswordSecretBrokenJSONQuotesNoCharacterOfTheSecret(t *testing.T) {
	// The JSON syntax error would quote the first unexpected character: here one of the password.
	_, cfg := passwordSecretAWS(t, `{"username":"app","password":Zs3cret}`)
	_, err := ReadPasswordSecret(context.Background(), cfg, "arn:app-password")
	if err == nil || err.Error() != "secret arn:app-password is not valid JSON" {
		t.Fatalf("broken JSON = %v", err)
	}
}

func TestReadSecretBrokenJSONQuotesNoCharacterOfTheSecret(t *testing.T) {
	_, cfg := passwordSecretAWS(t, `{"username":"app","password":Zs3cret}`)
	_, err := readSecret(context.Background(), cfg, "arn:app-password")
	if err == nil || err.Error() != "secret arn:app-password is not a JSON credential" {
		t.Fatalf("broken JSON = %v", err)
	}
}

func TestReadSecretOfAnExpiredSignInSaysSignInAgain(t *testing.T) {
	cfg := &aws.Config{Region: "eu-west-3", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, fmt.Errorf("token expired")
	})}
	_, err := ReadPasswordSecret(context.Background(), cfg, "arn:app-password")
	if err == nil || !strings.Contains(err.Error(), "sign in again") {
		t.Fatalf("err = %v, want the sign-in failure", err)
	}
}
