package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/keychain"
	"github.com/n2jsoft/airlock/internal/orgconfig"
)

// runAirlock runs the root command with args and returns what it printed on standard output.
func runAirlock(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCmd()
	var out, stderr strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// testConfigFile points the config directory to a temporary one and returns its config file.
func testConfigFile(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	return filepath.Join(root, "airlock", "config.json")
}

func TestConfigPathPrintsTheFile(t *testing.T) {
	path := testConfigFile(t)
	out, err := runAirlock(t, "config", "path")
	if err != nil || out != path+"\n" {
		t.Fatalf("out = %q, %v, want %q", out, err, path)
	}
}

func TestConfigShowWithoutFileListsTheDefaults(t *testing.T) {
	path := testConfigFile(t)
	out, err := runAirlock(t, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{path, "not created: run `airlock config init`", "jumphostTag", "Usage=jumphost", "default", "defaultRegion", "(none)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestConfigShowTellsFileValuesApart(t *testing.T) {
	path := testConfigFile(t)
	writeTestFile(t, path, `{"aws": {"defaultRegion": "eu-west-3"}}`)
	out, err := runAirlock(t, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "not created") {
		t.Fatalf("the file exists:\n%s", out)
	}
	var regionLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "aws.defaultRegion") {
			regionLine = line
		}
	}
	if fields := strings.Fields(regionLine); len(fields) != 3 || fields[1] != "eu-west-3" || fields[2] != "file" {
		t.Fatalf("defaultRegion line = %q, want its value and the file source", regionLine)
	}
}

func TestConfigShowJSONPrintsEffectiveValues(t *testing.T) {
	path := testConfigFile(t)
	writeTestFile(t, path, `{"defaultRegion": "eu-west-3"}`)
	out, err := runAirlock(t, "config", "show", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(out), &values); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if values["aws.defaultRegion"] != "eu-west-3" || values["aws.jumphostTag"] != "Usage=jumphost" || len(values) != len(config.Keys) {
		t.Fatalf("values = %v", values)
	}
}

func TestConfigShowReportsAnInvalidFile(t *testing.T) {
	path := testConfigFile(t)
	writeTestFile(t, path, `{"jumphost": "x"}`)
	if _, err := runAirlock(t, "config", "show"); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want the invalid file named", err)
	}
}

func TestConfigInitWritesTheTemplateOnce(t *testing.T) {
	path := testConfigFile(t)
	out, err := runAirlock(t, "config", "init")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Wrote " + path, "jumphostTag", "readOnlySecretTag", "`airlock config set <key> <value>`"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if cfg, loadErr := config.Load(path, nil); loadErr != nil || !reflect.DeepEqual(cfg, config.Defaults()) {
		t.Fatalf("template = %+v, %v", cfg, loadErr)
	}

	if _, err = runAirlock(t, "config", "init"); err == nil || err.Error() != path+" already exists: use --force to overwrite" {
		t.Fatalf("second init err = %v", err)
	}

	out, err = runAirlock(t, "config", "init", "--force")
	if err != nil || !strings.Contains(out, path+".bak") {
		t.Fatalf("forced init = %q, %v, want the backup named", out, err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("no backup: %v", err)
	}
}

func TestConfigSetAndUnset(t *testing.T) {
	path := testConfigFile(t)
	out, err := runAirlock(t, "config", "set", "jumphostTag", "Env=bastion")
	if err != nil || !strings.Contains(out, "jumphostTag = Env=bastion") {
		t.Fatalf("set = %q, %v", out, err)
	}
	if cfg, loadErr := config.Load(path, nil); loadErr != nil || cfg.JumphostTag.String() != "Env=bastion" {
		t.Fatalf("cfg = %+v, %v", cfg, loadErr)
	}

	if _, err = runAirlock(t, "config", "set", "jumphostTag", "Env"); err == nil {
		t.Fatal("a malformed tag must be refused")
	}
	if _, err = runAirlock(t, "config", "set", "jumphostTag"); err == nil {
		t.Fatal("set needs a key and a value")
	}

	out, err = runAirlock(t, "config", "unset", "jumphostTag")
	if err != nil || !strings.Contains(out, "the default applies") {
		t.Fatalf("unset = %q, %v", out, err)
	}
	out, err = runAirlock(t, "config", "unset", "jumphostTag")
	if err != nil || !strings.Contains(out, "jumphostTag was not set") {
		t.Fatalf("second unset = %q, %v", out, err)
	}
}

func TestConfigSetTellsTheNewNameOfAFlatKey(t *testing.T) {
	path := testConfigFile(t)
	cmd := NewRootCmd()
	var out, stderr strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"config", "set", "mcpGatewayPort", "8123"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "mcpGatewayPort is now mcp.gateway.port") || !strings.Contains(out.String(), "mcp.gateway.port = 8123") {
		t.Fatalf("out = %q, stderr = %q, want the new name", out.String(), stderr.String())
	}
	if cfg, err := config.Load(path, nil); err != nil || cfg.MCPGatewayPort != 8123 {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
	if _, err := runAirlock(t, "config", "set", "disabledModules", "k8s"); err == nil || !strings.Contains(err.Error(), "modules.<name>.enabled") {
		t.Fatalf("err = %v, want disabledModules refused with the replacing key", err)
	}
}

func TestRootStillTakesResourceArguments(t *testing.T) {
	cmd := NewRootCmd()
	found, args, err := cmd.Find([]string{"rds", "archive", "5433"})
	if err != nil || found != cmd || strings.Join(args, " ") != "rds archive 5433" {
		t.Fatalf("Find = %s %v %v, want the root command with its arguments", found.Name(), args, err)
	}
	found, _, err = cmd.Find([]string{"config", "show"})
	if err != nil || found.Name() != "show" {
		t.Fatalf("Find(config show) = %s, %v", found.Name(), err)
	}
}

func TestConfigRejectsAnUnknownSubcommand(t *testing.T) {
	testConfigFile(t)
	if _, err := runAirlock(t, "config", "typo"); err == nil || !strings.Contains(err.Error(), `unknown command "typo"`) {
		t.Fatalf("err = %v, want an unknown command error", err)
	}
}

func TestConfigShowSaysTheIgnoredProductionProfilesAndKeepsThemOutOfJSON(t *testing.T) {
	path := testConfigFile(t)
	writeTestFile(t, path, `{"environments": "prod!=*prod*", "productionProfiles": "*old*"}`)
	out, err := runAirlock(t, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "productionProfiles") {
			line = l
		}
	}
	if !strings.Contains(line, "*old* (ignored: environments is set)") || !strings.Contains(line, "deprecated") {
		t.Fatalf("productionProfiles line = %q, want it flagged as ignored", line)
	}
	out, err = runAirlock(t, "config", "show", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err = json.Unmarshal([]byte(out), &values); err != nil {
		t.Fatal(err)
	}
	if _, ok := values["productionProfiles"]; ok || len(values) != len(config.Keys) {
		t.Fatalf("values = %v, want the effective keys only", values)
	}
}

// useOrgEnv makes the commands of the test resolve the organization with env.
func useOrgEnv(t *testing.T, env orgEnv) {
	t.Helper()
	previous := orgEnvironment
	orgEnvironment = func() orgEnv { return env }
	t.Cleanup(func() { orgEnvironment = previous })
}

// settingLine returns the line of config show for key.
func settingLine(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, key+" ") {
			return line
		}
	}
	return ""
}

func TestConfigShowOrganizationSourceAndLock(t *testing.T) {
	path := testConfigFile(t)
	writeTestFile(t, path, `{"aws": {"defaultRegion": "us-east-1", "jumphostTag": "Env=local"}}`)
	useManagedFile(t, `{"settings":{"aws":{"jumphostTag":"Role=bastion","defaultRegion":"eu-west-3"},"mode":"cli"},"locked":["aws.jumphostTag"]}`)

	out, err := runAirlock(t, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Organization config: "+orgEnvironment().managedPath+"\n") {
		t.Fatalf("no organization header:\n%s", out)
	}
	for key, want := range map[string]string{
		"aws.jumphostTag":   "Role=bastion organization (locked)",
		"aws.defaultRegion": "us-east-1 file (overrides the organization value)",
		"mode":              "cli organization",
	} {
		if got := strings.Join(strings.Fields(settingLine(out, key))[1:], " "); got != want {
			t.Fatalf("%s line = %q, want %q:\n%s", key, got, want, out)
		}
	}

	out, err = runAirlock(t, "config", "show", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err = json.Unmarshal([]byte(out), &values); err != nil || values["aws.jumphostTag"] != "Role=bastion" {
		t.Fatalf("json = %v, %v:\n%s", values, err, out)
	}
}

func TestConfigShowNamesTheLastRead(t *testing.T) {
	path := testConfigFile(t)
	env := testOrgEnv(t, nil)
	useOrgEnv(t, env)
	source := "https://example.invalid/org.json"
	if err := orgconfig.SaveCache(env.cachePath, orgconfig.Cache{Source: source, FetchedAt: testNow, Body: []byte(testOrgBody)}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, `{"organizationConfig": "`+source+`"}`)

	out, err := runAirlock(t, "config", "show")
	want := "Organization config: " + source + " (last read " + testNow.Local().Format(time.DateTime) + ")\n"
	if err != nil || !strings.Contains(out, want) {
		t.Fatalf("out = %q, %v, want %q", out, err, want)
	}
}

func TestConfigSetOrganizationConfigRefusedWhenManaged(t *testing.T) {
	path := testConfigFile(t)
	useOrgEnv(t, testOrgEnv(t, nil))
	orgFile := writeTempFile(t, "org.json", testOrgBody)
	if _, err := runAirlock(t, "config", "set", "organizationConfig", orgFile); err != nil {
		t.Fatalf("no managed file: %v", err)
	}

	useManagedFile(t, `{"organizationConfig": "`+orgFile+`"}`)
	managed := orgEnvironment().managedPath
	for _, args := range [][]string{{"config", "set", "organizationConfig", orgFile}, {"config", "unset", "organizationConfig"}} {
		if _, err := runAirlock(t, args...); err == nil || !strings.Contains(err.Error(), managed) {
			t.Fatalf("%v: err = %v, want the managed file named", args, err)
		}
	}
	if cfg, err := config.Load(path, nil); err != nil || cfg.OrganizationConfig != orgFile {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}

func TestConfigOrganizationTokenStoresInKeychain(t *testing.T) {
	testConfigFile(t)
	memory := keychain.NewMemoryStore()
	previous := newKeychain
	newKeychain = func() keychain.Store { return memory }
	t.Cleanup(func() { newKeychain = previous })

	cmd := NewRootCmd()
	var out, stderr strings.Builder
	cmd.SetIn(strings.NewReader("ghp_secret123\n"))
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"config", "organization-token"})
	err := cmd.Execute()
	if err != nil || strings.Contains(out.String()+stderr.String(), "ghp_secret123") {
		t.Fatalf("out = %q, stderr = %q, %v", out.String(), stderr.String(), err)
	}
	if token, getErr := memory.Get(organizationTokenAccount); getErr != nil || token != "ghp_secret123" {
		t.Fatalf("token = %q, %v", token, getErr)
	}

	if _, err = runAirlockWithInput(t, "\n", "config", "organization-token"); err == nil {
		t.Fatal("an empty token must be refused")
	}
	if _, err = runAirlock(t, "config", "organization-token", "ghp_inline"); err == nil {
		t.Fatal("the token must never be an argument")
	}

	if _, err = runAirlock(t, "config", "organization-token", "--delete"); err != nil {
		t.Fatal(err)
	}
	if _, getErr := memory.Get(organizationTokenAccount); !errors.Is(getErr, keychain.ErrNotFound) {
		t.Fatalf("after --delete: %v", getErr)
	}
	if _, err = runAirlock(t, "config", "organization-token", "--delete"); err != nil {
		t.Fatalf("deleting a missing token: %v", err)
	}
}

func TestConfigInitLeavesOutTheOrganizationKeys(t *testing.T) {
	path := testConfigFile(t)
	useManagedFile(t, `{"settings":{"mode":"daemon"}}`)
	if _, err := runAirlock(t, "config", "init"); err != nil {
		t.Fatal(err)
	}
	if cfg, err := config.Load(path, testOrganization(t, `{"settings":{"mode":"daemon"}}`)); err != nil || cfg.Mode != config.ModeDaemon {
		t.Fatalf("cfg = %+v, %v, want the organization mode", cfg, err)
	}
}
