package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/keychain"
	"github.com/n2jsoft/airlock/internal/orgconfig"
)

// refuseRequests is a transport failing every request.
type refuseRequests struct{}

func (refuseRequests) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("tests never use the network")
}

// TestMain keeps every command of the tests away from the machine's managed config, the network
// and the keychain.
func TestMain(m *testing.M) {
	orgEnvironment = func() orgEnv {
		env := defaultOrgEnv()
		env.managedPath = ""
		env.fetcher = &orgconfig.Fetcher{
			HTTP:  &http.Client{Transport: refuseRequests{}},
			Token: func(context.Context) (string, error) { return "", errors.New("no token in tests") },
		}
		return env
	}
	os.Exit(m.Run())
}

// useManagedFile makes the commands of the test read a managed config holding content.
func useManagedFile(t *testing.T, content string) {
	t.Helper()
	managed := writeTempFile(t, "managed.json", content)
	previous := orgEnvironment
	orgEnvironment = func() orgEnv {
		env := previous()
		env.managedPath = managed
		return env
	}
	t.Cleanup(func() { orgEnvironment = previous })
}

const testOrgBody = `{"settings":{"aws":{"jumphostTag":"Role=bastion"}},"locked":["aws.jumphostTag"]}`

var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// testOrgEnv reads nothing of the machine: no managed file, a cache under t.TempDir.
func testOrgEnv(t *testing.T, client *http.Client) orgEnv {
	t.Helper()
	return orgEnv{
		cachePath: filepath.Join(t.TempDir(), "organization-cache.json"),
		fetcher:   &orgconfig.Fetcher{HTTP: client},
		now:       func() time.Time { return testNow },
		readFile:  os.ReadFile,
	}
}

// writeTempFile writes content to name in a new temporary directory and returns its path.
func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	writeTestFile(t, path, content)
	return path
}

// countingServer serves body and counts the requests.
func countingServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body)) //nolint:errcheck // test server
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestResolveManagedPointerWinsOverLocal(t *testing.T) {
	managedOrg := writeTempFile(t, "org.json", testOrgBody)
	localOrg := writeTempFile(t, "local-org.json", `{"settings":{"mode":"daemon"}}`)
	env := testOrgEnv(t, nil)
	env.managedPath = writeTempFile(t, "managed.json", `{"organizationConfig": "`+managedOrg+`"}`)
	local := writeTempFile(t, "config.json", `{"organizationConfig": "`+localOrg+`"}`)

	state := resolveOrganization(t.Context(), env, local)
	if state.Org == nil || !state.Managed || state.Source != managedOrg || len(state.Warnings) != 0 {
		t.Fatalf("state = %+v", state)
	}
	if !state.Org.Locked("aws.jumphostTag") || state.Org.Sets("mode") {
		t.Fatalf("organization is not the managed one: %+v", state.Org)
	}
}

func TestResolveManagedInline(t *testing.T) {
	env := testOrgEnv(t, nil)
	env.managedPath = writeTempFile(t, "managed.json", testOrgBody)

	state := resolveOrganization(t.Context(), env, "")
	if state.Org == nil || !state.Managed || !state.Org.Locked("aws.jumphostTag") || len(state.Warnings) != 0 {
		t.Fatalf("state = %+v", state)
	}
}

func TestResolveInvalidManagedFileWarns(t *testing.T) {
	env := testOrgEnv(t, nil)
	env.managedPath = writeTempFile(t, "managed.json", `{"organizationConfig": `)
	localOrg := writeTempFile(t, "org.json", testOrgBody)
	local := writeTempFile(t, "config.json", `{"organizationConfig": "`+localOrg+`"}`)

	state := resolveOrganization(t.Context(), env, local)
	if len(state.Warnings) != 1 || !strings.Contains(state.Warnings[0], env.managedPath) {
		t.Fatalf("warnings = %q", state.Warnings)
	}
	if state.Org == nil || state.Managed || state.Source != localOrg || !state.Org.Locked("aws.jumphostTag") {
		t.Fatalf("the local organizationConfig does not apply: %+v", state)
	}
}

func TestResolveNoCacheFetchesSynchronously(t *testing.T) {
	server, hits := countingServer(t, testOrgBody)
	env := testOrgEnv(t, server.Client())
	source := server.URL + "/org.json"
	local := writeTempFile(t, "config.json", `{"organizationConfig": "`+source+`"}`)

	state := resolveOrganization(t.Context(), env, local)
	if state.Org == nil || state.Managed || state.Stale || state.Source != source || len(state.Warnings) != 0 || hits.Load() != 1 {
		t.Fatalf("state = %+v, %d requests", state, hits.Load())
	}
	if !state.FetchedAt.Equal(testNow) {
		t.Fatalf("FetchedAt = %v, want %v", state.FetchedAt, testNow)
	}
	if cache, ok := orgconfig.LoadCache(env.cachePath).For(source); !ok || string(cache.Body) != testOrgBody {
		t.Fatalf("cache = %+v, %v", cache, ok)
	}
}

func TestResolveUnreachableNoCacheFailsOpen(t *testing.T) {
	server, _ := countingServer(t, testOrgBody)
	env := testOrgEnv(t, server.Client())
	source := server.URL + "/org.json"
	server.Close()
	local := writeTempFile(t, "config.json", `{"organizationConfig": "`+source+`"}`)

	state := resolveOrganization(t.Context(), env, local)
	if state.Org != nil || len(state.Warnings) != 1 || !strings.Contains(state.Warnings[0], "unreachable") {
		t.Fatalf("state = %+v", state)
	}
}

func TestResolveWarningsHideTheURLQuery(t *testing.T) {
	invalid, _ := countingServer(t, `{"settings":{"agent":{"writes":"maybe"}}}`)
	closed, _ := countingServer(t, testOrgBody)
	closedURL := closed.URL
	closed.Close()
	for name, tt := range map[string]struct {
		server *http.Client
		url    string
	}{
		"unreachable": {closed.Client(), closedURL},
		"invalid":     {invalid.Client(), invalid.URL},
	} {
		t.Run(name, func(t *testing.T) {
			env := testOrgEnv(t, tt.server)
			source := tt.url + "/org.json?sig=secret"
			local := writeTempFile(t, "config.json", `{"organizationConfig": "`+source+`"}`)

			state := resolveOrganization(t.Context(), env, local)
			if state.Org != nil || len(state.Warnings) != 1 || state.Source != tt.url+"/org.json" {
				t.Fatalf("state = %+v", state)
			}
			if strings.Contains(state.Warnings[0], "secret") {
				t.Fatalf("warning shows the query: %q", state.Warnings[0])
			}
		})
	}
}

func TestLoadConfigAppliesTheManagedLock(t *testing.T) {
	path := testConfigFile(t)
	writeTestFile(t, path, `{"aws": {"jumphostTag": "Env=local"}}`)
	useManagedFile(t, testOrgBody)

	cmd := NewRootCmd()
	cmd.SetContext(t.Context())
	cfg, sources, err := loadConfig(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JumphostTag.String() != "Role=bastion" || !sources.org.Managed {
		t.Fatalf("jumphostTag = %q, org = %+v", cfg.JumphostTag, sources.org)
	}
}

func TestResolveGitHubWithoutAccessNamesTheFix(t *testing.T) {
	env := testOrgEnv(t, nil)
	env.fetcher.Token = func(context.Context) (string, error) { return "", errors.New("no token") }
	local := writeTempFile(t, "config.json", `{"organizationConfig": "https://github.com/acme/infra/blob/main/airlock.json"}`)

	state := resolveOrganization(t.Context(), env, local)
	if state.Org != nil || len(state.Warnings) != 1 || !strings.Contains(state.Warnings[0], "gh auth login") ||
		!strings.Contains(state.Warnings[0], "airlock config organization-token") {
		t.Fatalf("state = %+v", state)
	}
}

func TestResolveCachedStaleDoesNotFetch(t *testing.T) {
	server, hits := countingServer(t, testOrgBody)
	env := testOrgEnv(t, server.Client())
	source := server.URL + "/org.json"
	fetchedAt := testNow.Add(-2 * time.Hour)
	if err := orgconfig.SaveCache(env.cachePath, orgconfig.Cache{Source: source, FetchedAt: fetchedAt, Body: []byte(testOrgBody)}); err != nil {
		t.Fatal(err)
	}
	local := writeTempFile(t, "config.json", `{"organizationConfig": "`+source+`"}`)

	state := resolveOrganization(t.Context(), env, local)
	if state.Org == nil || !state.Stale || !state.FetchedAt.Equal(fetchedAt) || hits.Load() != 0 {
		t.Fatalf("state = %+v, %d requests", state, hits.Load())
	}
}

func TestResolveCachedPreviousGivesTheChanges(t *testing.T) {
	env := testOrgEnv(t, nil)
	source := "https://example.invalid/org.json"
	previous := `{"settings":{"aws":{"jumphostTag":"Role=old"}}}`
	if err := orgconfig.SaveCache(env.cachePath, orgconfig.Cache{Source: source, FetchedAt: testNow, Body: []byte(testOrgBody), Previous: []byte(previous)}); err != nil {
		t.Fatal(err)
	}
	local := writeTempFile(t, "config.json", `{"organizationConfig": "`+source+`"}`)

	state := resolveOrganization(t.Context(), env, local)
	if state.Stale || len(state.Changes) != 1 || state.Changes[0].Name != "aws.jumphostTag" || !state.Changes[0].Locked {
		t.Fatalf("state = %+v", state)
	}
}

func TestResolveFileSourceNoCache(t *testing.T) {
	env := testOrgEnv(t, nil)
	orgFile := writeTempFile(t, "org.json", testOrgBody)
	local := writeTempFile(t, "config.json", `{"organizationConfig": "`+orgFile+`"}`)

	state := resolveOrganization(t.Context(), env, local)
	if state.Org == nil || len(state.Warnings) != 0 {
		t.Fatalf("state = %+v", state)
	}
	if _, err := os.Stat(env.cachePath); !os.IsNotExist(err) {
		t.Fatalf("cache written for a file source: %v", err)
	}
}

func TestResolveMissingFileSourceWarns(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "org.json")
	local := writeTempFile(t, "config.json", `{"organizationConfig": "`+missing+`"}`)

	state := resolveOrganization(t.Context(), testOrgEnv(t, nil), local)
	if state.Org != nil || len(state.Warnings) != 1 || !strings.Contains(state.Warnings[0], "not found") {
		t.Fatalf("state = %+v", state)
	}
}

func TestOrganizationTokenFallsBackToKeychain(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", t.TempDir()) // no gh
	store := keychain.NewMemoryStore()
	if err := store.Set(organizationTokenAccount, "kc-token"); err != nil {
		t.Fatal(err)
	}
	token, err := organizationToken(store)(t.Context())
	if err != nil || token != "kc-token" {
		t.Fatalf("token = %q, %v", token, err)
	}
}

func TestConfigImportSkipsLockedKeys(t *testing.T) {
	shared := shareString(t, `{"jumphostTag": "Env=bastion"}`)
	path := testConfigFile(t)
	useManagedFile(t, testOrgBody)

	out, err := runAirlock(t, "config", "import", shared, "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(locked by the organization, not imported)\n") || !strings.Contains(out, "Nothing to change") {
		t.Fatalf("out:\n%s", out)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("config.json written: %v", statErr)
	}
}

func TestDaemonRefreshesTheOrganizationAtEveryTick(t *testing.T) {
	server, hits := countingServer(t, testOrgBody)
	env := testOrgEnv(t, server.Client())
	source := server.URL + "/org.json"
	local := writeTempFile(t, "config.json", `{"organizationConfig": "`+source+`"}`)
	// Two ticks, plus one refresh at start when the cache is stale.
	for name, tt := range map[string]struct {
		fetchedAt time.Time
		want      int32
	}{"fresh": {testNow, 2}, "stale": {testNow.Add(-2 * time.Hour), 3}} {
		t.Run(name, func(t *testing.T) {
			hits.Store(0)
			if err := orgconfig.SaveCache(env.cachePath, orgconfig.Cache{Source: source, FetchedAt: tt.fetchedAt, Body: []byte(testOrgBody)}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			tick, done := make(chan time.Time), make(chan struct{})
			go func() {
				// The last refresh may end canceled: its log line does not matter.
				refreshOrganizationEvery(ctx, env, local, tick, func(string, ...any) {})
				close(done)
			}()
			tick <- testNow
			tick <- testNow
			for deadline := time.Now().Add(5 * time.Second); hits.Load() < tt.want && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			cancel()
			<-done
			if got := hits.Load(); got != tt.want {
				t.Fatalf("%d requests, want %d", got, tt.want)
			}
		})
	}
}

func TestResolveWarnsOfARejectedPush(t *testing.T) {
	var body atomic.Value
	body.Store(testOrgBody)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body.Load().(string))) //nolint:errcheck,forcetypeassert // test server
	}))
	t.Cleanup(server.Close)
	env := testOrgEnv(t, server.Client())
	source := server.URL + "/org.json"
	local := writeTempFile(t, "config.json", `{"organizationConfig": "`+source+`"}`)
	state := resolveOrganization(t.Context(), env, local)
	if state.Org == nil || len(state.Warnings) != 0 {
		t.Fatalf("first resolve = %+v", state)
	}

	body.Store(`{"settings":{"agent":{"writes":"maybe"}}}`)
	if err := refreshOrganization(t.Context(), env, state.src); err == nil {
		t.Fatal("the invalid push must be rejected")
	}
	state = resolveOrganization(t.Context(), env, local)
	if state.Org == nil || !state.Org.Locked("aws.jumphostTag") {
		t.Fatalf("the previous organization must keep applying: %+v", state)
	}
	if len(state.Warnings) != 1 || !strings.Contains(state.Warnings[0], "agent.writes") || !strings.Contains(state.Warnings[0], source) {
		t.Fatalf("warnings = %q, want the rejection", state.Warnings)
	}

	body.Store(testOrgBody)
	if err := refreshOrganization(t.Context(), env, state.src); err != nil {
		t.Fatal(err)
	}
	if state = resolveOrganization(t.Context(), env, local); len(state.Warnings) != 0 {
		t.Fatalf("warnings = %q after a valid read", state.Warnings)
	}
}
