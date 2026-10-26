package cli

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/orgconfig"
)

const syncOrgBody = `{"settings":{"aws":{"jumphostTag":"Role=bastion","defaultRegion":"eu-west-3"}}}`

// syncSetup serves body as the organization config of a config.json holding the local aws keys,
// and returns the config.json path and the source.
func syncSetup(t *testing.T, client *http.Client, source, localAWS string) (path string, env orgEnv) {
	t.Helper()
	path = testConfigFile(t)
	env = testOrgEnv(t, client)
	useOrgEnv(t, env)
	writeTestFile(t, path, `{"organizationConfig": "`+source+`", "aws": `+localAWS+`}`)
	return path, env
}

func TestConfigSyncDropsEqualShadowedKeysWithYes(t *testing.T) {
	server, _ := countingServer(t, syncOrgBody)
	source := server.URL + "/org.json"
	path, _ := syncSetup(t, server.Client(), source, `{"jumphostTag": "Role=bastion", "defaultRegion": "us-east-1"}`)

	out, err := runAirlock(t, "config", "sync", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "is up to date") || !strings.Contains(out, "aws.jumphostTag") {
		t.Fatalf("out:\n%s", out)
	}
	content := readConfigFile(t, path)
	if strings.Contains(content, "jumphostTag") || !strings.Contains(content, "us-east-1") || !strings.Contains(content, source) {
		t.Fatalf("config.json:\n%s", content)
	}
}

func TestConfigSyncKeepsDifferingKeys(t *testing.T) {
	server, _ := countingServer(t, syncOrgBody)
	path, _ := syncSetup(t, server.Client(), server.URL+"/org.json", `{"jumphostTag": "Role=bastion", "defaultRegion": "us-east-1"}`)
	before := readConfigFile(t, path)

	// No terminal and no --yes: the equal keys are only listed.
	out, err := runAirlockWithInput(t, "y\n", "config", "sync")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"aws.jumphostTag", "--yes", "aws.defaultRegion overrides the organization value"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out lacks %q:\n%s", want, out)
		}
	}
	if after := readConfigFile(t, path); after != before {
		t.Fatalf("config.json changed:\n%s", after)
	}
}

func TestConfigSyncReportsFetchError(t *testing.T) {
	source := "https://example.invalid/org.json"
	_, env := syncSetup(t, &http.Client{Transport: refuseRequests{}}, source, `{}`)
	if err := orgconfig.SaveCache(env.cachePath, orgconfig.Cache{Source: source, FetchedAt: testNow, Body: []byte(testOrgBody)}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(env.cachePath)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = runAirlock(t, "config", "sync", "--yes"); err == nil || !strings.Contains(err.Error(), source) {
		t.Fatalf("err = %v, want the source named", err)
	}
	if after, readErr := os.ReadFile(env.cachePath); readErr != nil || !bytes.Equal(after, before) {
		t.Fatalf("cache changed: %v\n%s", readErr, after)
	}
}

func TestConfigSyncPrintsTheChangesOnce(t *testing.T) {
	server, _ := countingServer(t, syncOrgBody)
	source := server.URL + "/org.json"
	_, env := syncSetup(t, server.Client(), source, `{}`)
	old := `{"settings":{"aws":{"jumphostTag":"Role=old","defaultRegion":"eu-west-3"}}}`
	if err := orgconfig.SaveCache(env.cachePath, orgconfig.Cache{Source: source, FetchedAt: testNow, Body: []byte(old)}); err != nil {
		t.Fatal(err)
	}

	out, err := runAirlock(t, "config", "sync")
	if err != nil || !strings.Contains(out, "Role=old → Role=bastion") {
		t.Fatalf("out = %q, %v", out, err)
	}
	if cache, _ := orgconfig.LoadCache(env.cachePath).For(source); cache.Previous != nil {
		t.Fatalf("diff not cleared: %s", cache.Previous)
	}
}

func TestConfigSyncWithoutOrganization(t *testing.T) {
	testConfigFile(t)
	useOrgEnv(t, testOrgEnv(t, nil))
	out, err := runAirlock(t, "config", "sync")
	if err != nil || !strings.Contains(out, "No organization config") {
		t.Fatalf("out = %q, %v", out, err)
	}
}

func TestConfigSyncFailsOnABadSource(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "org.json")
	for name, tt := range map[string]struct{ local, managed string }{
		"http source":          {local: `{"organizationConfig": "http://example.com/org.json"}`},
		"managed http source":  {managed: `{"organizationConfig": "http://example.com/org.json"}`},
		"invalid managed file": {managed: `{"organizationConfig": 42}`},
		"missing file source":  {local: `{"organizationConfig": "` + missing + `"}`},
	} {
		t.Run(name, func(t *testing.T) {
			path := testConfigFile(t)
			useOrgEnv(t, testOrgEnv(t, nil))
			if tt.local != "" {
				writeTestFile(t, path, tt.local)
			}
			if tt.managed != "" {
				useManagedFile(t, tt.managed)
			}
			out, err := runAirlock(t, "config", "sync")
			if err == nil || strings.Contains(out, "No organization config") {
				t.Fatalf("out = %q, err = %v; want a failure", out, err)
			}
		})
	}
}
