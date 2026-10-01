package kubemcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// tokenAWS prints the ExecCredential of tokens[i] on the i-th call, expiring at expires, and
// counts the calls.
func tokenAWS(expires time.Time, tokens ...string) (runner mcpserve.AWSRunner, callsPtr *int) {
	callCount := 0
	return func(_ context.Context, args ...string) (string, error) {
		if strings.Join(args[:2], " ") != "eks get-token" {
			return "", fmt.Errorf("unexpected aws %v", args)
		}
		token := tokens[min(callCount, len(tokens)-1)]
		callCount++
		return fmt.Sprintf(`{"kind":"ExecCredential","apiVersion":"client.authentication.k8s.io/v1beta1","spec":{},`+
			`"status":{"expirationTimestamp":%q,"token":%q}}`, expires.UTC().Format(time.RFC3339), token), nil
	}, &callCount
}

func TestTokenSourceCachesUntilShortlyBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	aws, calls := tokenAWS(now.Add(15*time.Minute), "k8s-aws-v1.first", "k8s-aws-v1.second")
	source := newTokenSource(aws, "platform", "dev")
	source.now = func() time.Time { return now }

	for range 2 {
		if token, err := source.Token(context.Background()); err != nil || token != "k8s-aws-v1.first" {
			t.Fatalf("token = %q, %v", token, err)
		}
	}
	if *calls != 1 {
		t.Fatalf("aws called %d times, want 1", *calls)
	}
	now = now.Add(14*time.Minute + 30*time.Second)
	//nolint:errcheck // test intentionally ignores errors
	if token, _ := source.Token(context.Background()); token != "k8s-aws-v1.second" || *calls != 2 {
		t.Fatalf("token = %q after %d calls, want a new token within a minute of expiry", token, *calls)
	}
}

func TestTokenSourceResetFetchesANewToken(t *testing.T) {
	aws, calls := tokenAWS(time.Now().Add(time.Hour), "a", "b")
	source := newTokenSource(aws, "platform", "dev")
	//nolint:errcheck // test intentionally ignores errors
	_, _ = source.Token(context.Background())
	source.reset()
	//nolint:errcheck // test intentionally ignores errors
	if token, _ := source.Token(context.Background()); token != "b" || *calls != 2 {
		t.Fatalf("token = %q after %d calls", token, *calls)
	}
}

func TestTokenSourceExplainsExpiredSSOSession(t *testing.T) {
	aws := func(context.Context, ...string) (string, error) {
		return "", errors.New("aws eks get-token failed: Error when retrieving token from sso: Token has expired and refresh failed")
	}
	_, err := newTokenSource(aws, "platform", "dev").Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "aws sso login --profile dev") {
		t.Fatalf("err = %v, want the sso login hint", err)
	}
}

func TestTokenSourceRejectsOutputWithoutToken(t *testing.T) {
	aws := func(context.Context, ...string) (string, error) { return `{"status":{}}`, nil }
	if _, err := newTokenSource(aws, "platform", "dev").Token(context.Background()); err == nil {
		t.Fatal("expected an error without token")
	}
}
