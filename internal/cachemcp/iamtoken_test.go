package cachemcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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
