package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

func TestExecVarsOfAPostgresTunnel(t *testing.T) {
	client := jumphost.Client{Engine: "postgres", Address: dbmcp.TunnelAddress(15432), User: "app_ro", Password: "tok/en?&=", Database: "orders db"}
	got := execVars(&client, "")
	want := []string{
		"AIRLOCK_HOST=127.0.0.1", "AIRLOCK_PORT=15432", "AIRLOCK_USER=app_ro", "AIRLOCK_PASSWORD=tok/en?&=", "AIRLOCK_DATABASE=orders db",
		"PGHOST=127.0.0.1", "PGPORT=15432", "PGUSER=app_ro", "PGPASSWORD=tok/en?&=", "PGDATABASE=orders db", "PGSSLMODE=require",
		"DATABASE_URL=postgres://app_ro:tok%2Fen%3F&=@127.0.0.1:15432/orders%20db?sslmode=require",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("vars =\n%q\nwant\n%q", got, want)
	}
}

func TestExecVarsOfADirectSQLServer(t *testing.T) {
	client := jumphost.Client{Engine: "sqlserver", Address: dbmcp.Address{Host: "db.internal", Port: 1433, TLS: dbmcp.TLSVerify}, User: "report", Password: "pw"}
	got := strings.Join(execVars(&client, ""), "\n")
	for _, want := range []string{"SQLCMDSERVER=db.internal,1433", "SQLCMDUSER=report", "SQLCMDPASSWORD=pw", "DATABASE_URL=sqlserver://report:pw@db.internal:1433?encrypt=true"} {
		if !strings.Contains(got, want+"\n") && !strings.HasSuffix(got, want) {
			t.Errorf("vars lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "SQLCMDDBNAME") || strings.Contains(got, "PGHOST") {
		t.Errorf("vars set settings without value or of another engine:\n%s", got)
	}
}

func TestExecVarsOfAMySQLTunnel(t *testing.T) {
	client := jumphost.Client{Engine: "mariadb", Address: dbmcp.TunnelAddress(13306), User: "app_ro", Password: "tok/en", Database: "shop"}
	got := execVars(&client, "")
	for _, want := range []string{"MYSQL_HOST=127.0.0.1", "MYSQL_TCP_PORT=13306", "MYSQL_PWD=tok/en", "DATABASE_URL=mysql://app_ro:tok%2Fen@127.0.0.1:13306/shop"} {
		if !slices.Contains(got, want) {
			t.Errorf("vars lack %q: %q", want, got)
		}
	}
}

func TestExecVarsOfCaches(t *testing.T) {
	tls := jumphost.Client{Engine: jumphost.ClientEngineRedis, Address: dbmcp.TunnelAddress(16379), ServerName: "master.cache.amazonaws.com", User: "app", Password: "p:w"}
	got := execVars(&tls, "")
	if !slices.Contains(got, "REDIS_URL=rediss://app:p%3Aw@127.0.0.1:16379") || !slices.Contains(got, "AIRLOCK_TLS_SERVER_NAME=master.cache.amazonaws.com") {
		t.Fatalf("TLS cache vars = %q", got)
	}
	plain := jumphost.Client{Engine: jumphost.ClientEngineRedis, Address: dbmcp.Address{Host: "127.0.0.1", Port: 56379, TLS: dbmcp.TLSDisable}}
	if got := execVars(&plain, ""); !slices.Equal(got, []string{"AIRLOCK_HOST=127.0.0.1", "AIRLOCK_PORT=56379", "REDIS_URL=redis://127.0.0.1:56379"}) {
		t.Fatalf("cache without auth vars = %q", got)
	}
}

func TestExecVarsOfAnEKSTunnel(t *testing.T) {
	client := jumphost.Client{Address: dbmcp.TunnelAddress(16443)}
	got := execVars(&client, "/home/me/.config/airlock/kube/prod.yaml")
	if !slices.Equal(got, []string{"AIRLOCK_HOST=127.0.0.1", "AIRLOCK_PORT=16443", "KUBECONFIG=/home/me/.config/airlock/kube/prod.yaml"}) {
		t.Fatalf("vars = %q", got)
	}
}

// TestExecHelperProcess is the command of the runCommand tests: it prints PGHOST and exits with
// AIRLOCK_EXEC_HELPER_EXIT.
func TestExecHelperProcess(*testing.T) {
	code := os.Getenv("AIRLOCK_EXEC_HELPER_EXIT")
	if code == "" {
		return
	}
	fmt.Print(os.Getenv("PGHOST"))
	exit, _ := strconv.Atoi(code) //nolint:errcheck // the tests set a number
	os.Exit(exit)
}

func TestRunCommandPassesTheEnvironmentAndExitStatus(t *testing.T) {
	for _, exit := range []int{0, 3} {
		var out bytes.Buffer
		env := append(os.Environ(), "AIRLOCK_EXEC_HELPER_EXIT="+strconv.Itoa(exit), "PGHOST=127.0.0.1")
		code, err := runCommand([]string{os.Args[0], "-test.run=^TestExecHelperProcess$"}, env, strings.NewReader(""), &out, io.Discard, false)
		if err != nil || code != exit || out.String() != "127.0.0.1" {
			t.Fatalf("runCommand = %d, %v, output %q; want %d", code, err, out.String(), exit)
		}
	}
}

func TestRunCommandReportsAMissingCommand(t *testing.T) {
	if _, err := runCommand([]string{"airlock-no-such-command"}, nil, nil, io.Discard, io.Discard, false); err == nil || !strings.Contains(err.Error(), "airlock-no-such-command") {
		t.Fatalf("err = %v", err)
	}
}

func TestExecChecksItsArguments(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, tc := range []struct {
		want string
		args []string
	}{
		{args: []string{"exec", "rds", "my-db"}, want: "after --"},
		{args: []string{"exec", "rds", "my-db", "--"}, want: "after --"},
		{args: []string{"exec", "rds", "--", "psql"}, want: "before --"},
		{args: []string{"exec", "s3", "bucket", "--", "ls"}, want: `not "s3"`},
	} {
		cmd := NewRootCmd()
		cmd.SetArgs(tc.args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err := cmd.Execute()
		if _, exit := errors.AsType[*ExitCodeError](err); err == nil || exit || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("airlock %s: err = %v, want %q", strings.Join(tc.args, " "), err, tc.want)
		}
	}
}

func TestCompleteExecArgsOffersTheTypesThenFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cmd := newExecCmd()
	types, _ := completeExecArgs(cmd, nil, "")
	var names []string
	for _, entry := range types {
		name, _, _ := strings.Cut(entry, "\t")
		names = append(names, name)
	}
	if !slices.Equal(names, []string{"rds", "eks", "elasticache", "direct", "docker"}) {
		t.Fatalf("types = %q", names)
	}
}

func TestStackExecVarsPrefixEachTargetAndKeepUniqueOnesBare(t *testing.T) {
	targets := []jumphost.Options{
		{ResourceType: "rds", Name: "orders-db"},
		{ResourceType: "elasticache", Name: "sessions"},
		{ResourceType: "eks", Name: "9platform"},
	}
	vars := [][]string{
		{"AIRLOCK_PORT=5432", "DATABASE_URL=postgres://a"},
		{"AIRLOCK_PORT=6379", "REDIS_URL=redis://b"},
		{"AIRLOCK_PORT=9444", "KUBECONFIG=/k"},
	}
	got, err := stackExecVars(targets, vars)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"ORDERS_DB_AIRLOCK_PORT=5432", "ORDERS_DB_DATABASE_URL=postgres://a", "DATABASE_URL=postgres://a"},
		{"SESSIONS_AIRLOCK_PORT=6379", "SESSIONS_REDIS_URL=redis://b", "REDIS_URL=redis://b"},
		{"_9PLATFORM_AIRLOCK_PORT=9444", "_9PLATFORM_KUBECONFIG=/k", "KUBECONFIG=/k"},
	}
	for idx := range want {
		if !slices.Equal(got[idx], want[idx]) {
			t.Errorf("vars of %s = %q, want %q", targets[idx].Name, got[idx], want[idx])
		}
	}
}

func TestStackExecVarsUseTheTypeWhenNamesClash(t *testing.T) {
	targets := []jumphost.Options{{ResourceType: "rds", Name: "orders"}, {ResourceType: "elasticache", Name: "orders"}}
	got, err := stackExecVars(targets, [][]string{{"AIRLOCK_PORT=1"}, {"AIRLOCK_PORT=2"}})
	if err != nil || !slices.Equal(got[0], []string{"RDS_ORDERS_AIRLOCK_PORT=1"}) || !slices.Equal(got[1], []string{"ELASTICACHE_ORDERS_AIRLOCK_PORT=2"}) {
		t.Fatalf("stackExecVars() = %q, %v", got, err)
	}
	clash := []jumphost.Options{{ResourceType: "rds", Name: "a.b"}, {ResourceType: "rds", Name: "a-b"}}
	if _, err := stackExecVars(clash, [][]string{nil, nil}); err == nil || !strings.Contains(err.Error(), "RDS_A_B") {
		t.Fatalf("clashing prefixes: %v", err)
	}
}

func TestExecStackChecksItsArgumentsBeforeOpening(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(dir+"/.git", 0o750); err != nil {
		t.Fatal(err)
	}
	// airlock exec opens neither an API-only target nor a forward.
	data := `{"stacks":{"dev":[{"type":"dynamodb","name":"orders"},{"type":"forward","host":"h","remotePort":443}]}}`
	if err := os.WriteFile(dir+"/.airlock.json", []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for args, want := range map[string]string{
		"--stack dev":                     "give the command to run after --",
		"--stack a b -- true":             "at most one stack name",
		"--stack --profile p -- true":     "--profile does not apply with --stack",
		"--stack qa -- true":              "qa",
		"--stack dev -- true":             "stack dev has no target airlock exec connects to",
		"--stack --db-secret s dev -- ls": "--db-secret does not apply",
	} {
		cmd := newExecCmd()
		cmd.SetArgs(strings.Fields(args))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("exec %s: error = %v, want %q", args, err, want)
		}
	}
}
