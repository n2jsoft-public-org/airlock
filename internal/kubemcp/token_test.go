package kubemcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// fakeTokens returns tokens[i] on the i-th call, expiring at expires, and counts the calls.
func fakeTokens(expires time.Time, tokens ...string) (fetch tokenFunc, callsPtr *int) {
	callCount := 0
	return func(context.Context) (string, time.Time, error) {
		token := tokens[min(callCount, len(tokens)-1)]
		callCount++
		return token, expires, nil
	}, &callCount
}

func TestTokenSourceCachesUntilShortlyBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fetch, calls := fakeTokens(now.Add(15*time.Minute), "k8s-aws-v1.first", "k8s-aws-v1.second")
	source := newTokenSource(fetch)
	source.now = func() time.Time { return now }

	for range 2 {
		if token, err := source.Token(context.Background()); err != nil || token != "k8s-aws-v1.first" {
			t.Fatalf("token = %q, %v", token, err)
		}
	}
	if *calls != 1 {
		t.Fatalf("fetched %d times, want 1", *calls)
	}
	now = now.Add(14*time.Minute + 30*time.Second)
	//nolint:errcheck // test intentionally ignores errors
	if token, _ := source.Token(context.Background()); token != "k8s-aws-v1.second" || *calls != 2 {
		t.Fatalf("token = %q after %d calls, want a new token within a minute of expiry", token, *calls)
	}
}

func TestTokenSourceResetFetchesANewToken(t *testing.T) {
	fetch, calls := fakeTokens(time.Now().Add(time.Hour), "a", "b")
	source := newTokenSource(fetch)
	//nolint:errcheck // test intentionally ignores errors
	_, _ = source.Token(context.Background())
	source.reset()
	//nolint:errcheck // test intentionally ignores errors
	if token, _ := source.Token(context.Background()); token != "b" || *calls != 2 {
		t.Fatalf("token = %q after %d calls", token, *calls)
	}
}

func TestTokenSourceSurfacesAnExpiredSignIn(t *testing.T) {
	cfg := aws.Config{Region: "eu-west-3", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, errors.New("the SSO session associated with this profile has expired")
	})}
	_, err := newTokenSource(eksTokenFunc(&cfg, "platform")).Token(context.Background())
	if _, ok := errors.AsType[*mcpserve.CredentialsError](err); !ok {
		t.Fatalf("err = %v, want a *mcpserve.CredentialsError", err)
	}
	if !strings.Contains(err.Error(), "sign in again") || strings.Contains(err.Error(), "operation error") {
		t.Fatalf("err = %q, want the sign-in message without the SDK chain", err)
	}
}

func TestEKSTokenFuncPresignsWithTheConfigCredentials(t *testing.T) {
	cfg := aws.Config{Region: "eu-west-3", Credentials: credentials.NewStaticCredentialsProvider("AKIDTEST", "secret", "")}
	token, err := newTokenSource(eksTokenFunc(&cfg, "platform")).Token(context.Background())
	if err != nil || len(token) < len("k8s-aws-v1.") || token[:11] != "k8s-aws-v1." {
		t.Fatalf("token = %q, %v", token, err)
	}
}
