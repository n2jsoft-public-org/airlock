package sessions

import (
	"encoding/json"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/environment"
)

func TestEnvironmentOf(t *testing.T) {
	set, err := environment.Parse("prod!=prod*; dev=dev*")
	if err != nil {
		t.Fatal(err)
	}
	if env := EnvironmentOf(set, &Session{Profile: "prod-eu"}); !env.Production {
		t.Fatalf("prod-eu = %+v", env)
	}
	if env := EnvironmentOf(set, &Session{ResourceType: DirectResourceType, Production: true}); !env.Production {
		t.Fatalf("a production direct database = %+v", env)
	}
	if env := EnvironmentOf(set, &Session{Profile: "dev-eu"}); env.Name != "dev" || env.Production {
		t.Fatalf("dev-eu = %+v", env)
	}
}

func TestEnvironmentOfALegacyProductionSession(t *testing.T) {
	set, err := environment.Parse("live!=*-live; dev=dev*")
	if err != nil {
		t.Fatal(err)
	}
	var s Session
	if err := json.Unmarshal([]byte(`{"resourceType": "direct", "target": "billing", "production": true}`), &s); err != nil {
		t.Fatal(err)
	}
	if env := EnvironmentOf(set, &s); !env.Production || env.Name != "live" {
		t.Fatalf("env = %+v, want an older daemon's production session shown as production", env)
	}
	if env := EnvironmentOf(set, &Session{ResourceType: DirectResourceType, Environment: "dev", Profile: "prod-eu"}); env.Name != "dev" {
		t.Fatalf("env = %+v, want the direct database's own environment", env)
	}
	if env := ApprovalEnvironmentOf(set, &Approval{Environment: "prod"}); !env.Production {
		t.Fatalf("approval env = %+v", env)
	}
}

func TestProductionFlagWinsOverTheNameTheSetLacks(t *testing.T) {
	// The gateway's own set names its production "live"; this dashboard's set does not.
	set, err := environment.Parse("prod!=*prod*; dev=dev*")
	if err != nil {
		t.Fatal(err)
	}
	if env := ApprovalEnvironmentOf(set, &Approval{Environment: "live", Production: true}); !env.Production || env.Name != "prod" {
		t.Fatalf("approval env = %+v, want production", env)
	}
	if env := EnvironmentOf(set, &Session{ResourceType: DirectResourceType, Environment: "live", Production: true}); !env.Production || env.Name != "prod" {
		t.Fatalf("session env = %+v, want production", env)
	}
	own, err := environment.Parse("live!=*-live")
	if err != nil {
		t.Fatal(err)
	}
	if env := ApprovalEnvironmentOf(own, &Approval{Environment: "live", Production: true}); !env.Production || env.Name != "live" {
		t.Fatalf("approval env = %+v, want the production named live", env)
	}
}
