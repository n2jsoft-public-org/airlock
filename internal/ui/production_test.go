package ui

import (
	"context"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/n2jsoft-public-org/airlock/internal/environment"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
)

func TestProductionSessionIsFlagged(t *testing.T) {
	for _, width := range []int{140, 80} { // sidebar cards, then the compact pills
		m, _ := newSessionsModel(t, "dev-db", "prod-db")
		m.environments = mustEnvironments(t, "prod!=prod*")
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
	m.environments = mustEnvironments(t, "prod!=prod*")
	m, _ = update(t, m, key("n"))
	if !m.wizard.environments.Match("prod-eu").Production {
		t.Fatal("the wizard opened from the dashboard does not know the production profiles")
	}
}

func TestWizardFlagsProductionProfile(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{AwsProfile: "prod-eu"})
	if strings.Contains(ansi.Strip(m.render()), "PROD") {
		t.Fatal("PROD shown without any production pattern")
	}
	m.environments = mustEnvironments(t, "prod!=prod*")
	if !strings.Contains(ansi.Strip(m.render()), "PROD") {
		t.Fatalf("the header does not flag the production profile:\n%s", ansi.Strip(m.render()))
	}
}

func TestWizardReviewFlagsProductionProfile(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true, Engine: "postgres"})
	m.environments = mustEnvironments(t, "prod!=dev")
	if got := strings.Count(ansi.Strip(m.render()), "PROD"); got < 2 {
		t.Fatalf("%d PROD badges, want the header and the review's AWS row:\n%s", got, ansi.Strip(m.render()))
	}
}

func TestWizardProfileListFlagsProductionProfiles(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.environments = mustEnvironments(t, "prod!=prod")
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pastStart(t, m)
	m, _ = wizardUpdate(t, m, key("enter")) // rds, then the profiles (dev, prod) load
	m, _ = wizardUpdate(t, m, jumphostProfilesLoadedMsg{profiles: []jumphost.AWSProfileSummary{{Name: "dev"}, {Name: "prod"}}})

	if got := strings.Count(ansi.Strip(m.render()), "PROD"); got != 1 {
		t.Fatalf("%d PROD badges, want one on the prod profile:\n%s", got, ansi.Strip(m.render()))
	}
}

func TestRecentConnectionsFlagProductionProfiles(t *testing.T) {
	p := newRecentPicker(NewStyles(true), recentFixture(), nil, mustEnvironments(t, "prod!=dev"), nil, time.Now())
	p.SetSize(100, 20)
	if got := strings.Count(ansi.Strip(p.View(NewStyles(true), 100)), "PROD"); got != 2 {
		t.Fatalf("%d PROD badges, want both dev connections flagged", got)
	}
}

func mustEnvironments(t *testing.T, s string) environment.Set {
	t.Helper()
	set, err := environment.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestStagingSessionIsBadgedWithoutTheProductionBorder(t *testing.T) {
	for _, width := range []int{140, 80} { // sidebar cards, then the compact pills
		m, _ := newSessionsModel(t, "stg-db")
		m.environments = mustEnvironments(t, "prod!=prod*; staging=stg*")
		m.sessions[0].Profile = "stg-eu"
		m = m.selectSession(0)
		m, _ = update(t, m, tea.WindowSizeMsg{Width: width, Height: 40})
		if view := plainView(m); !strings.Contains(view, "STAGING") || strings.Contains(view, "PROD") {
			t.Fatalf("width %d: want a STAGING badge and no PROD:\n%s", width, view)
		}
	}
}

// borderPrefix is the escape sequence that colors the top-left corner of a rounded border with c.
func borderPrefix(c color.Color) string {
	return strings.TrimSuffix(lipgloss.NewStyle().Foreground(c).Render("╭"), "╭\x1b[m")
}

func TestOnlyProductionSessionsGetTheWarningBorder(t *testing.T) {
	m, _ := newSessionsModel(t, "stg-db", "prod-db")
	m.environments = mustEnvironments(t, "prod!=prod*; staging=stg*")
	m.sessions[0].Profile = "stg-eu"
	m.sessions[1].Profile = "prod-eu"
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	warning := borderPrefix(m.styles.WarningColor) + "╭"
	if !strings.Contains(warning, "\x1b[") {
		t.Fatalf("no color sequence in %q", warning)
	}
	if details := m.selectSession(0).renderDetails(80); strings.Contains(details, warning) {
		t.Fatalf("the staging session's details use the production border:\n%s", details)
	}
	if details := m.selectSession(1).renderDetails(80); !strings.Contains(details, warning) {
		t.Fatalf("the production session's details lack the warning border:\n%s", details)
	}
}

func TestWizardProfileListBadgesEveryEnvironment(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.environments = mustEnvironments(t, "prod!=*prod*; dev=*dev*")
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pastStart(t, m)
	m, _ = wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, jumphostProfilesLoadedMsg{profiles: []jumphost.AWSProfileSummary{{Name: "team-prod"}, {Name: "team-dev"}, {Name: "sandbox"}}})
	view := ansi.Strip(m.render())
	if !strings.Contains(view, "PROD") || !strings.Contains(view, "DEV") {
		t.Fatalf("profile list = %s, want PROD and DEV badges", view)
	}
}
