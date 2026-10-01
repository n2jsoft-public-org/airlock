package cachemcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

var tokenTime = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func TestPresignConnectMatchesTheSigV4Token(t *testing.T) {
	token, err := presignConnect(context.Background(), aws.Credentials{AccessKeyID: "AKID", SecretAccessKey: "SECRET", SessionToken: "TOKEN"},
		"my-cache", "app-iam", "eu-west-3", false, tokenTime)
	if err != nil {
		t.Fatal(err)
	}
	want := "my-cache/?Action=connect&User=app-iam&X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKID%2F20261001%2Feu-west-3%2Felasticache%2Faws4_request&X-Amz-Date=20261001T120000Z" +
		"&X-Amz-Expires=900&X-Amz-Security-Token=TOKEN&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=e0fd09f0508367db9cd052e8f5f81bc151037aa895bf5496fc7ce5629734e1b5"
	if token != want {
		t.Fatalf("token =\n%s\nwant\n%s", token, want)
	}
}

func TestPresignConnectForServerlessCaches(t *testing.T) {
	token, err := presignConnect(context.Background(), aws.Credentials{AccessKeyID: "AKID", SecretAccessKey: "SECRET"},
		"sessions", "app-iam", "eu-west-3", true, tokenTime)
	if err != nil || !strings.HasPrefix(token, "sessions/?Action=connect&ResourceType=ServerlessCache&User=app-iam&") {
		t.Fatalf("token = %s, %v", token, err)
	}
}

// exportAWS prints credentials expiring at expires and counts the calls.
func exportAWS(expires time.Time) (runner mcpserve.AWSRunner, calls *int) {
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

func TestExportedCredentialsAreCachedUntilShortlyBeforeExpiry(t *testing.T) {
	now := tokenTime
	runner, calls := exportAWS(now.Add(time.Hour))
	creds := &exportedCredentials{aws: runner, now: func() time.Time { return now }}

	first, err := creds.get(context.Background())
	if err != nil || first.AccessKeyID != "AKID1" || !first.CanExpire {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if again, err := creds.get(context.Background()); err != nil || again.AccessKeyID != "AKID1" || *calls != 1 {
		t.Fatalf("credentials not cached: %+v after %d calls (%v)", again, *calls, err)
	}
	now = now.Add(56 * time.Minute)
	if renewed, err := creds.get(context.Background()); err != nil || renewed.AccessKeyID != "AKID2" {
		t.Fatalf("credentials not renewed 4 minutes before expiry: %+v (%v)", renewed, err)
	}
}

func TestExportedCredentialsRejectsEmptyOutput(t *testing.T) {
	creds := &exportedCredentials{aws: func(context.Context, ...string) (string, error) { return `{}`, nil }, now: time.Now}
	if _, err := creds.get(context.Background()); err == nil {
		t.Fatal("expected an error without access key")
	}
}

func TestExportedCredentialsWithAnUnreadableExpiryAreNotCached(t *testing.T) {
	calls := 0
	runner := func(context.Context, ...string) (string, error) {
		calls++
		return `{"Version":1,"AccessKeyId":"AKID","SecretAccessKey":"SECRET","Expiration":"in an hour"}`, nil
	}
	creds := &exportedCredentials{aws: runner, now: time.Now}
	for range 2 {
		if got, err := creds.get(context.Background()); err != nil || got.AccessKeyID != "AKID" {
			t.Fatalf("credentials = %+v, %v", got, err)
		}
	}
	if calls != 2 {
		t.Fatalf("aws called %d times, want credentials read again (expiry unknown)", calls)
	}
}
