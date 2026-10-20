package jumphost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft/airlock/internal/cachemcp"
)

const (
	cacheEndpoint = "master.sessions.abc123.euw3.cache.amazonaws.com"
	// groupNoAuth is a Valkey replication group without TLS nor auth.
	groupNoAuth = `<ReplicationGroup><ReplicationGroupId>sessions</ReplicationGroupId><Engine>valkey</Engine><Status>available</Status>` +
		`<TransitEncryptionEnabled>false</TransitEncryptionEnabled><AuthTokenEnabled>false</AuthTokenEnabled><UserGroupIds></UserGroupIds>` +
		`<ClusterEnabled>false</ClusterEnabled><NodeGroups><NodeGroup><PrimaryEndpoint><Address>` + cacheEndpoint + `</Address><Port>6379</Port>` +
		`</PrimaryEndpoint></NodeGroup></NodeGroups></ReplicationGroup>`
	// groupRBAC is a Redis replication group with TLS and an RBAC user group.
	groupRBAC = `<ReplicationGroup><ReplicationGroupId>sessions</ReplicationGroupId><Engine>redis</Engine><Status>available</Status>` +
		`<TransitEncryptionEnabled>true</TransitEncryptionEnabled><AuthTokenEnabled>false</AuthTokenEnabled>` +
		`<UserGroupIds><member>app-users</member></UserGroupIds><ClusterEnabled>false</ClusterEnabled>` +
		`<NodeGroups><NodeGroup><PrimaryEndpoint><Address>` + cacheEndpoint + `</Address><Port>6379</Port></PrimaryEndpoint></NodeGroup></NodeGroups></ReplicationGroup>`
	// groupToken uses a legacy AUTH token.
	groupToken = `<ReplicationGroup><ReplicationGroupId>sessions</ReplicationGroupId><Engine>redis</Engine><Status>available</Status>` +
		`<TransitEncryptionEnabled>true</TransitEncryptionEnabled><AuthTokenEnabled>true</AuthTokenEnabled><UserGroupIds></UserGroupIds>` +
		`<ClusterEnabled>false</ClusterEnabled><NodeGroups><NodeGroup><PrimaryEndpoint><Address>` + cacheEndpoint + `</Address><Port>6379</Port>` +
		`</PrimaryEndpoint></NodeGroup></NodeGroups></ReplicationGroup>`
	// groupClusterMode only has a configuration endpoint.
	groupClusterMode = `<ReplicationGroup><ReplicationGroupId>sessions</ReplicationGroupId><Engine>redis</Engine><Status>available</Status>` +
		`<TransitEncryptionEnabled>true</TransitEncryptionEnabled><AuthTokenEnabled>false</AuthTokenEnabled><UserGroupIds></UserGroupIds>` +
		`<ClusterEnabled>true</ClusterEnabled><ConfigurationEndpoint><Address>clustercfg.sessions.abc123.euw3.cache.amazonaws.com</Address>` +
		`<Port>6379</Port></ConfigurationEndpoint><NodeGroups><NodeGroup><NodeGroupId>0001</NodeGroupId></NodeGroup></NodeGroups></ReplicationGroup>`
)

// cacheUserGroups and cacheUsers are the RBAC user groups and users of the account.
var (
	cacheUserGroups = []string{
		`<member><UserGroupId>app-users</UserGroupId><UserIds><member>default-off</member><member>app-iam</member><member>app-pw</member></UserIds>` +
			`<ReplicationGroups><member>sessions</member></ReplicationGroups><ServerlessCaches></ServerlessCaches></member>`,
		`<member><UserGroupId>other</UserGroupId><UserIds><member>other-iam</member></UserIds>` +
			`<ReplicationGroups><member>orders</member></ReplicationGroups><ServerlessCaches></ServerlessCaches></member>`,
	}
	cacheUsers = []string{
		`<member><UserId>default-off</UserId><Authentication><Type>no-password</Type></Authentication></member>`,
		`<member><UserId>app-iam</UserId><Authentication><Type>iam</Type></Authentication></member>`,
		`<member><UserId>app-pw</UserId><Authentication><Type>password</Type></Authentication></member>`,
		`<member><UserId>other-iam</UserId><Authentication><Type>iam</Type></Authentication></member>`,
	}
)

// fakeElastiCache serves the ElastiCache describes to the SDK: groups, clusters and serverless map
// the id of a cache to its XML element. A describe by an unknown id is a not-found fault; lists
// return one element per page, so that every list goes through pagination. other serves the other
// operations.
type fakeElastiCache struct {
	groups, clusters, serverless map[string]string
	other                        http.HandlerFunc
	userGroups, users            []string
}

// installElastiCache points the SDK of profile dev at f, signed in, with a jumphost and the tunnel tools.
func installElastiCache(t *testing.T, f *fakeElastiCache) {
	t.Helper()
	installAWSAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch op := awsapitest.Operation(r); op {
		case "DescribeReplicationGroups":
			respondCaches(t, w, r, op, "ReplicationGroups", "Marker", f.groups, "ReplicationGroupId")
		case "DescribeCacheClusters":
			if r.FormValue("ShowCacheNodeInfo") != "true" {
				t.Error("DescribeCacheClusters without ShowCacheNodeInfo has no endpoint")
			}
			respondCaches(t, w, r, op, "CacheClusters", "Marker", f.clusters, "CacheClusterId")
		case "DescribeServerlessCaches":
			respondCaches(t, w, r, op, "ServerlessCaches", "NextToken", f.serverless, "ServerlessCacheName")
		case "DescribeUserGroups":
			respondPage(t, w, r, op, "UserGroups", "Marker", f.userGroups)
		case "DescribeUsers":
			respondPage(t, w, r, op, "Users", "Marker", f.users)
		default:
			if f.other == nil {
				t.Errorf("unexpected operation %s", op)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.other(w, r)
		}
	})
	installTunnelTools(t)
}

// respondCaches answers a describe of caches: the one named by the idParam form value, else all.
func respondCaches(t *testing.T, w http.ResponseWriter, r *http.Request, op, field, token string, caches map[string]string, idParam string) {
	id := r.FormValue(idParam)
	if id == "" {
		respondPage(t, w, r, op, field, token, slices.Collect(maps.Values(caches)))
		return
	}
	cache, ok := caches[id]
	if !ok {
		awsapitest.RespondXML(t, w, http.StatusNotFound, `<ErrorResponse><Error><Type>Sender</Type><Code>CacheNotFound</Code>`+
			`<Message>`+id+` not found</Message></Error></ErrorResponse>`)
		return
	}
	respondPage(t, w, r, op, field, token, []string{cache})
}

// respondPage answers the page of elements (one per page, sorted) that the token form value names.
func respondPage(t *testing.T, w http.ResponseWriter, r *http.Request, op, field, token string, elements []string) {
	slices.Sort(elements)
	page, next := pageIndex(t, r.FormValue(token)), ""
	var element string
	if page < len(elements) {
		element = elements[page]
	}
	if page+1 < len(elements) {
		next = fmt.Sprintf("<%s>page%d</%[1]s>", token, page+2)
	}
	awsapitest.RespondXML(t, w, http.StatusOK, "<"+op+"Response><"+op+"Result><"+field+">"+element+"</"+field+">"+next+
		"</"+op+"Result></"+op+"Response>")
}

// installCacheGroup serves the replication group group (named sessions) and the RBAC users.
func installCacheGroup(t *testing.T, group string) {
	t.Helper()
	installElastiCache(t, &fakeElastiCache{groups: map[string]string{"sessions": group}, userGroups: cacheUserGroups, users: cacheUsers})
}

// serverlessCache is a serverless Valkey cache with an RBAC user group.
const serverlessCache = `<member><ServerlessCacheName>sessions</ServerlessCacheName><Engine>valkey</Engine><MajorEngineVersion>8</MajorEngineVersion>` +
	`<Status>available</Status><UserGroupId>app-users</UserGroupId><Endpoint><Address>sessions.serverless.euw3.cache.amazonaws.com</Address>` +
	`<Port>6379</Port></Endpoint></member>`

// installServerlessCache serves a cache that is no replication group nor cluster.
func installServerlessCache(t *testing.T) {
	t.Helper()
	installElastiCache(t, &fakeElastiCache{serverless: map[string]string{"sessions": serverlessCache}})
}

func prepareCache(t *testing.T, options *Options) (*Session, error) {
	t.Helper()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".mcp.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, ProjectDir: project, MCPServerPrefix: "acme"})
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
	installCacheGroup(t, groupNoAuth)
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
	installCacheGroup(t, groupRBAC)
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
	installCacheGroup(t, groupToken)
	if _, err := prepareCache(t, &Options{DBUser: "app-iam", SignIn: SignInIAM}); err == nil || !strings.Contains(err.Error(), "RBAC user group") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareCacheWithSecretDefaultsToTheDefaultUser(t *testing.T) {
	secrets := &fakeSecretsManager{t: t, values: map[string]string{"arn:aws:secretsmanager:eu-west-3:1:secret:cache-token": "plain-auth-token"}}
	installElastiCache(t, &fakeElastiCache{groups: map[string]string{"sessions": groupToken}, other: secrets.serve})
	session, err := prepareCache(t, &Options{DBSecretARN: "arn:aws:secretsmanager:eu-west-3:1:secret:cache-token", SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(session.Details.Auth, " · default") || !strings.HasPrefix(session.Details.Auth, "Secrets Manager · ") {
		t.Fatalf("auth = %q", session.Details.Auth)
	}
}

func TestPrepareCacheInClusterModeUsesTheConfigurationEndpoint(t *testing.T) {
	installCacheGroup(t, groupClusterMode)
	session, err := prepareCache(t, &Options{SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(session.Details.ConnectedServer, "clustercfg.sessions.abc123.euw3.cache.amazonaws.com:6379") {
		t.Fatalf("connected server = %q", session.Details.ConnectedServer)
	}
}

func TestIAMUsersOf(t *testing.T) {
	installCacheGroup(t, groupRBAC)
	runner := testRunner(t)
	users, err := runner.ListCacheIAMUsers(t.Context(), &Options{ResourceType: "elasticache", Name: "sessions", AwsProfile: "dev", AwsRegion: "eu-west-3"})
	if err != nil || !reflect.DeepEqual(users, []string{"app-iam"}) {
		t.Fatalf("users = %v, %v", users, err)
	}
	none, err := runner.ListCacheIAMUsers(t.Context(), &Options{ResourceType: "elasticache", Name: "unknown", AwsProfile: "dev", AwsRegion: "eu-west-3"})
	if err != nil || len(none) != 0 {
		t.Fatalf("users of an unknown cache = %v", none)
	}
}

func TestStartCacheMCPRequiresSignInForAuthCaches(t *testing.T) {
	installCacheGroup(t, groupRBAC)
	session, err := prepareCache(t, &Options{})
	if err != nil {
		t.Fatal(err)
	}
	if session.MCPServer() != nil || !strings.HasPrefix(session.Details.MCP, "not started: this cache requires a sign-in") {
		t.Fatalf("MCP = %q", session.Details.MCP)
	}
}

func TestPrepareCacheStartsTheMCPServer(t *testing.T) {
	installCacheGroup(t, groupNoAuth)
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
	installCacheGroup(t, strings.Replace(groupNoAuth, "<Engine>valkey</Engine>", "<Engine>memcached</Engine>", 1))
	session, err := prepareCache(t, &Options{})
	if err != nil {
		t.Fatal(err)
	}
	if session.MCPServer() != nil || session.Details.MCP != "" || strings.Contains(strings.Join(session.Intro, "\n"), "redis-cli") {
		t.Fatalf("MCP = %q, intro = %v", session.Details.MCP, session.Intro)
	}
}

func TestCacheSessionSignInCheckMapsRejection(t *testing.T) {
	installCacheGroup(t, groupRBAC)
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
	installCacheGroup(t, groupNoAuth)
	session, err := prepareCache(t, &Options{SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if session.SignIn != nil {
		t.Fatal("a cache without auth has nothing to check")
	}
}

func TestPrepareServerlessCacheConnectsWithTLS(t *testing.T) {
	installServerlessCache(t)
	session, err := prepareCache(t, &Options{SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if intro := strings.Join(session.Intro, "\n"); !strings.Contains(intro, "--tls --sni sessions.serverless.euw3.cache.amazonaws.com") {
		t.Fatalf("intro =\n%s", intro)
	}
}

func TestPrepareServerlessCacheSignsInWithAWSIAM(t *testing.T) {
	installServerlessCache(t)
	session, err := prepareCache(t, &Options{DBUser: "app-iam", SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if session.Details.Auth != "AWS IAM token (generated by airlock) · app-iam" || session.SignIn == nil {
		t.Fatalf("auth = %q, sign-in = %v", session.Details.Auth, session.SignIn)
	}
}

// cacheNode is the node sessions-0001-001 of the replication group sessions (groupRBAC).
const cacheNode = `<CacheCluster><CacheClusterId>sessions-0001-001</CacheClusterId><ReplicationGroupId>sessions</ReplicationGroupId>` +
	`<Engine>redis</Engine><EngineVersion>7.1</EngineVersion><CacheClusterStatus>available</CacheClusterStatus>` +
	`<TransitEncryptionEnabled>false</TransitEncryptionEnabled><AuthTokenEnabled>false</AuthTokenEnabled><CacheNodes><CacheNode><Endpoint>` +
	`<Address>sessions-0001-001.sessions.abc123.euw3.cache.amazonaws.com</Address><Port>6379</Port></Endpoint></CacheNode></CacheNodes></CacheCluster>`

func TestPrepareCacheNodeOfAReplicationGroupUsesItsUserGroups(t *testing.T) {
	// The node id is no replication group: the group is described by its own id.
	installElastiCache(t, &fakeElastiCache{groups: map[string]string{"sessions": groupRBAC}, clusters: map[string]string{"sessions-0001-001": cacheNode}})
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, ProjectDir: t.TempDir()})
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

// cacheCluster is the XML element of a standalone cache cluster id reached at host:6379.
func cacheCluster(id, engine, host string) string {
	return `<CacheCluster><CacheClusterId>` + id + `</CacheClusterId><Engine>` + engine + `</Engine>` +
		`<CacheClusterStatus>available</CacheClusterStatus><AuthTokenEnabled>false</AuthTokenEnabled>` +
		`<CacheNodes><CacheNode><Endpoint><Address>` + host + `</Address><Port>6379</Port></Endpoint></CacheNode></CacheNodes></CacheCluster>`
}

func listCaches(t *testing.T) ([]ResourceSummary, error) {
	t.Helper()
	return testRunner(t).ListResourceSummaries(t.Context(), &Options{ResourceType: "elasticache", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
}

func TestCacheTargetsListsEveryPage(t *testing.T) {
	installElastiCache(t, &fakeElastiCache{
		groups: map[string]string{"sessions": groupRBAC, "orders": strings.ReplaceAll(groupClusterMode, "sessions", "orders")},
		clusters: map[string]string{
			"legacy": cacheCluster("legacy", "memcached", "legacy.cache.amazonaws.com"),
			"single": cacheCluster("single", "valkey", "single.cache.amazonaws.com"),
		},
		serverless: map[string]string{
			"sessions-sl": strings.ReplaceAll(serverlessCache, "<ServerlessCacheName>sessions<", "<ServerlessCacheName>sessions-sl<"),
			"tokens":      strings.ReplaceAll(serverlessCache, "<ServerlessCacheName>sessions<", "<ServerlessCacheName>tokens<"),
		},
	})
	got, err := listCaches(t)
	want := []ResourceSummary{
		{Name: "legacy", Type: "elasticache", Description: "legacy.cache.amazonaws.com:6379 | available", Engine: "memcached", CacheAuth: CacheAuthNone},
		{Name: "orders", Type: "elasticache", Description: "clustercfg.orders.abc123.euw3.cache.amazonaws.com:6379 | available", Engine: "redis", CacheAuth: CacheAuthNone},
		{Name: "sessions", Type: "elasticache", Description: cacheEndpoint + ":6379 | available", Engine: "redis", CacheAuth: CacheAuthRBAC},
		{Name: "sessions-sl", Type: "elasticache", Description: "sessions.serverless.euw3.cache.amazonaws.com:6379 | available", Engine: "valkey", CacheAuth: CacheAuthRBAC},
		{Name: "single", Type: "elasticache", Description: "single.cache.amazonaws.com:6379 | available", Engine: "valkey", CacheAuth: CacheAuthNone},
		{Name: "tokens", Type: "elasticache", Description: "sessions.serverless.euw3.cache.amazonaws.com:6379 | available", Engine: "valkey", CacheAuth: CacheAuthRBAC},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %+v, %v;\nwant %+v", got, err, want)
	}
}

func TestCacheTargetsSkipReplicationGroupMembers(t *testing.T) {
	// An empty ReplicationGroupId is falsy too: the cluster is standalone.
	blank := strings.Replace(cacheCluster("blank-group", "valkey", "blank.cache.amazonaws.com"),
		"</CacheClusterId>", "</CacheClusterId><ReplicationGroupId></ReplicationGroupId>", 1)
	installElastiCache(t, &fakeElastiCache{
		groups: map[string]string{"sessions": groupNoAuth},
		clusters: map[string]string{
			"sessions-0001-001": cacheNode,
			"blank-group":       blank,
			"single":            cacheCluster("single", "valkey", "single.cache.amazonaws.com"),
		},
	})
	got, err := listCaches(t)
	var names []string
	for _, target := range got {
		names = append(names, target.Name)
	}
	if err != nil || !slices.Equal(names, []string{"blank-group", "sessions", "single"}) {
		t.Fatalf("targets = %v, %v; want the standalone clusters and the group, not its node", names, err)
	}
}

func TestCacheTargetsToleratesAFailingListing(t *testing.T) {
	groups := map[string]string{"sessions": groupNoAuth}
	installAWSAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if op := awsapitest.Operation(r); op == "DescribeReplicationGroups" {
			respondCaches(t, w, r, op, "ReplicationGroups", "Marker", groups, "ReplicationGroupId")
			return
		}
		awsapitest.RespondXML(t, w, http.StatusBadRequest, accessDeniedXML)
	})
	got, err := listCaches(t)
	if err != nil || len(got) != 1 || got[0].Name != "sessions" {
		t.Fatalf("targets = %+v, %v; want the replication group listed all the same", got, err)
	}
}

// accessDeniedXML is the reply of a query-protocol API to a call that IAM denies.
const accessDeniedXML = `<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code>` +
	`<Message>User is not authorized to perform this action</Message></Error></ErrorResponse>`

func TestCacheTargetsReportEveryListingFailing(t *testing.T) {
	installAWSAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		awsapitest.RespondXML(t, w, http.StatusBadRequest, accessDeniedXML)
	})
	_, err := listCaches(t)
	if err == nil || !strings.Contains(err.Error(), "failed to list ElastiCache caches") || !strings.Contains(err.Error(), "not authorized") ||
		strings.Contains(err.Error(), "operation error") {
		t.Fatalf("err = %v, want the listing failure without the SDK operation chain", err)
	}
}

func TestCacheTargetsKeepUnreachableErrors(t *testing.T) {
	srv := awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev")
	srv.Close()
	_, err := testRunner(t).listResources(t.Context(), awsConfig{Profile: "dev", Region: "eu-west-3"}, "elasticache")
	if err == nil || !strings.Contains(err.Error(), "failed to list ElastiCache caches") || strings.Contains(err.Error(), "sign in") {
		t.Fatalf("err = %v, want the ElastiCache listing failing without asking to sign in", err)
	}
}

func TestCachePortIsTheEndpointPort(t *testing.T) {
	cases := map[string]fakeElastiCache{
		"group":      {groups: map[string]string{"sessions": strings.Replace(groupNoAuth, "<Port>6379<", "<Port>16380<", 1)}},
		"cluster":    {clusters: map[string]string{"sessions": strings.Replace(cacheCluster("sessions", "valkey", "h"), "<Port>6379<", "<Port>16380<", 1)}},
		"serverless": {serverless: map[string]string{"sessions": strings.Replace(serverlessCache, "<Port>6379<", "<Port>16380<", 1)}},
		"none":       {},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			installElastiCache(t, &f)
			port, err := testRunner(t).resolvePreferredLocalPort(t.Context(), &Options{ResourceType: "elasticache", Name: "sessions", AwsProfile: "dev", AwsRegion: "eu-west-3"})
			want := 16380
			if name == "none" {
				want = defaultRedisPort
			}
			if err != nil || port != want {
				t.Fatalf("port = %d, %v; want %d", port, err, want)
			}
		})
	}
}

func TestListCacheIAMUsersReportsAFailure(t *testing.T) {
	installAWSAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		awsapitest.RespondXML(t, w, http.StatusBadRequest, accessDeniedXML)
	})
	_, err := testRunner(t).ListCacheIAMUsers(t.Context(), &Options{Name: "sessions", AwsProfile: "dev", AwsRegion: "eu-west-3"})
	if err == nil || !strings.Contains(err.Error(), "ElastiCache user groups") || strings.Contains(err.Error(), "operation error") {
		t.Fatalf("err = %v, want the user groups failing without the SDK operation chain", err)
	}
}
