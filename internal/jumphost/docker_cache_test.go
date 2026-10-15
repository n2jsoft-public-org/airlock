package jumphost

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/cachemcp"
	"github.com/n2jsoft/airlock/internal/dockerdb"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// stubCacheMCP records the config of the cache MCP servers instead of starting them.
func stubCacheMCP(t *testing.T) *[]*cachemcp.SessionConfig {
	t.Helper()
	var started []*cachemcp.SessionConfig
	previous := cachemcpStart
	cachemcpStart = func(_ *mcpserve.Host, cfg *cachemcp.SessionConfig) (*cachemcp.Handle, error) {
		started = append(started, cfg)
		return nil, nil
	}
	t.Cleanup(func() { cachemcpStart = previous })
	return &started
}

func redisContainer() dockerdb.Container {
	return dockerdb.Container{Name: "cache", Image: "redis:7", Engine: dockerdb.EngineRedis, Host: "127.0.0.1", Port: 6379, User: "default", Trust: true}
}

func TestPrepareDockerOpensACache(t *testing.T) {
	started := stubCacheMCP(t)
	session, err := dockerRunner(t, redisContainer()).Prepare(context.Background(), Options{ResourceType: "docker", Name: "cache", MCPAllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if session.Label != "DOCKER cache" || session.ResourceType != "docker-cache" || session.Target != "cache" || session.Production ||
		session.LocalPort != 0 || session.Details.IDEDataSource != "" {
		t.Fatalf("session = %+v", session)
	}
	if d := session.Details; d.ConnectedServer != "127.0.0.1:6379" || d.RestartCommand != "airlock docker cache" || d.Auth != "none" {
		t.Fatalf("details = %+v", d)
	}
	if len(session.Intro) < 3 || session.Intro[0] != "Docker container cache (redis) at 127.0.0.1:6379" ||
		session.Intro[1] != "TLS: disable" || session.Intro[2] != "redis-cli -h 127.0.0.1 -p 6379" {
		t.Fatalf("intro = %q", session.Intro)
	}
	if len(*started) != 1 {
		t.Fatalf("started %d cache servers", len(*started))
	}
	cfg := (*started)[0]
	if cfg.Target != "docker cache" || cfg.Engine != "redis" || cfg.Host != "127.0.0.1" || cfg.LocalPort != 6379 || cfg.TLS ||
		cfg.Profile != "" || cfg.Region != "" || cfg.Via != "a local Docker container reached directly at 127.0.0.1:6379." || cfg.Credentials != nil || !cfg.AllowWrites || cfg.ServerPrefix != "acme" {
		t.Fatalf("cache config = %+v", cfg)
	}
	if session.SignIn == nil {
		t.Fatal("a docker cache session checks its sign-in")
	}
}

func TestPrepareDockerCachePasswords(t *testing.T) {
	secured := redisContainer()
	secured.Password, secured.Trust = "s3cret", false
	for _, tc := range []struct{ typed, want string }{{"", "s3cret"}, {"typed", "typed"}} {
		started := stubCacheMCP(t)
		session, err := dockerRunner(t, secured).Prepare(context.Background(), Options{ResourceType: "docker-cache", Name: "cache", DBPassword: tc.typed})
		if err != nil {
			t.Fatal(err)
		}
		user, password, err := (*started)[0].Credentials(context.Background())
		if err != nil || user != "default" || password != tc.want {
			t.Errorf("typed %q: credentials = %q %q %v", tc.typed, user, password, err)
		}
		shown := fmt.Sprintf("%+v %q", session.Details, session.Intro)
		if session.Details.Auth != "password · default" || !slices.Contains(session.Intro, "redis-cli -h 127.0.0.1 -p 6379 --user default --askpass") ||
			strings.Contains(shown, tc.want) {
			t.Errorf("typed %q: details = %+v intro = %q", tc.typed, session.Details, session.Intro)
		}
		session.Release()
	}
}

func TestDockerCacheSignInCheck(t *testing.T) {
	stubCacheMCP(t)
	var conns []cachemcp.ConnConfig
	previous := checkCacheSignIn
	checkCacheSignIn = func(_ context.Context, cfg cachemcp.ConnConfig) error {
		conns = append(conns, cfg)
		return fmt.Errorf("%w: NOAUTH Authentication required", cachemcp.ErrSignInRejected)
	}
	t.Cleanup(func() { checkCacheSignIn = previous })
	session, err := dockerRunner(t, redisContainer()).Prepare(context.Background(), Options{ResourceType: "docker", Name: "cache"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	candidates, err := session.SignIn.Candidates(context.Background())
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %+v, %v", candidates, err)
	}
	err = session.SignIn.Check(context.Background(), candidates[0])
	if !errors.Is(err, ErrSignInRejected) || !errors.Is(err, cachemcp.ErrSignInRejected) ||
		err.Error() != "the cache rejected the credentials: NOAUTH Authentication required" {
		t.Fatalf("check = %v, want the cache's own rejection, matching ErrSignInRejected", err)
	}
	if len(conns) != 1 || conns[0].Host != "127.0.0.1" || conns[0].LocalPort != 6379 || conns[0].TLS || conns[0].Credentials != nil {
		t.Fatalf("conns = %+v", conns)
	}
}

func TestPrepareDockerCacheRefusesASecondSession(t *testing.T) {
	stubCacheMCP(t)
	runner := dockerRunner(t, redisContainer())
	first, err := runner.Prepare(context.Background(), Options{ResourceType: "docker", Name: "cache"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if _, err := runner.Prepare(context.Background(), Options{ResourceType: "docker-cache", Name: "cache"}); err == nil {
		t.Fatal("a container opens once")
	}
}

func TestCacheSignInErrorKeepsTheCacheMessage(t *testing.T) {
	rejected := fmt.Errorf("%w: WRONGPASS invalid username-password pair", cachemcp.ErrSignInRejected)
	err := cacheSignInError(rejected)
	if !errors.Is(err, ErrSignInRejected) || !errors.Is(err, cachemcp.ErrSignInRejected) || err.Error() != rejected.Error() {
		t.Fatalf("err = %v, want the cache's message matching both rejections", err)
	}
	if other := errors.New("dial tcp: connection refused"); !errors.Is(cacheSignInError(other), other) ||
		cacheSignInError(other).Error() != other.Error() {
		t.Fatal("other errors pass through")
	}
}
