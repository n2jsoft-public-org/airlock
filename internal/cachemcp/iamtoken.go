package cachemcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	// iamTokenLifetime is how long ElastiCache accepts a token for a new connection.
	iamTokenLifetime = 15 * time.Minute
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
