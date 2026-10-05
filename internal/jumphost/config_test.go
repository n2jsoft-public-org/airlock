package jumphost

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/awstag"
)

func TestRunnerResolveDBUser(t *testing.T) {
	withDefault := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, DefaultDBUser: "acme_ro"})
	if got := withDefault.ResolveDBUser("  "); got != "acme_ro" {
		t.Fatalf("blank user resolved to %q, want the configured default", got)
	}
	if got := withDefault.ResolveDBUser(" app_ro "); got != "app_ro" {
		t.Fatalf("explicit user resolved to %q", got)
	}
	without := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard})
	if got := without.ResolveDBUser(""); got != "" {
		t.Fatalf("blank user without default resolved to %q, want empty", got)
	}
}

// installRecordingAWS puts an aws executable on PATH that appends its arguments to a log file and
// prints output.
func installRecordingAWS(t *testing.T, output string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$@\" >> '" + logPath + "'\nprintf '%s\\n' '" + output + "'\n"
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return logPath
}

func TestGetJumphostIDUsesConfiguredTag(t *testing.T) {
	logPath := installRecordingAWS(t, "i-0123")
	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, JumphostTag: awstag.Tag{Key: "Role", Value: "bastion"}})

	id, err := runner.getJumphostID(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"})
	if err != nil || id != "i-0123" {
		t.Fatalf("getJumphostID = %q, %v", id, err)
	}
	calls, err := os.ReadFile(logPath) // #nosec G304 -- test file.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "Name=tag:Role,Values=bastion") {
		t.Fatalf("describe-instances filter = %q, want the configured tag", calls)
	}
}

func TestGetJumphostIDDefaultsToUsageJumphost(t *testing.T) {
	logPath := installRecordingAWS(t, "i-0123")
	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard})
	if _, err := runner.getJumphostID(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"}); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(logPath) // #nosec G304 -- test file.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "Name=tag:Usage,Values=jumphost") {
		t.Fatalf("describe-instances filter = %q, want Usage=jumphost", calls)
	}
}

func TestGetJumphostIDNotFoundNamesTag(t *testing.T) {
	installRecordingAWS(t, "None")
	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard})
	_, err := runner.getJumphostID(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"})
	if err == nil || !strings.Contains(err.Error(), "Usage=jumphost") || !strings.Contains(err.Error(), "eu-west-3") {
		t.Fatalf("error = %v, want the tag and region named", err)
	}
}

func TestSecretsSessionInfoWithoutTagOmitsReadOnlyHint(t *testing.T) {
	info := strings.Join(rdsSecretsSessionInfo(11433, "arn:db", "arn:master", "admin", awstag.Tag{}, "--profile dev"), "\n")
	if strings.Contains(info, "list-secrets") {
		t.Fatalf("no read-only hint expected without a tag:\n%s", info)
	}
	info = strings.Join(rdsSecretsSessionInfo(11433, "arn:db", "arn:master", "admin", awstag.Tag{Key: "AccessRole", Value: "ro-access"}, "--profile dev"), "\n")
	if !strings.Contains(info, "Key=='AccessRole' && Value=='ro-access'") {
		t.Fatalf("read-only hint must use the configured tag:\n%s", info)
	}
}

func TestStartMCPWithoutSecretTagExplainsConfig(t *testing.T) {
	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, ProjectDir: t.TempDir()})
	target := ideTarget{Engine: "sqlserver-se", InstanceARN: "arn:db"}
	handle, display := runner.startMCP(&Options{}, "mssql-1", 11433, &target, awsConfig{Profile: "dev", Region: "eu-west-3"}, false)
	if handle != nil {
		t.Fatal("no MCP server expected without a read-only secret tag")
	}
	if !strings.Contains(display, "`airlock config set readOnlySecretTag Key=Value`") {
		t.Fatalf("display = %q, want the command setting the tag", display)
	}
}

func TestPrepareRDSWithoutIAMUserFailsBeforeReservingAPort(t *testing.T) {
	installRecordingAWS(t, `{"Engine":"postgres","DBInstanceIdentifier":"archive","DBInstanceArn":"arn:db","Endpoint":{"Address":"archive.rds.amazonaws.com","Port":55432},"IAMDatabaseAuthenticationEnabled":true}`)
	registry := NewSessionRegistry()
	// Only the registry's reservation is under test: the real port may be busy on the machine (it
	// is in the ephemeral range, used by the connections of tests running in parallel).
	registry.portFree = func(int) bool { return true }
	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, Registry: registry})

	_, _, _, _, _, err := runner.prepareRDS(context.Background(), awsConfig{Profile: "dev", Region: "eu-west-3"}, Options{Name: "archive"})
	if err == nil {
		t.Fatal("IAM sign-in without a database user must fail")
	}
	for _, want := range []string{"--db-user", "`airlock config set defaultDbUser <user>`"} {
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
		`"jumphostTag"`,
		"`airlock config show`",
		"`airlock config set jumphostTag Key=Value`",
		"--jumphost-tag",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}
