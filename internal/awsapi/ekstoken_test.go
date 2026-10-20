package awsapi

import (
	"context"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

func TestEKSTokenPresignsGetCallerIdentity(t *testing.T) {
	cfg := aws.Config{Region: "eu-west-3", Credentials: credentials.NewStaticCredentialsProvider("AKIDTEST", "secret", "")}
	before := time.Now()
	token, expires, err := EKSToken(context.Background(), &cfg, "platform")
	if err != nil {
		t.Fatal(err)
	}
	encoded, ok := strings.CutPrefix(token, "k8s-aws-v1.")
	if !ok {
		t.Fatalf("token = %q, want the k8s-aws-v1. prefix", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	if u.Host != "sts.eu-west-3.amazonaws.com" || query.Get("Action") != "GetCallerIdentity" ||
		query.Get("X-Amz-Expires") != "60" || !strings.Contains(query.Get("X-Amz-SignedHeaders"), "x-k8s-aws-id") {
		t.Fatalf("presigned url = %s", raw)
	}
	if expires.Before(before.Add(13*time.Minute)) || expires.After(time.Now().Add(15*time.Minute)) {
		t.Fatalf("expires = %v", expires)
	}
}
