package cachemcp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/n2jsoft-public-org/airlock/internal/awsapi"
	"github.com/n2jsoft-public-org/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

func TestPasswordCredentialsDefaultUser(t *testing.T) {
	user, password, err := PasswordCredentials("", "s3cret")(context.Background())
	if user != DefaultUser || password != "s3cret" || err != nil {
		t.Fatalf("= %q %q %v", user, password, err)
	}
	if user, _, err := PasswordCredentials("app", "x")(context.Background()); user != "app" || err != nil {
		t.Fatalf("user = %q, %v", user, err)
	}
}

func TestSecretCredentialsReadOnceAndRetryFailures(t *testing.T) {
	reads := 0
	provider := SecretCredentials(func(context.Context) (string, string, error) {
		reads++
		if reads == 1 {
			return "", "", errors.New("throttled")
		}
		return "", "from-secret", nil
	})
	if _, _, err := provider(context.Background()); err == nil {
		t.Fatal("first read must fail")
	}
	for range 2 {
		user, password, err := provider(context.Background())
		if user != DefaultUser || password != "from-secret" || err != nil {
			t.Fatalf("= %q %q %v", user, password, err)
		}
	}
	if reads != 2 {
		t.Fatalf("secret read %d times, want 2", reads)
	}
}

func TestIAMCredentialsSignAFreshTokenPerConnection(t *testing.T) {
	awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev")
	cfg, err := awsapi.NewConfigs().Get(t.Context(), "dev", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	provider := IAMCredentials(&cfg, "my-cache", "app-iam", false)
	user, token, err := provider(context.Background())
	if err != nil || user != "app-iam" || !strings.HasPrefix(token, "my-cache/?Action=connect&User=app-iam&") ||
		!strings.Contains(token, "AKIDTEST") || !strings.Contains(token, "eu-west-3") {
		t.Fatalf("= %q %q %v", user, token, err)
	}
}

func TestIAMCredentialsTellToSignInAgain(t *testing.T) {
	cfg := aws.Config{Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, errors.New("the SSO session has expired")
	})}
	_, _, err := IAMCredentials(&cfg, "my-cache", "app-iam", false)(context.Background())
	if _, ok := errors.AsType[*mcpserve.CredentialsError](err); !ok {
		t.Fatalf("err = %v, want a *mcpserve.CredentialsError", err)
	}
}
