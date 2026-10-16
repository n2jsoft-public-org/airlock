package config

import (
	"strings"
	"testing"
)

func mustOrg(t *testing.T, data string) (org *Organization, warnings []string) {
	t.Helper()
	org, warnings, err := ParseOrganization("src", []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return org, warnings
}

func TestParseOrganizationLocksDottedAndFlatNames(t *testing.T) {
	org, _ := mustOrg(t, `{"settings":{"agent":{"writes":false,"autoConnect":"none"}},"locked":["agent.writes","agentAutoConnect"]}`)
	if !org.Locked("agent.writes") || !org.Locked("agent.autoConnect") || !org.Locked("agentWrites") || org.Locked("mode") {
		t.Fatalf("locked = %v", org.locked)
	}
	if !org.Sets("agentWrites") || org.Sets("mode") {
		t.Fatal("Sets wrong")
	}
	var none *Organization
	if none.Locked("mode") || none.Sets("mode") {
		t.Fatal("nil organization must lock and set nothing")
	}
}

func TestParseOrganizationStarLocksEverySetKey(t *testing.T) {
	org, _ := mustOrg(t, `{"settings":{"mode":"daemon","agent":{"writes":false}},"locked":"*"}`)
	if !org.Locked("mode") || !org.Locked("agent.writes") || org.Locked("aws.defaultRegion") {
		t.Fatalf("locked = %v", org.locked)
	}
}

func TestParseOrganizationLockedErrors(t *testing.T) {
	for name, data := range map[string]string{
		"no value":         `{"settings":{"agent":{"writes":false}},"locked":["mode"]}`,
		"unknown":          `{"settings":{},"locked":["nope"]}`,
		"disabledModules":  `{"settings":{"disabledModules":"s3"},"locked":["disabledModules"]}`,
		"neither list/*":   `{"settings":{},"locked":3}`,
		"malformed value":  `{"settings":{"agent":{"writes":"maybe"}}}`,
		"malformed object": `[`,
	} {
		if _, _, err := ParseOrganization("src", []byte(data)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestParseOrganizationUnknownKeyWarns(t *testing.T) {
	org, warnings := mustOrg(t, `{"settings":{"future":{"x":1},"mode":"daemon"}}`)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "future.x") || !org.Sets("mode") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestParseOrganizationUnknownTopLevelKeyWarns(t *testing.T) {
	for typo, data := range map[string]string{
		"lock":    `{"settings":{"mode":"daemon"},"lock":["mode"]}`,
		"setting": `{"setting":{"mode":"daemon"},"locked":[]}`,
	} {
		_, warnings := mustOrg(t, data)
		if len(warnings) != 1 || !strings.Contains(warnings[0], `"`+typo+`"`) {
			t.Errorf("%s: warnings = %q, want one naming it", typo, warnings)
		}
	}
}

func TestParseOrganizationPersonalKeysIgnored(t *testing.T) {
	org, warnings := mustOrg(t, `{"settings":{"language":"fr"}}`)
	if org.Sets("language") || len(warnings) != 1 {
		t.Fatalf("sets=%v warnings=%v", org.Sets("language"), warnings)
	}
}

func TestParseOrganizationMalformedValueNamesKey(t *testing.T) {
	_, _, err := ParseOrganization("src", []byte(`{"settings":{"agent":{"writes":"maybe"}}}`))
	if err == nil || !strings.Contains(err.Error(), "agent.writes") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseManaged(t *testing.T) {
	if m, err := ParseManaged([]byte(`{"organizationConfig":"org/repo/x.json"}`)); err != nil || m.Source != "org/repo/x.json" || m.Inline != nil {
		t.Fatalf("pointer: %+v %v", m, err)
	}
	inline := `{"settings":{"mode":"daemon"},"locked":"*"}`
	if m, err := ParseManaged([]byte(inline)); err != nil || m.Source != "" || string(m.Inline) != inline {
		t.Fatalf("inline: %+v %v", m, err)
	}
	if _, err := ParseManaged([]byte(`{"organizationConfig":"x","settings":{}}`)); err == nil {
		t.Fatal("both: want an error")
	}
	if _, err := ParseManaged([]byte(`{}`)); err == nil {
		t.Fatal("neither: want an error")
	}
}
