package cachemcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

const (
	// iamTokenLifetime is how long ElastiCache accepts a token for a new connection.
	iamTokenLifetime = 15 * time.Minute
	// credentialsRefreshMargin renews exported credentials this long before they expire.
	credentialsRefreshMargin = 5 * time.Minute
	// emptyPayloadHash is the SHA-256 of an empty body, signed into presigned requests.
	emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// presignConnect is the AWS IAM auth token of user for cacheName: a SigV4-presigned connect
// request of service elasticache, valid 15 minutes, without its scheme.
//
//nolint:gocritic // hugeParam: aws.Credentials is the value type the signer takes.
func presignConnect(ctx context.Context, creds aws.Credentials, cacheName, user, region string, serverless bool, at time.Time) (string, error) {
	query := url.Values{
		"Action":        {"connect"},
		"User":          {user},
		"X-Amz-Expires": {strconv.Itoa(int(iamTokenLifetime / time.Second))},
	}
	if serverless {
		query.Set("ResourceType", "ServerlessCache")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+cacheName+"/?"+query.Encode(), http.NoBody)
	if err != nil {
		return "", err
	}
	signed, _, err := v4.NewSigner().PresignHTTP(ctx, creds, request, emptyPayloadHash, "elasticache", region, at)
	if err != nil {
		return "", fmt.Errorf("failed to sign the cache IAM token: %w", err)
	}
	return strings.TrimPrefix(signed, "http://"), nil
}

// exportedCredentials reads the AWS credentials of the session's profile with
// aws configure export-credentials, and keeps them until shortly before they expire.
type exportedCredentials struct {
	expires time.Time
	aws     mcpserve.AWSRunner
	now     func() time.Time
	creds   aws.Credentials
	mu      sync.Mutex
	cached  bool
}

func (e *exportedCredentials) get(ctx context.Context) (aws.Credentials, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cached && (e.expires.IsZero() || e.now().Before(e.expires.Add(-credentialsRefreshMargin))) {
		return e.creds, nil
	}
	raw, err := e.aws(ctx, "configure", "export-credentials", "--format", "process")
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("failed to read the AWS credentials signing the cache IAM token: %w", err)
	}
	var exported struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"SessionToken"`
		Expiration      string `json:"Expiration"`
	}
	if json.Unmarshal([]byte(raw), &exported) != nil || exported.AccessKeyID == "" {
		return aws.Credentials{}, errors.New("aws configure export-credentials printed no credentials")
	}
	var expires time.Time
	// Credentials whose expiry cannot be read are used once, never cached: they may expire anytime.
	readable := true
	if exported.Expiration != "" {
		if expires, err = time.Parse(time.RFC3339, exported.Expiration); err != nil {
			expires, readable = time.Time{}, false
		}
	}
	e.creds = aws.Credentials{
		AccessKeyID: exported.AccessKeyID, SecretAccessKey: exported.SecretAccessKey, SessionToken: exported.SessionToken,
		CanExpire: !expires.IsZero(), Expires: expires,
	}
	e.expires, e.cached = expires, readable
	return e.creds, nil
}
