// Package kubemcp serves MCP servers giving AI assistants read-only access to EKS clusters reached
// through jumphost tunnels.
package kubemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// tokenRefreshMargin renews a token this long before it expires.
const tokenRefreshMargin = time.Minute

// tokenSource prints and caches the bearer tokens of a cluster with aws eks get-token.
type tokenSource struct {
	expires time.Time
	aws     mcpserve.AWSRunner
	now     func() time.Time
	cluster string
	profile string
	token   string
	mu      sync.Mutex
}

func newTokenSource(aws mcpserve.AWSRunner, cluster, profile string) *tokenSource {
	return &tokenSource{aws: aws, cluster: cluster, profile: profile, now: time.Now}
}

type execCredential struct {
	Status struct {
		ExpirationTimestamp string `json:"expirationTimestamp"`
		Token               string `json:"token"`
	} `json:"status"`
}

// Token returns a valid token, printing a new one when the cached one is about to expire.
func (s *tokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && s.now().Before(s.expires.Add(-tokenRefreshMargin)) {
		return s.token, nil
	}
	raw, err := s.aws(ctx, "eks", "get-token", "--cluster-name", s.cluster, "--output", "json")
	if err != nil {
		if text := strings.ToLower(err.Error()); strings.Contains(text, "sso") && strings.Contains(text, "expired") {
			return "", fmt.Errorf("%w (run `aws sso login --profile %s`)", err, s.profile)
		}
		return "", fmt.Errorf("failed to get a token for cluster %s: %w", s.cluster, err)
	}
	var credential execCredential
	parseErr := json.Unmarshal([]byte(raw), &credential)
	if parseErr != nil || credential.Status.Token == "" {
		return "", fmt.Errorf("aws eks get-token printed no token for cluster %s", s.cluster)
	}
	expires, err := time.Parse(time.RFC3339, credential.Status.ExpirationTimestamp)
	if err != nil {
		expires = s.now().Add(14 * time.Minute)
	}
	s.token, s.expires = credential.Status.Token, expires
	return s.token, nil
}

// reset drops the cached token, e.g. after the API refused it.
func (s *tokenSource) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
}
