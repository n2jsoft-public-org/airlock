package config

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

const lockedOrg = `{"settings":{"agent":{"writes":false},"aws":{"defaultRegion":"eu-west-1","jumphostTag":"Role=org"},"mode":"daemon"},
	"locked":["agent.writes"]}`

func readBack(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a temporary test file.
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSetLockedKeyRefused(t *testing.T) {
	org, _ := mustOrg(t, lockedOrg)
	path := writeConfig(t, `{"mode":"cli"}`)
	err := Set(path, org, "agent.writes", "true")
	if err == nil || !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "agent.writes") || !strings.Contains(err.Error(), "src") {
		t.Fatalf("err = %v; want ErrLocked naming the key and source", err)
	}
	if got := readBack(t, path); got != `{"mode":"cli"}` {
		t.Fatalf("file changed: %s", got)
	}
	if err = Set(path, org, "mode", "daemon"); err != nil {
		t.Fatalf("unlocked key refused: %v", err)
	}
}

func TestSetLockedFlatKeyRefused(t *testing.T) {
	org, _ := mustOrg(t, lockedOrg)
	if err := Set(writeConfig(t, `{}`), org, "agentWrites", "true"); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v; want ErrLocked", err)
	}
}

func TestUnsetLockedKeyRefused(t *testing.T) {
	org, _ := mustOrg(t, lockedOrg)
	path := writeConfig(t, `{"agent":{"writes":true}}`)
	if _, err := Unset(path, org, "agent.writes"); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v; want ErrLocked", err)
	}
}

func TestPlanImportMarksLockedKeys(t *testing.T) {
	org, _ := mustOrg(t, lockedOrg)
	path := writeConfig(t, `{}`)
	shared := encodeShare([]byte(`{"agent":{"writes":true},"mcp":{"serverPrefix":"mine"}}`))
	plan, err := PlanImport(path, org, shared, false)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]ChangeKind{}
	for _, c := range plan.Changes {
		kinds[c.Name] = c.Kind
	}
	if kinds["agent.writes"] != ChangeLocked || kinds["mcp.serverPrefix"] != ChangeAdded {
		t.Fatalf("changes = %v", kinds)
	}
	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, path); !strings.Contains(got, "serverPrefix") || strings.Contains(got, "writes") {
		t.Fatalf("file = %s", got)
	}
}

func TestShadowedKeysEqualAndDiffering(t *testing.T) {
	org, _ := mustOrg(t, lockedOrg)
	path := writeConfig(t, `{"agent":{"writes":true},"aws":{"defaultRegion":"eu-west-1","jumphostTag":"Role=mine"},"mcp":{"serverPrefix":"x"}}`)
	equal, differing, err := ShadowedKeys(path, org)
	if err != nil {
		t.Fatal(err)
	}
	// agent.writes is locked, mcp.serverPrefix is not set by the organization: in neither list.
	if !slices.Equal(equal, []string{"aws.defaultRegion"}) || !slices.Equal(differing, []string{"aws.jumphostTag"}) {
		t.Fatalf("equal = %v, differing = %v", equal, differing)
	}
}

func TestDropKeysRemovesOnlyGivenKeys(t *testing.T) {
	path := writeConfig(t, `{"mode":"daemon","aws":{"defaultRegion":"eu-west-1"},"agentWrites":true}`)
	if err := DropKeys(path, []string{"aws.defaultRegion", "agentWrites"}); err != nil {
		t.Fatal(err)
	}
	f, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !f.sets("mode") || f.sets("aws.defaultRegion") || f.sets("agent.writes") {
		t.Fatalf("file = %s", readBack(t, path))
	}
	if err = DropKeys(path, []string{"nope"}); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestDiffOrganizations(t *testing.T) {
	oldOrg, _ := mustOrg(t, `{"settings":{"mode":"daemon","aws":{"defaultRegion":"eu-west-1"},"mcp":{"serverPrefix":"a"}}}`)
	newOrg, _ := mustOrg(t, `{"settings":{"mode":"daemon","aws":{"defaultRegion":"eu-west-3"},"agent":{"writes":false}},
		"locked":["mode","agent.writes"]}`)
	got := map[string]Change{}
	var order []string
	for _, c := range DiffOrganizations(oldOrg, newOrg) {
		got[c.Name] = c
		order = append(order, c.Name)
	}
	if !slices.Equal(order, []string{"mode", "aws.defaultRegion", "mcp.serverPrefix", "agent.writes"}) {
		t.Fatalf("order = %v", order)
	}
	if c := got["aws.defaultRegion"]; c.Kind != ChangeUpdated || c.Old != "eu-west-1" || c.New != "eu-west-3" || c.Locked {
		t.Fatalf("region = %+v", c)
	}
	if c := got["mode"]; c.Kind != ChangeUpdated || !c.Locked || c.WasLocked || c.Old != c.New {
		t.Fatalf("mode = %+v", c)
	}
	if c := got["mcp.serverPrefix"]; c.Kind != ChangeRemoved || c.Old != "a" || c.New != "" {
		t.Fatalf("prefix = %+v", c)
	}
	if c := got["agent.writes"]; c.Kind != ChangeAdded || !c.Locked || c.New != "false" {
		t.Fatalf("writes = %+v", c)
	}
	if c := DiffOrganizations(newOrg, oldOrg)[0]; c.Name != "mode" || c.Locked || !c.WasLocked {
		t.Fatalf("unlocked mode = %+v", c)
	}
	if len(DiffOrganizations(nil, nil)) != 0 || len(DiffOrganizations(newOrg, newOrg)) != 0 {
		t.Fatal("identical organizations must not differ")
	}
	if len(DiffOrganizations(nil, newOrg)) != 3 {
		t.Fatal("nil old = no organization: every key it sets is added")
	}
}
