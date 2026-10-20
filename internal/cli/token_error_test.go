package cli

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

func TestTokenCommandsReportAnExpiredSignIn(t *testing.T) {
	awsapitest.Env(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		if _, err := w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid refresh token provided"}`)); err != nil {
			t.Error(err)
		}
	})
	conf := "[profile sso]\nregion = eu-west-3\nsso_session = acme\nsso_account_id = 111111111111\nsso_role_name = Dev\n\n" +
		"[sso-session acme]\nsso_start_url = https://acme.awsapps.com/start\nsso_region = eu-west-3\n"
	// #nosec G703 -- a temp file of the test.
	if err := os.WriteFile(os.Getenv("AWS_CONFIG_FILE"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath, err := ssocreds.StandardCachedTokenFilepath("acme")
	if err != nil {
		t.Fatal(err)
	}
	token := `{"accessToken":"old","expiresAt":"2000-01-01T00:00:00Z","refreshToken":"r","clientId":"c","clientSecret":"s","registrationExpiresAt":"2999-01-01T00:00:00Z"}`
	if err = os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- a temp file of the test.
	if err = os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"eks-token", "--cluster-name", "platform", "--profile", "sso", "--region", "eu-west-3"},
		{"rds-token", "--hostname", "db.rds", "--username", "app", "--profile", "sso", "--region", "eu-west-3"},
	} {
		_, err := runAirlock(t, args...)
		if !errors.Is(err, jumphost.ErrSignInRequired) || !strings.Contains(err.Error(), "sso") || strings.Contains(err.Error(), "operation error") {
			t.Fatalf("%s: err = %v, want ErrSignInRequired naming the profile", args[0], err)
		}
	}
}
