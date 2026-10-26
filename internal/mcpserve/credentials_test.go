package mcpserve

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/n2jsoft-public-org/airlock/internal/awsapi/awsapitest"
)

func TestSignInCredentialsWrapsExpiredSSO(t *testing.T) {
	expired := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, &ssocreds.InvalidTokenError{}
	})
	_, err := SignInCredentials(expired).Retrieve(context.Background())
	if _, ok := errors.AsType[*CredentialsError](err); !ok {
		t.Fatalf("err = %v, want a *CredentialsError", err)
	}
	if got := AWSError(err).Error(); !strings.HasPrefix(got, "failed to read the AWS credentials of the profile (sign in again from airlock)") {
		t.Fatalf("AWSError = %q", got)
	}
}

func TestSignInCredentialsPassesCredentials(t *testing.T) {
	inner := credentials.NewStaticCredentialsProvider("AKID", "SECRET", "TOKEN")
	got, err := SignInCredentials(inner).Retrieve(context.Background())
	if err != nil || got.AccessKeyID != "AKID" || got.SecretAccessKey != "SECRET" || got.SessionToken != "TOKEN" {
		t.Fatalf("credentials = %+v, %v", got, err)
	}
}

func TestSignInCredentialsHidesSDKChainOfExpiredSSOSession(t *testing.T) {
	awsapitest.Env(t, func(w http.ResponseWriter, r *http.Request) {
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
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithSharedConfigProfile("sso"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Credentials = SignInCredentials(cfg.Credentials)
	_, err = sts.NewFromConfig(cfg).GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{})
	if _, ok := errors.AsType[*CredentialsError](err); !ok {
		t.Fatalf("err = %v, want a *CredentialsError", err)
	}
	got := AWSError(err).Error()
	for _, leak := range []string{"operation error", "https response error"} {
		if strings.Contains(got, leak) {
			t.Fatalf("message leaks the SDK chain: %q", got)
		}
	}
	if !strings.Contains(got, "sign in again") {
		t.Fatalf("message = %q, want the sign-in hint", got)
	}
}
