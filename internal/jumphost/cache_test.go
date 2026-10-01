package jumphost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/cachemcp"
)

const (
	cacheEndpoint = "master.sessions.abc123.euw3.cache.amazonaws.com"
	// groupNoAuth is a Valkey replication group without TLS nor auth.
	groupNoAuth = `{"ReplicationGroupId":"sessions","Engine":"valkey","Status":"available","TransitEncryptionEnabled":false,` +
		`"AuthTokenEnabled":false,"UserGroupIds":[],"ClusterEnabled":false,` +
		`"NodeGroups":[{"PrimaryEndpoint":{"Address":"` + cacheEndpoint + `","Port":6379}}]}`
	// groupRBAC is a Redis replication group with TLS and an RBAC user group.
	groupRBAC = `{"ReplicationGroupId":"sessions","Engine":"redis","Status":"available","TransitEncryptionEnabled":true,` +
		`"AuthTokenEnabled":false,"UserGroupIds":["app-users"],"ClusterEnabled":false,` +
		`"NodeGroups":[{"PrimaryEndpoint":{"Address":"` + cacheEndpoint + `","Port":6379}}]}`
	// groupToken uses a legacy AUTH token.
	groupToken = `{"ReplicationGroupId":"sessions","Engine":"redis","Status":"available","TransitEncryptionEnabled":true,` +
		`"AuthTokenEnabled":true,"UserGroupIds":[],"ClusterEnabled":false,` +
		`"NodeGroups":[{"PrimaryEndpoint":{"Address":"` + cacheEndpoint + `","Port":6379}}]}`
	// groupClusterMode only has a configuration endpoint.
	groupClusterMode = `{"ReplicationGroupId":"sessions","Engine":"redis","Status":"available","TransitEncryptionEnabled":true,` +
		`"AuthTokenEnabled":false,"UserGroupIds":[],"ClusterEnabled":true,` +
		`"ConfigurationEndpoint":{"Address":"clustercfg.sessions.abc123.euw3.cache.amazonaws.com","Port":6379},"NodeGroups":[{"NodeGroupId":"0001"}]}`
	cacheUserGroups = `[{"UserGroupId":"app-users","UserIds":["default-off","app-iam","app-pw"],"ReplicationGroups":["sessions"],"ServerlessCaches":[]},` +
		`{"UserGroupId":"other","UserIds":["other-iam"],"ReplicationGroups":["orders"],"ServerlessCaches":[]}]`
	cacheUsers = `[{"UserId":"default-off","Authentication":{"Type":"no-password"}},{"UserId":"app-iam","Authentication":{"Type":"iam"}},` +
		`{"UserId":"app-pw","Authentication":{"Type":"password"}},{"UserId":"other-iam","Authentication":{"Type":"iam"}}]`
)

// installCacheAWS puts an aws executable on PATH describing the replication group group.
func installCacheAWS(t *testing.T, group string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*get-caller-identity*) echo arn:aws:iam::111111111111:user/dev ;;\n" +
		"*describe-instances*) echo i-0123 ;;\n" +
		"*describe-replication-groups*) echo '" + group + "' ;;\n" +
		"*describe-user-groups*) echo '" + cacheUserGroups + "' ;;\n" +
		"*describe-users*) echo '" + cacheUsers + "' ;;\n" +
		"*get-secret-value*) echo 'plain-auth-token' ;;\n" +
		"esac\n"
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// serverlessCache is a serverless Valkey cache with an RBAC user group.
const serverlessCache = `{"ServerlessCacheName":"sessions","Engine":"valkey","MajorEngineVersion":"8","Status":"available",` +
	`"UserGroupId":"app-users","Endpoint":{"Address":"sessions.serverless.euw3.cache.amazonaws.com","Port":6379}}`

// installServerlessCacheAWS is installCacheAWS for a cache that is no replication group nor cluster.
func installServerlessCacheAWS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*get-caller-identity*) echo arn:aws:iam::111111111111:user/dev ;;\n" +
		"*describe-instances*) echo i-0123 ;;\n" +
		"*describe-replication-groups*|*describe-cache-clusters*) echo not found >&2; exit 254 ;;\n" +
		"*describe-serverless-caches*) echo '" + serverlessCache + "' ;;\n" +
		"esac\n"
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func prepareCache(t *testing.T, options *Options) (*Session, error) {
	t.Helper()
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".claude"), 0o750); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, ProjectDir: project, MCPServerPrefix: "acme"})
	options.ResourceType, options.Name, options.AwsProfile, options.AwsRegion = "elasticache", "sessions", "dev", "eu-west-3"
	options.LocalPortNumber, options.SkipIDEDataSource = 16379, true
	session, err := runner.Prepare(context.Background(), *options)
	if session != nil {
		t.Cleanup(func() { session.Release() })
	}
	return session, err
}

func TestCacheAuthMode(t *testing.T) {
	cases := []struct {
		want   string
		groups []string
		token  bool
	}{{CacheAuthNone, nil, false}, {CacheAuthToken, nil, true}, {CacheAuthRBAC, []string{"g"}, false}, {CacheAuthRBAC, []string{"g"}, true}}
	for _, c := range cases {
		if got := cacheAuthMode(c.token, c.groups); got != c.want {
			t.Errorf("cacheAuthMode(%v, %v) = %q, want %q", c.token, c.groups, got, c.want)
		}
	}
}

func TestPrepareCacheWithoutAuth(t *testing.T) {
	installCacheAWS(t, groupNoAuth)
	session, err := prepareCache(t, &Options{SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	intro := strings.Join(session.Intro, "\n")
	if !strings.Contains(intro, "redis-cli -p 16379") || strings.Contains(intro, "--tls") || strings.Contains(intro, "--user") || session.Details.Auth != "" {
		t.Fatalf("intro =\n%s\nauth = %q", intro, session.Details.Auth)
	}
}

func TestPrepareCacheWithIAMUser(t *testing.T) {
	installCacheAWS(t, groupRBAC)
	session, err := prepareCache(t, &Options{DBUser: "app-iam", SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	intro := strings.Join(session.Intro, "\n")
	if !strings.Contains(intro, "redis-cli -p 16379 --tls --sni "+cacheEndpoint+" --user app-iam --askpass") {
		t.Fatalf("intro =\n%s", intro)
	}
	if session.Details.Auth != "AWS IAM token (generated by airlock) · app-iam" || !strings.Contains(session.Details.RestartCommand, "--db-user app-iam") {
		t.Fatalf("details = %+v", session.Details)
	}
}

func TestPrepareCacheRefusesIAMWithoutUserGroup(t *testing.T) {
	installCacheAWS(t, groupToken)
	if _, err := prepareCache(t, &Options{DBUser: "app-iam", SignIn: SignInIAM}); err == nil || !strings.Contains(err.Error(), "RBAC user group") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareCacheWithSecretDefaultsToTheDefaultUser(t *testing.T) {
	installCacheAWS(t, groupToken)
	session, err := prepareCache(t, &Options{DBSecretARN: "arn:aws:secretsmanager:eu-west-3:1:secret:cache-token", SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(session.Details.Auth, " · default") || !strings.HasPrefix(session.Details.Auth, "Secrets Manager · ") {
		t.Fatalf("auth = %q", session.Details.Auth)
	}
}

func TestPrepareCacheInClusterModeUsesTheConfigurationEndpoint(t *testing.T) {
	installCacheAWS(t, groupClusterMode)
	session, err := prepareCache(t, &Options{SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(session.Details.ConnectedServer, "clustercfg.sessions.abc123.euw3.cache.amazonaws.com:6379") {
		t.Fatalf("connected server = %q", session.Details.ConnectedServer)
	}
}

func TestIAMUsersOf(t *testing.T) {
	users, err := iamUsersOf(cacheUserGroups, cacheUsers, "sessions")
	if err != nil || !reflect.DeepEqual(users, []string{"app-iam"}) {
		t.Fatalf("users = %v, %v", users, err)
	}
	none, err := iamUsersOf(cacheUserGroups, cacheUsers, "unknown")
	if err != nil || len(none) != 0 {
		t.Fatalf("users of an unknown cache = %v", none)
	}
}

func TestStartCacheMCPRequiresSignInForAuthCaches(t *testing.T) {
	installCacheAWS(t, groupRBAC)
	session, err := prepareCache(t, &Options{})
	if err != nil {
		t.Fatal(err)
	}
	if session.MCPServer() != nil || !strings.HasPrefix(session.Details.MCP, "not started: this cache requires a sign-in") {
		t.Fatalf("MCP = %q", session.Details.MCP)
	}
}

func TestPrepareCacheStartsTheMCPServer(t *testing.T) {
	installCacheAWS(t, groupNoAuth)
	session, err := prepareCache(t, &Options{MCPAllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	server := session.MCPServer()
	if server == nil || !server.AllowsWrites() || !strings.HasPrefix(session.Details.MCP, "WRITES ENABLED · acme-dev-sessions-cache ") {
		t.Fatalf("MCP = %q", session.Details.MCP)
	}
}

func TestPrepareMemcachedHasNoMCPServer(t *testing.T) {
	installCacheAWS(t, strings.Replace(groupNoAuth, `"Engine":"valkey"`, `"Engine":"memcached"`, 1))
	session, err := prepareCache(t, &Options{})
	if err != nil {
		t.Fatal(err)
	}
	if session.MCPServer() != nil || session.Details.MCP != "" || strings.Contains(strings.Join(session.Intro, "\n"), "redis-cli") {
		t.Fatalf("MCP = %q, intro = %v", session.Details.MCP, session.Intro)
	}
}

func TestCacheSessionSignInCheckMapsRejection(t *testing.T) {
	installCacheAWS(t, groupRBAC)
	session, err := prepareCache(t, &Options{DBUser: "app-iam", SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if session.SignIn == nil {
		t.Fatal("an IAM cache session must check its sign-in")
	}
	previous := checkCacheSignIn
	checkCacheSignIn = func(context.Context, cachemcp.ConnConfig) error {
		return fmt.Errorf("%w: WRONGPASS invalid username-password pair", cachemcp.ErrSignInRejected)
	}
	t.Cleanup(func() { checkCacheSignIn = previous })

	candidates, err := session.SignIn.Candidates(context.Background())
	if err != nil || len(candidates) != 1 || candidates[0].Label != "AWS IAM" || candidates[0].User != "app-iam" {
		t.Fatalf("candidates = %+v, %v", candidates, err)
	}
	if err := session.SignIn.Check(context.Background(), candidates[0]); !errors.Is(err, ErrSignInRejected) {
		t.Fatalf("check = %v, want ErrSignInRejected", err)
	}
}

func TestCacheWithoutAuthHasNoSignInCheck(t *testing.T) {
	installCacheAWS(t, groupNoAuth)
	session, err := prepareCache(t, &Options{SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if session.SignIn != nil {
		t.Fatal("a cache without auth has nothing to check")
	}
}

func TestPrepareServerlessCacheConnectsWithTLS(t *testing.T) {
	installServerlessCacheAWS(t)
	session, err := prepareCache(t, &Options{SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if intro := strings.Join(session.Intro, "\n"); !strings.Contains(intro, "--tls --sni sessions.serverless.euw3.cache.amazonaws.com") {
		t.Fatalf("intro =\n%s", intro)
	}
}

func TestPrepareServerlessCacheSignsInWithAWSIAM(t *testing.T) {
	installServerlessCacheAWS(t)
	session, err := prepareCache(t, &Options{DBUser: "app-iam", SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if session.Details.Auth != "AWS IAM token (generated by airlock) · app-iam" || session.SignIn == nil {
		t.Fatalf("auth = %q, sign-in = %v", session.Details.Auth, session.SignIn)
	}
}

// cacheNode is the node sessions-0001-001 of the replication group sessions (groupRBAC).
const cacheNode = `{"CacheClusterId":"sessions-0001-001","ReplicationGroupId":"sessions","Engine":"redis","EngineVersion":"7.1",` +
	`"CacheClusterStatus":"available","TransitEncryptionEnabled":false,"AuthTokenEnabled":false,` +
	`"CacheNodes":[{"Endpoint":{"Address":"sessions-0001-001.sessions.abc123.euw3.cache.amazonaws.com","Port":6379}}]}`

// installCacheNodeAWS is installCacheAWS for the node id of an RBAC replication group: the node
// id is no replication group, the group is described by its own id.
func installCacheNodeAWS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*get-caller-identity*) echo arn:aws:iam::111111111111:user/dev ;;\n" +
		"*describe-instances*) echo i-0123 ;;\n" +
		"*describe-replication-groups*sessions-0001-001*) echo 'Replication group not found' >&2; exit 254 ;;\n" +
		"*describe-replication-groups*--replication-group-id\\ sessions\\ *) echo '" + groupRBAC + "' ;;\n" +
		"*describe-cache-clusters*) echo '" + cacheNode + "' ;;\n" +
		"*export-credentials*) echo '{\"Version\":1,\"AccessKeyId\":\"AKID\",\"SecretAccessKey\":\"SECRET\"}' ;;\n" +
		"esac\n"
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestPrepareCacheNodeOfAReplicationGroupUsesItsUserGroups(t *testing.T) {
	installCacheNodeAWS(t)
	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, ProjectDir: t.TempDir()})
	session, err := runner.Prepare(context.Background(), Options{
		ResourceType: "elasticache", Name: "sessions-0001-001", AwsProfile: "dev", AwsRegion: "eu-west-3",
		LocalPortNumber: 16379, SkipIDEDataSource: true, SkipMCP: true, DBUser: "app-iam", SignIn: SignInIAM,
	})
	if err != nil {
		t.Fatalf("err = %v, want the RBAC user group of the replication group to allow AWS IAM", err)
	}
	t.Cleanup(func() { session.Release() })
	if intro := strings.Join(session.Intro, "\n"); !strings.Contains(intro, "--tls") || !strings.Contains(intro, "--user app-iam") {
		t.Fatalf("intro =\n%s\nwant the TLS and user of the replication group", intro)
	}

	// The IAM token is signed for the replication group, not the node.
	var token string
	previous := checkCacheSignIn
	checkCacheSignIn = func(ctx context.Context, cfg cachemcp.ConnConfig) error {
		var tokenErr error
		_, token, tokenErr = cfg.Credentials(ctx)
		return tokenErr
	}
	t.Cleanup(func() { checkCacheSignIn = previous })
	candidates, err := session.SignIn.Candidates(context.Background())
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %+v, %v", candidates, err)
	}
	if err := session.SignIn.Check(context.Background(), candidates[0]); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "sessions/?Action=connect&User=app-iam&") {
		t.Fatalf("token = %s, want one signed for the replication group sessions", token)
	}
}
