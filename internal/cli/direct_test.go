package cli

// Every test gives an empty standard input that is not a terminal: airlock direct never opens the dashboard.

import (
	"strings"
	"testing"
)

func TestDirectRefusesAWSFlags(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	for _, args := range [][]string{
		{"--profile", "dev"}, {"--aws-profile", "dev"}, {"--region", "eu-west-3"},
		{"--db-user", "admin"}, {"--db-secret", "arn:aws:secretsmanager:eu-west-3:1:secret:x"},
	} {
		flag := strings.TrimPrefix(args[0], "--")
		_, err := runAirlockWithInput(t, "", append([]string{"direct", "legacy-billing"}, args...)...)
		if err == nil || err.Error() != "--"+flag+" does not apply to direct databases" {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
}

func TestDirectRefusesALocalPort(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	_, err := runAirlockWithInput(t, "", "direct", "legacy-billing", "15433")
	if err == nil || err.Error() != "a local port does not apply to direct databases" {
		t.Fatalf("err = %v", err)
	}
}

func TestDirectRequiresAName(t *testing.T) {
	testDatabases(t)
	_, err := runAirlockWithInput(t, "", "direct")
	if err == nil || !strings.HasPrefix(err.Error(), "a direct database name is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestDirectUnknownNameFailsBeforeOpening(t *testing.T) {
	testDatabases(t)
	_, err := runAirlockWithInput(t, "", "direct", "nope")
	if err == nil || err.Error() != "no direct database named nope (see airlock connection list)" {
		t.Fatalf("err = %v", err)
	}
}

func TestDirectWithoutRememberedPasswordFailsOutsideATerminal(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	// Standard input is a strings.Reader, not a terminal: no dashboard, the keychain is the only source.
	_, err := runAirlockWithInput(t, "", "direct", "legacy-billing")
	want := `no password remembered for legacy-billing: open it once from the dashboard with "Remember in keychain"`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestRootHelpNamesTheDirectType(t *testing.T) {
	if use := NewRootCmd().Use; use != "airlock [eks|rds|elasticache|dynamodb|s3|direct|docker] [name] [local-port]" {
		t.Fatalf("Use = %q", use)
	}
}
