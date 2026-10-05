package sessions

import "testing"

func TestProductionProfilesMatches(t *testing.T) {
	p := ProductionProfiles{"*PROD*", "*-live"}
	//nolint:gocritic // whitespace in " prod " key is intentional to test trimming
	for profile, want := range map[string]bool{"team-prod": true, "Shop-Live": true, "dev": false, "": false, " prod ": true} {
		if got := p.Matches(profile); got != want {
			t.Errorf("Matches(%q) = %v, want %v", profile, got, want)
		}
	}
	if (ProductionProfiles{}).Matches("prod") {
		t.Fatal("no pattern flags nothing")
	}
}

func TestProductionProfilesIsProduction(t *testing.T) {
	p := ProductionProfiles{"prod*"}
	if !p.IsProduction(&Session{Profile: "prod-eu"}) || !p.IsProduction(&Session{ResourceType: DirectResourceType, Production: true}) {
		t.Fatal("a production profile or a production direct database is production")
	}
	if p.IsProduction(&Session{Profile: "dev"}) {
		t.Fatal("dev is not production")
	}
}
