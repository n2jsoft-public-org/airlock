package environment

import (
	"slices"
	"strings"
	"testing"
)

func mustParse(t *testing.T, s string) Set {
	t.Helper()
	set, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestDefaultIsAProductionEnvironmentNamedProd(t *testing.T) {
	for _, set := range []Set{Default(), {}, mustParse(t, ""), mustParse(t, "prod!=")} {
		if got := set.String(); got != "prod!=" {
			t.Fatalf("String = %q, want prod!=", got)
		}
		if p := set.Production(); p != (Env{Name: "prod", Production: true, Configured: true}) {
			t.Fatalf("Production = %+v", p)
		}
		if env := set.Match("team-prod"); !env.IsZero() {
			t.Fatalf("Match without patterns = %+v, want none", env)
		}
	}
}

func TestParseNormalizes(t *testing.T) {
	set := mustParse(t, "  Staging = *stg* , ,*STAGING* ; DEV=*dev*;  PROD ! = *prod*,*-live ;")
	if got := set.String(); got != "prod!=*prod*,*-live; staging=*stg*,*STAGING*; dev=*dev*" {
		t.Fatalf("String = %q", got)
	}
	if got := set.Names(); !slices.Equal(got, []string{"prod", "staging", "dev"}) {
		t.Fatalf("Names = %q", got)
	}
}

func TestParseWithoutProductionAddsProd(t *testing.T) {
	set := mustParse(t, "staging=*stg*")
	if got := set.String(); got != "prod!=; staging=*stg*" {
		t.Fatalf("String = %q", got)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"staging":                    "name=patterns",
		"=*stg*":                     "environment name",
		"9lives=*":                   "environment name",
		"a-very-long-name=*":         "environment name",
		"stg_1=*":                    "environment name",
		"dev=*dev*; DEV=*d*":         "twice",
		"prod!=*prod*; live!=*-live": "only one",
		"prod=*prod*":                "reserved",
		"live!=*-live; prod=*prod*":  "reserved",
		"dev=team-[":                 "invalid pattern",
	}
	for in, want := range cases {
		if _, err := Parse(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) error = %v, want one containing %q", in, err, want)
		}
	}
}

func TestMatchPrefersProduction(t *testing.T) {
	set := mustParse(t, "dev=*dev*; prod!=*prod*")
	if env := set.Match("team-prod-dev"); !env.Production || env.Name != "prod" {
		t.Fatalf("Match = %+v, want production first", env)
	}
}

func TestMatchFollowsConfigOrder(t *testing.T) {
	set := mustParse(t, "prod!=*prod*; staging=*stg*,*-qa; qa=*-qa; dev=*dev*")
	cases := map[string]Env{
		" Team-STG ": {Name: "staging", Index: 0, Configured: true}, //nolint:gocritic
		"shop-qa":    {Name: "staging", Index: 0, Configured: true},
		"dev-eu":     {Name: "dev", Index: 2, Configured: true},
		"sandbox":    {},
		"":           {},
	}
	for profile, want := range cases {
		if got := set.Match(profile); got != want {
			t.Errorf("Match(%q) = %+v, want %+v", profile, got, want)
		}
	}
}

func TestLookup(t *testing.T) {
	set := mustParse(t, "live!=*-live; staging=*stg*; qa=")
	cases := map[string]Env{
		"live":    {Name: "live", Production: true, Configured: true},
		"prod":    {Name: "live", Production: true, Configured: true},
		" PROD ":  {Name: "live", Production: true, Configured: true}, //nolint:gocritic
		"staging": {Name: "staging", Index: 0, Configured: true},
		"QA":      {Name: "qa", Index: 1, Configured: true},
		"sandbox": {Name: "sandbox"},
		"":        {},
	}
	for name, want := range cases {
		if got := set.Lookup(name); got != want {
			t.Errorf("Lookup(%q) = %+v, want %+v", name, got, want)
		}
	}
}

func TestFromProductionProfiles(t *testing.T) {
	set := FromProductionProfiles([]string{"*PROD*", "*-live"})
	if got := set.String(); got != "prod!=*PROD*,*-live" {
		t.Fatalf("String = %q", got)
	}
	if !set.Match("acme-prod").Production || !set.Match("Shop-Live").Production || !set.Match("dev").IsZero() {
		t.Fatal("the production patterns must keep matching as productionProfiles did")
	}
}

func TestValidName(t *testing.T) {
	for _, name := range []string{"dev", "pre-prod", "qa2", "abcdefghijkl"} {
		if err := ValidName(name); err != nil {
			t.Errorf("ValidName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", "Dev", "2qa", "abcdefghijklm", "qa_1", "qa 1"} {
		if err := ValidName(name); err == nil {
			t.Errorf("ValidName(%q) accepted an invalid name", name)
		}
	}
}

func TestProductionPatternsAndWithProductionPatterns(t *testing.T) {
	set, err := Parse("live!=; staging=*stg*")
	if err != nil {
		t.Fatal(err)
	}
	if got := set.ProductionPatterns(); len(got) != 0 {
		t.Fatalf("ProductionPatterns = %v, want none", got)
	}
	kept := set.WithProductionPatterns([]string{"*prod*", "*-live"})
	if got := kept.String(); got != "live!=*prod*,*-live; staging=*stg*" {
		t.Fatalf("WithProductionPatterns = %q", got)
	}
	if got := kept.ProductionPatterns(); len(got) != 2 || got[0] != "*prod*" {
		t.Fatalf("ProductionPatterns = %v", got)
	}
	if got := set.String(); got != "live!=; staging=*stg*" {
		t.Fatalf("the original set changed: %q", got)
	}
	if got := Default().ProductionPatterns(); len(got) != 0 {
		t.Fatalf("Default patterns = %v", got)
	}
}
