package cli

import (
	"net/http"
	"strings"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/awsapi/awsapitest"
)

func TestRDSTokenCmdPrintsAToken(t *testing.T) {
	awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev")

	out, err := runAirlock(t, "rds-token", "--hostname", "archive.abc.eu-west-3.rds.amazonaws.com", "--port", "5432",
		"--username", "app_ro", "--profile", "dev", "--region", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "archive.abc.eu-west-3.rds.amazonaws.com:5432?") || !strings.Contains(out, "DBUser=app_ro") || strings.Count(out, "\n") != 1 {
		t.Fatalf("out = %q, want a presigned token on one line", out)
	}
}
