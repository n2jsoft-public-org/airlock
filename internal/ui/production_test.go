package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

func TestProductionMatcher(t *testing.T) {
	production := productionMatcher{"*PROD*", "*-live"}
	for profile, want := range map[string]bool{"acme-prod": true, "Team-Live": true, "prod": true, "dev": false, "live-dev": false, "": false} {
		if got := production.matches(profile); got != want {
			t.Errorf("matches(%q) = %v, want %v", profile, got, want)
		}
	}
	if (productionMatcher{}).matches("prod") {
		t.Fatal("without patterns, no profile is production")
	}
}

func TestProductionSessionIsFlagged(t *testing.T) {
	for _, width := range []int{140, 80} { // sidebar cards, then the compact pills
		m, _ := newSessionsModel(t, "dev-db", "prod-db")
		m.production = productionMatcher{"prod*"}
		m.sessions[1].Profile = "prod-eu"
		m = m.selectSession(0)
		m, _ = update(t, m, tea.WindowSizeMsg{Width: width, Height: 40})

		if got := strings.Count(plainView(m), "PROD"); got != 1 {
			t.Fatalf("width %d: %d PROD badges, want one for the production session:\n%s", width, got, plainView(m))
		}
	}
}

func TestDashboardPassesProductionProfilesToTheWizard(t *testing.T) {
	m, _ := newSessionsModel(t, "db")
	m.production = productionMatcher{"prod*"}
	m, _ = update(t, m, key("n"))
	if !m.wizard.production.matches("prod-eu") {
		t.Fatal("the wizard opened from the dashboard does not know the production profiles")
	}
}

func TestWizardFlagsProductionProfile(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{AwsProfile: "prod-eu"})
	if strings.Contains(ansi.Strip(m.render()), "PROD") {
		t.Fatal("PROD shown without any production pattern")
	}
	m.production = productionMatcher{"prod*"}
	if !strings.Contains(ansi.Strip(m.render()), "PROD") {
		t.Fatalf("the header does not flag the production profile:\n%s", ansi.Strip(m.render()))
	}
}

func TestWizardReviewFlagsProductionProfile(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true, Engine: "postgres"})
	m.production = productionMatcher{"dev"}
	if got := strings.Count(ansi.Strip(m.render()), "PROD"); got < 2 {
		t.Fatalf("%d PROD badges, want the header and the review's AWS row:\n%s", got, ansi.Strip(m.render()))
	}
}

func TestWizardProfileListFlagsProductionProfiles(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.production = productionMatcher{"prod"}
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pastStart(t, m)
	m, _ = wizardUpdate(t, m, key("enter")) // rds, then the profiles (dev, prod) load
	m, _ = wizardUpdate(t, m, jumphostProfilesLoadedMsg{profiles: []jumphost.AWSProfileSummary{{Name: "dev"}, {Name: "prod"}}})

	if got := strings.Count(ansi.Strip(m.render()), "PROD"); got != 1 {
		t.Fatalf("%d PROD badges, want one on the prod profile:\n%s", got, ansi.Strip(m.render()))
	}
}

func TestRecentConnectionsFlagProductionProfiles(t *testing.T) {
	p := newRecentPicker(NewStyles(true), recentFixture(), nil, productionMatcher{"dev"}, nil, time.Now())
	p.SetSize(100, 20)
	if got := strings.Count(ansi.Strip(p.View(NewStyles(true), 100)), "PROD"); got != 2 {
		t.Fatalf("%d PROD badges, want both dev connections flagged", got)
	}
}
