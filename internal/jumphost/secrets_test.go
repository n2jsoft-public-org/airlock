package jumphost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft/airlock/internal/awstag"
)

const secretsInstanceARN = "arn:aws:rds:eu-west-3:111111111111:db:archive"

func TestRankDBSecretsPutsLinkedSecretsFirst(t *testing.T) {
	var entries []smtypes.SecretListEntry
	raw := `[
		{"ARN":"arn:z","Name":"zeta/other","Description":""},
		{"ARN":"arn:tag","Name":"team/app-ro","Tags":[{"Key":"DBInstanceArn","Value":"` + secretsInstanceARN + `"}]},
		{"ARN":"arn:master","Name":"rds!db-1234","Description":"master"},
		{"ARN":"arn:name","Name":"prod/Archive/app-user"},
		{"ARN":"arn:a","Name":"alpha/other"}
	]`
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatal(err)
	}

	got := rankDBSecrets(entries, "archive", secretsInstanceARN, "arn:master")
	want := []struct {
		name   string
		linked bool
	}{
		{"prod/Archive/app-user", true}, {"rds!db-1234", true}, {"team/app-ro", true},
		{"alpha/other", false}, {"zeta/other", false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for idx, w := range want {
		if got[idx].Name != w.name || got[idx].Linked != w.linked {
			t.Fatalf("got[%d] = %+v, want %s linked=%v", idx, got[idx], w.name, w.linked)
		}
	}
	if got[1].Description != "master" || got[1].ARN != "arn:master" {
		t.Fatalf("summary lost its fields: %+v", got[1])
	}
}

func TestSecretDisplayName(t *testing.T) {
	cases := map[string]string{
		"arn:aws:secretsmanager:eu-west-3:111111111111:secret:prod/app-user-AbC123":    "prod/app-user",
		"arn:aws:secretsmanager:eu-west-3:111111111111:secret:rds!db-1234-5678-xYz9Qw": "rds!db-1234-5678",
		"prod/app-user": "prod/app-user",
		"":              "",
	}
	for id, want := range cases {
		if got := SecretDisplayName(id); got != want {
			t.Errorf("SecretDisplayName(%q) = %q, want %q", id, got, want)
		}
	}
}

// secretConfig is the SDK configuration of a Secrets Manager holding arn:secret as value; no
// secret at all when failing.
func secretConfig(t *testing.T, value string, present bool) *aws.Config {
	t.Helper()
	values := map[string]string{}
	if present {
		values["arn:secret"] = value
	}
	return installSecretsManager(t, values).cfg
}

func TestResolveSecretSignInPrefersSecretUser(t *testing.T) {
	user, arn, err := resolveSecretSignIn(context.Background(), secretConfig(t, `{"username":"app_user","password":"s3cret"}`, true), "arn:secret", "typed")
	if err != nil || user != "app_user" || arn != "arn:secret" {
		t.Fatalf("user = %q, err = %v", user, err)
	}
}

func TestResolveSecretSignInPlainTextNeedsUser(t *testing.T) {
	if user, _, err := resolveSecretSignIn(context.Background(), secretConfig(t, "s3cret", true), "arn:secret", " typed "); err != nil || user != "typed" {
		t.Fatalf("user = %q, err = %v", user, err)
	}
	_, _, err := resolveSecretSignIn(context.Background(), secretConfig(t, "s3cret", true), "arn:secret", "")
	if err == nil || !strings.Contains(err.Error(), "has no username: fill the User field") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveSecretSignInExplainsUnreadableSecret(t *testing.T) {
	_, _, err := resolveSecretSignIn(context.Background(), secretConfig(t, "", false), "arn:secret", "typed")
	for _, want := range []string{"arn:secret", "secretsmanager:GetSecretValue", "kms:Decrypt", "ResourceNotFoundException"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
	}
}

func TestMCPEngineSupported(t *testing.T) {
	for engine, want := range map[string]bool{
		"postgres":          true,
		"aurora-postgresql": true,
		"sqlserver-se":      true,
		"mysql":             false,
		"":                  false,
	} {
		if got := MCPEngineSupported(engine); got != want {
			t.Errorf("MCPEngineSupported(%q) = %v, want %v", engine, got, want)
		}
	}
}

// installSecretsAWS answers ListDBSecrets: identity of account 111111111111, the instance archive
// with its master secret arn:master, and ListSecrets with pages chained by tokens page2, page3…
func installSecretsAWS(t *testing.T, pages ...[]map[string]any) {
	t.Helper()
	installAWSAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch op := awsapitest.Operation(r); op {
		case "DescribeDBInstances":
			if id := r.FormValue("DBInstanceIdentifier"); id != "archive" {
				t.Errorf("DBInstanceIdentifier = %q, want archive", id)
			}
			awsapitest.RespondXML(t, w, http.StatusOK, rdsPageXML("", `<DBInstance><DBInstanceIdentifier>archive</DBInstanceIdentifier>`+
				`<DBInstanceArn>`+secretsInstanceARN+`</DBInstanceArn><MasterUserSecret><SecretArn>arn:master</SecretArn></MasterUserSecret></DBInstance>`))
		case "ListSecrets":
			var body struct{ NextToken string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			page := pageIndex(t, body.NextToken)
			reply := map[string]any{"SecretList": pages[page]}
			if page+1 < len(pages) {
				reply["NextToken"] = "page" + strconv.Itoa(page+2)
			}
			awsapitest.Respond(t, w, http.StatusOK, reply)
		default:
			t.Errorf("unexpected operation %s", op)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
}

func TestListDBSecretsNamesAccountAndRegionWhenEmpty(t *testing.T) {
	installSecretsAWS(t, nil)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	_, err := runner.ListDBSecrets(context.Background(), &Options{ResourceType: "rds", Name: "archive", AwsProfile: "dev", AwsRegion: "eu-west-3"})
	if err == nil || err.Error() != "no secret in 111111111111/eu-west-3" {
		t.Fatalf("err = %v, want the account and the region named", err)
	}
}

func TestListDBSecretsListsEveryPage(t *testing.T) {
	installSecretsAWS(t,
		[]map[string]any{{"ARN": "arn:other", "Name": "alpha/other"}},
		[]map[string]any{{"ARN": "arn:master", "Name": "rds!db-1234", "Description": "master"}})
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	got, err := runner.ListDBSecrets(context.Background(), &Options{ResourceType: "rds", Name: "archive", AwsProfile: "dev", AwsRegion: "eu-west-3"})
	want := []SecretSummary{
		{ARN: "arn:master", Name: "rds!db-1234", Description: "master", Linked: true},
		{ARN: "arn:other", Name: "alpha/other"},
	}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("secrets = %+v, %v; want %+v", got, err, want)
	}
}

func TestSecretCredentialsReaderReadsThePickedSecretOnEachCall(t *testing.T) {
	fake := installSecretsManager(t, map[string]string{"arn:secret": `{"username":"app_rw","password":"s3cr3t-Pa55"}`})
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, ReadOnlySecretTag: awstag.Tag{Key: "Role", Value: "read-only"}})
	read := runner.secretCredentialsReader(fake.cfg, "archive",
		&ideTarget{DBUser: "app_rw", DBSecretARN: "arn:secret", InstanceARN: secretsInstanceARN})

	for range 2 {
		creds, err := read(context.Background())
		if err != nil || len(creds) != 1 || creds[0] != (DBCredentials{User: "app_rw", Password: "s3cr3t-Pa55"}) {
			t.Fatalf("read = %+v, %v, want the secret's credentials", creds, err)
		}
	}
	if fake.count("GetSecretValue") != 2 || fake.count("ListSecrets") != 0 {
		t.Fatalf("operations = %v, want the picked secret read twice and nothing listed", fake.ops)
	}
}

func TestSecretCredentialsReaderReadsTheReadOnlySecretsOfEachDatabase(t *testing.T) {
	const (
		integ  = "arn:aws:secretsmanager:eu-west-3:111111111111:secret:dev/rds/read-only/archive-integ-uLQ3NB"
		named  = "arn:aws:secretsmanager:eu-west-3:111111111111:secret:dev/rds/read-only/archive-app-xyKAqI"
		broken = "arn:aws:secretsmanager:eu-west-3:111111111111:secret:dev/rds/read-only/archive-old-AbCdEf"
	)
	// The secrets deployed name no database: only their name tells them apart.
	fake := installSecretsManager(t, map[string]string{
		integ: `{"username":"app_ro","password":"integ-Pa55"}`,
		named: `{"username":"app_ro","password":"app-Pa55","dbname":"app_db"}`,
	}, integ, broken, named)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, ReadOnlySecretTag: awstag.Tag{Key: "Role", Value: "read-only"}})

	read := runner.secretCredentialsReader(fake.cfg, "archive", &ideTarget{InstanceARN: secretsInstanceARN, MasterSecretARN: "arn:master"})
	if read == nil {
		t.Fatal("no reader for a session signing in with the read-only secrets")
	}
	creds, err := read(context.Background())
	want := []DBCredentials{
		{Database: "app_db", Secret: "archive-app", User: "app_ro", Password: "app-Pa55"},
		{Secret: "archive-integ", User: "app_ro", Password: "integ-Pa55"},
	}
	if err != nil || !slices.Equal(creds, want) {
		t.Fatalf("read = %+v, %v, want every readable read-only secret by label, never the master one", creds, err)
	}
}

func TestSecretCredentialsReaderNeedsSecrets(t *testing.T) {
	tagged := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, ReadOnlySecretTag: awstag.Tag{Key: "Role", Value: "read-only"}})
	for name, target := range map[string]*ideTarget{
		"AWS IAM":        {IAMAuth: true, DBUser: "app_ro", InstanceARN: secretsInstanceARN},
		"typed password": {DBUser: "app_rw", DBPassword: "typed", InstanceARN: secretsInstanceARN},
	} {
		if tagged.secretCredentialsReader(nil, "archive", target) != nil {
			t.Errorf("%s session offers to show secrets", name)
		}
	}
	untagged := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	if untagged.secretCredentialsReader(nil, "archive", &ideTarget{InstanceARN: secretsInstanceARN}) != nil {
		t.Error("session offers read-only secrets without readOnlySecretTag")
	}
}

func TestResolveSecretSignInReturnsTheARNOfARecreatedSecret(t *testing.T) {
	const stale = "arn:aws:secretsmanager:eu-west-3:111111111111:secret:dev/app-xyKAqI"
	const current = "arn:aws:secretsmanager:eu-west-3:111111111111:secret:dev/app-Eim4Md"
	cfg := installSecretsManager(t, map[string]string{current: "s3cret"}).cfg
	user, arn, err := resolveSecretSignIn(context.Background(), cfg, stale, "typed")
	if err != nil || user != "typed" || arn != current {
		t.Fatalf("user = %q, arn = %q, err = %v; want the current ARN", user, arn, err)
	}
}

func TestRememberSecretARNReplacesTheSavedARNOfARecreatedSecret(t *testing.T) {
	const stale = "arn:aws:secretsmanager:eu-west-3:111111111111:secret:dev/app-xyKAqI"
	const current = "arn:aws:secretsmanager:eu-west-3:111111111111:secret:dev/app-Eim4Md"
	runner, _ := keychainRunner(t)
	options := Options{ResourceType: "rds", Name: "archive", AwsProfile: "dev", AwsRegion: "eu-west-3",
		SignIn: SignInSecret, DBSecretARN: stale, DBUser: "app_user"}
	mustSave(t, runner, &options)

	runner.rememberSecretARN(context.Background(), &options, current)
	prefs, ok := runner.LoadConnectionPrefs(context.Background(), options)
	if !ok || prefs.DBSecretARN != current || prefs.SignInMethod() != SignInSecret || prefs.DBUser != "app_user" {
		t.Fatalf("prefs = %+v, %v; want the current ARN saved, the rest kept", prefs, ok)
	}
	if options.DBSecretARN != current {
		t.Fatalf("options ARN = %q, want the current one", options.DBSecretARN)
	}
}
