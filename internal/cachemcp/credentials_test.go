package cachemcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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
	runner, calls := exportAWS(time.Now().Add(time.Hour))
	provider := IAMCredentials(runner, "my-cache", "app-iam", "eu-west-3", false)
	user, token, err := provider(context.Background())
	if err != nil || user != "app-iam" || !strings.HasPrefix(token, "my-cache/?Action=connect&User=app-iam&") {
		t.Fatalf("= %q %q %v", user, token, err)
	}
	if _, _, err := provider(context.Background()); err != nil || *calls != 1 {
		t.Fatalf("credentials exported %d times, want 1 (%v)", *calls, err)
	}
}
