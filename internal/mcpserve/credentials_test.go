package mcpserve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

var credentialsTime = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

// exportAWS answers aws configure export-credentials with new keys on each call, expiring at expires.
func exportAWS(expires time.Time) (runner AWSRunner, calls *int) {
	count := 0
	return func(_ context.Context, args ...string) (string, error) {
		if strings.Join(args, " ") != "configure export-credentials --format process" {
			return "", fmt.Errorf("unexpected aws %v", args)
		}
		count++
		return fmt.Sprintf(`{"Version":1,"AccessKeyId":"AKID%d","SecretAccessKey":"SECRET","SessionToken":"TOKEN","Expiration":%q}`,
			count, expires.UTC().Format(time.RFC3339)), nil
	}, &count
}

var _ aws.CredentialsProvider = (*ExportedCredentials)(nil)

func TestExportedCredentialsAreCachedUntilShortlyBeforeExpiry(t *testing.T) {
	now := credentialsTime
	runner, calls := exportAWS(now.Add(time.Hour))
	creds := NewExportedCredentials(runner)
	creds.now = func() time.Time { return now }

	first, err := creds.Retrieve(context.Background())
	if err != nil || first.AccessKeyID != "AKID1" || !first.CanExpire {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if again, err := creds.Retrieve(context.Background()); err != nil || again.AccessKeyID != "AKID1" || *calls != 1 {
		t.Fatalf("credentials not cached: %+v after %d calls (%v)", again, *calls, err)
	}
	now = now.Add(56 * time.Minute)
	if renewed, err := creds.Retrieve(context.Background()); err != nil || renewed.AccessKeyID != "AKID2" {
		t.Fatalf("credentials not renewed 4 minutes before expiry: %+v (%v)", renewed, err)
	}
}

func TestExportedCredentialsRejectsEmptyOutput(t *testing.T) {
	creds := NewExportedCredentials(func(context.Context, ...string) (string, error) { return `{}`, nil })
	if _, err := creds.Retrieve(context.Background()); err == nil {
		t.Fatal("expected an error without access key")
	}
}

func TestExportedCredentialsWithAnUnreadableExpiryAreNotCached(t *testing.T) {
	calls := 0
	creds := NewExportedCredentials(func(context.Context, ...string) (string, error) {
		calls++
		return `{"Version":1,"AccessKeyId":"AKID","SecretAccessKey":"SECRET","Expiration":"in an hour"}`, nil
	})
	for range 2 {
		if got, err := creds.Retrieve(context.Background()); err != nil || got.AccessKeyID != "AKID" {
			t.Fatalf("credentials = %+v, %v", got, err)
		}
	}
	if calls != 2 {
		t.Fatalf("aws called %d times, want credentials read again (expiry unknown)", calls)
	}
}

func TestExportedCredentialsFailureTellsToSignInAgain(t *testing.T) {
	creds := NewExportedCredentials(func(context.Context, ...string) (string, error) {
		return "", errors.New("the SSO session has expired")
	})
	_, err := creds.Retrieve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sign in again from airlock") || !strings.Contains(err.Error(), "SSO session has expired") {
		t.Fatalf("err = %v", err)
	}
}
