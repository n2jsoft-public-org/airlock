package jumphost

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft/airlock/internal/awstag"
	"github.com/n2jsoft/airlock/internal/dbuser"
)

func TestRunnerResolveDBUser(t *testing.T) {
	defaults := dbuser.Defaults{IAM: "acme_ro", SQLServer: "mcp_ro"}
	withDefault := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, DefaultDBUser: defaults})
	if got := withDefault.ResolveDBUser("aurora-postgresql", "  "); got != "acme_ro" {
		t.Fatalf("blank user resolved to %q, want the configured default", got)
	}
	if got := withDefault.ResolveDBUser("sqlserver-se", ""); got != "mcp_ro" {
		t.Fatalf("blank SQL Server user resolved to %q, want the SQL Server default", got)
	}
	if got := withDefault.ResolveDBUser("postgres", " app_ro "); got != "app_ro" {
		t.Fatalf("explicit user resolved to %q", got)
	}
	iamOnly := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, DefaultDBUser: dbuser.Defaults{IAM: "acme_ro"}})
	if got := iamOnly.ResolveDBUser("sqlserver-ee", ""); got != "" {
		t.Fatalf("blank SQL Server user resolved to %q, want empty: the IAM user must not apply", got)
	}
	without := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	if got := without.ResolveDBUser("postgres", ""); got != "" {
		t.Fatalf("blank user without default resolved to %q, want empty", got)
	}
}

const (
	noInstancePageXML = `<DescribeInstancesResponse><reservationSet/><nextToken>next</nextToken></DescribeInstancesResponse>`
	instancePageXML   = `<DescribeInstancesResponse><reservationSet><item><instancesSet><item><instanceId>i-0123</instanceId></item>` +
		`</instancesSet></item></reservationSet><nextToken>more</nextToken></DescribeInstancesResponse>`
	lastEmptyPageXML = `<DescribeInstancesResponse><reservationSet/></DescribeInstancesResponse>`
)

// installEC2 answers DescribeInstances with pages in turn and returns the forms of the requests.
func installEC2(t *testing.T, pages ...string) *[]url.Values {
	t.Helper()
	var forms []url.Values
	awsapitest.Env(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if op := awsapitest.Operation(r); op != "DescribeInstances" || len(forms) == len(pages) {
			t.Errorf("unexpected request %s #%d", op, len(forms)+1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		forms = append(forms, r.Form)
		awsapitest.RespondXML(t, w, http.StatusOK, pages[len(forms)-1])
	}, "dev")
	return &forms
}

func TestJumphostLookupFiltersOnTagAndRunning(t *testing.T) {
	for _, tag := range []awstag.Tag{{Key: "Role", Value: "bastion"}, {}} {
		forms := installEC2(t, instancePageXML)
		runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, JumphostTag: tag})
		id, err := runner.getJumphostID(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"})
		if err != nil || id != "i-0123" {
			t.Fatalf("getJumphostID = %q, %v", id, err)
		}
		want := url.Values{"Filter.1.Name": {"tag:" + runner.jumphostTag.Key}, "Filter.1.Value.1": {runner.jumphostTag.Value},
			"Filter.2.Name": {"instance-state-name"}, "Filter.2.Value.1": {"running"}}
		for key, value := range want {
			if got := (*forms)[0][key]; !reflect.DeepEqual(got, value) {
				t.Errorf("tag %v: %s = %q, want %q", tag, key, got, value)
			}
		}
	}
}

func TestGetJumphostIDDefaultsToUsageJumphost(t *testing.T) {
	forms := installEC2(t, instancePageXML)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	if _, err := runner.getJumphostID(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"}); err != nil {
		t.Fatal(err)
	}
	if got := (*forms)[0].Get("Filter.1.Name") + "=" + (*forms)[0].Get("Filter.1.Value.1"); got != "tag:Usage=jumphost" {
		t.Fatalf("filter = %q, want tag:Usage=jumphost", got)
	}
}

func TestGetJumphostIDReadsPagesUntilAnInstance(t *testing.T) {
	// A filtered page can be empty and still carry a next token; the instance found stops the lookup.
	installEC2(t, noInstancePageXML, instancePageXML)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	if id, err := runner.getJumphostID(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"}); err != nil || id != "i-0123" {
		t.Fatalf("getJumphostID = %q, %v", id, err)
	}
}

func TestGetJumphostIDNotFoundNamesTag(t *testing.T) {
	installEC2(t, noInstancePageXML, lastEmptyPageXML)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	_, err := runner.getJumphostID(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"})
	if err == nil || !strings.Contains(err.Error(), "Usage=jumphost") || !strings.Contains(err.Error(), "eu-west-3") {
		t.Fatalf("error = %v, want the tag and region named", err)
	}
}

func TestGetJumphostIDReportsTheAWSErrorWithoutTheSDKChain(t *testing.T) {
	awsapitest.Env(t, func(w http.ResponseWriter, _ *http.Request) {
		awsapitest.RespondXML(t, w, http.StatusForbidden, `<Response><Errors><Error><Code>UnauthorizedOperation</Code>`+
			`<Message>You are not authorized to perform this operation.</Message></Error></Errors><RequestID>r</RequestID></Response>`)
	}, "dev")
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	_, err := runner.getJumphostID(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"})
	if err == nil || !strings.Contains(err.Error(), "failed to look up the jumphost instance: UnauthorizedOperation") ||
		strings.Contains(err.Error(), "operation error") {
		t.Fatalf("err = %v, want the lookup failure without the SDK operation chain", err)
	}
}

func TestSecretsSessionInfoWithoutTagOmitsReadOnlyHint(t *testing.T) {
	info := strings.Join(rdsSecretsSessionInfo(11433, "arn:db", "arn:master", "admin", awstag.Tag{}, "--profile dev"), "\n")
	if strings.Contains(info, "Read-only credentials") {
		t.Fatalf("no read-only hint expected without a tag:\n%s", info)
	}
	info = strings.Join(rdsSecretsSessionInfo(11433, "arn:db", "arn:master", "admin", awstag.Tag{Key: "AccessRole", Value: "ro-access"}, "--profile dev"), "\n")
	if !strings.Contains(info, "AccessRole=ro-access") || !strings.Contains(info, "DBInstanceArn=arn:db") {
		t.Fatalf("read-only hint must use the configured tag:\n%s", info)
	}
}

func TestStartMCPWithoutSecretTagExplainsConfig(t *testing.T) {
	awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev")
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, ProjectDir: t.TempDir()})
	target := ideTarget{Engine: "sqlserver-se", InstanceARN: "arn:db"}
	handle, display := runner.startMCP(context.Background(), &Options{}, "mssql-1", 11433, &target, awsConfig{Profile: "dev", Region: "eu-west-3"}, false)
	if handle != nil {
		t.Fatal("no MCP server expected without a read-only secret tag")
	}
	if !strings.Contains(display, "`airlock config set readOnlySecretTag Key=Value`") {
		t.Fatalf("display = %q, want the command setting the tag", display)
	}
}

func TestPrepareRDSWithoutIAMUserFailsBeforeReservingAPort(t *testing.T) {
	installRDS(t, rdsPageXML("", `<DBInstance><Engine>postgres</Engine><DBInstanceIdentifier>archive</DBInstanceIdentifier><DBInstanceArn>arn:db</DBInstanceArn>`+
		`<Endpoint><Address>archive.rds.amazonaws.com</Address><Port>55432</Port></Endpoint><IAMDatabaseAuthenticationEnabled>true</IAMDatabaseAuthenticationEnabled></DBInstance>`))
	registry := NewSessionRegistry()
	// Only the registry's reservation is under test: the real port may be busy on the machine (it
	// is in the ephemeral range, used by the connections of tests running in parallel).
	registry.portFree = func(int) bool { return true }
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, Registry: registry})

	_, err := runner.prepareRDS(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"}, &Options{Name: "archive"})
	if err == nil {
		t.Fatal("IAM sign-in without a database user must fail")
	}
	for _, want := range []string{"--db-user", "`airlock config set modules.sql.defaultDbUser <user>`"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
	if !registry.PortAvailable(55432) {
		t.Fatal("the failed connection must not keep a local port reserved")
	}
}

func TestJumphostNotFoundErrorNamesTheSetting(t *testing.T) {
	err := jumphostNotFoundError(awstag.Tag{Key: "Usage", Value: "jumphost"}, "dev", "eu-west-3")
	for _, want := range []string{
		"no running jumphost instance tagged Usage=jumphost in eu-west-3 (profile dev)",
		`"aws.jumphostTag"`,
		"`airlock config show`",
		"`airlock config set aws.jumphostTag Key=Value`",
		"--jumphost-tag",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}
