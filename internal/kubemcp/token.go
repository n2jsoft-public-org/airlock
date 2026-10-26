// Package kubemcp serves MCP servers giving AI assistants read-only access to EKS clusters reached
// through jumphost tunnels.
package kubemcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/n2jsoft-public-org/airlock/internal/awsapi"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// tokenRefreshMargin renews a token this long before it expires.
const tokenRefreshMargin = time.Minute

// tokenFunc presigns a new bearer token and returns when it stops being worth using.
type tokenFunc func(ctx context.Context) (token string, expires time.Time, err error)

// tokenSource presigns and caches the bearer tokens of a cluster.
type tokenSource struct {
	expires time.Time
	fetch   tokenFunc
	now     func() time.Time
	token   string
	mu      sync.Mutex
}

func newTokenSource(fetch tokenFunc) *tokenSource {
	return &tokenSource{fetch: fetch, now: time.Now}
}

// eksTokenFunc presigns the tokens of cluster with cfg; an expired sign-in comes out as a
// *mcpserve.CredentialsError.
func eksTokenFunc(cfg *aws.Config, cluster string) tokenFunc {
	signed := cfg.Copy()
	signed.Credentials = mcpserve.SignInCredentials(cfg.Credentials)
	return func(ctx context.Context) (string, time.Time, error) {
		token, expires, err := awsapi.EKSToken(ctx, &signed, cluster)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("failed to get a token for cluster %s: %w", cluster, mcpserve.AWSError(err))
		}
		return token, expires, nil
	}
}

// Token returns a valid token, presigning a new one when the cached one is about to expire.
func (s *tokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && s.now().Before(s.expires.Add(-tokenRefreshMargin)) {
		return s.token, nil
	}
	token, expires, err := s.fetch(ctx)
	if err != nil {
		return "", err
	}
	s.token, s.expires = token, expires
	return s.token, nil
}

// reset drops the cached token, e.g. after the API refused it.
func (s *tokenSource) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
}
