package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft-public-org/airlock/internal/environment"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
)

// Review Focus 4: a light background gets the darker palette, the default stays dark.
func TestNewStylesPicksThePaletteOfTheBackground(t *testing.T) {
	if got, want := NewStyles(false).AccentColor, lipgloss.Color("31"); got != want {
		t.Errorf("light accent = %v, want %v", got, want)
	}
	if got, want := NewStyles(true).AccentColor, lipgloss.Color("81"); got != want {
		t.Errorf("dark accent = %v, want %v", got, want)
	}
}

// The page dots are the list chrome a light terminal shows: list.New draws them dark.
func TestWizardListFollowsTheBackground(t *testing.T) {
	light, want := newWizardList(NewStyles(false), nil), list.DefaultStyles(false)
	if light.Paginator.ActiveDot != want.ActivePaginationDot.String() || light.Paginator.InactiveDot != want.InactivePaginationDot.String() {
		t.Errorf("page dots = %q / %q, want the light ones %q / %q", light.Paginator.ActiveDot, light.Paginator.InactiveDot,
			want.ActivePaginationDot.String(), want.InactivePaginationDot.String())
	}
}

func TestDashboardUsesTheDetectedBackground(t *testing.T) {
	m := newDashboardModel(t.Context(), newFakeService(), fakeSessionRunner{}, 0, jumphost.Options{}, &DashboardOptions{lightBackground: true})
	if m.styles.Dark {
		t.Fatal("a light terminal got the dark palette")
	}
	// Without a first session, the dashboard opens on the wizard it built itself.
	if m.mode != ssmModeWizard || m.wizard.styles.Dark {
		t.Fatal("the wizard did not inherit the dashboard palette")
	}
}

// A placeholder wider than its input ends with "…", as with Bubbles v1, instead of a cut word.
func TestInputViewShortensALongPlaceholder(t *testing.T) {
	in := textinput.New()
	in.SetStyles(textInputStyles())
	in.Prompt, in.Placeholder = "› ", "Search: paris, eu-west, tokyo"
	in.SetWidth(12)
	if got := strings.TrimRight(ansi.Strip(inputView(in)), " "); got != "› Search: p…" {
		t.Fatalf("view = %q, want %q", got, "› Search: p…")
	}
}

func TestEnvBadge(t *testing.T) {
	s := NewStyles(true)
	set, err := environment.Parse("live!=*-live; staging=*stg*; dev=*dev*")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.EnvBadge(environment.Env{}); got != "" {
		t.Fatalf("no environment = %q, want no badge", got)
	}
	live, staging, dev, unknown := s.EnvBadge(set.Production()), s.EnvBadge(set.Lookup("staging")), s.EnvBadge(set.Lookup("dev")), s.EnvBadge(set.Lookup("qa"))
	for badge, text := range map[string]string{live: "LIVE", staging: "STAGING", dev: "DEV", unknown: "QA"} {
		if !strings.Contains(ansi.Strip(badge), text) {
			t.Errorf("badge %q does not read %s", ansi.Strip(badge), text)
		}
	}
	if live != s.Badge.Background(colorProductionBadge).Render("LIVE") {
		t.Fatal("production keeps the PROD color whatever its name")
	}
	if staging == dev || staging == s.Badge.Background(colorUnknownEnvironment).Render("STAGING") {
		t.Fatal("configured environments take distinct palette colors, not the unknown gray")
	}
	if unknown != s.Badge.Background(colorUnknownEnvironment).Render("QA") {
		t.Fatal("an environment the setting lacks is gray")
	}
}

func TestEnvPillStyle(t *testing.T) {
	s := NewStyles(true)
	set := mustEnvironments(t, "live!=*-live; staging=*stg*")
	if got, want := s.EnvPillStyle(set.Production()).Render("LIVE"), s.Warning.Bold(true).Render("LIVE"); got != want {
		t.Fatalf("production pill = %q, want the Warning style %q", got, want)
	}
	if got, want := s.EnvPillStyle(set.Lookup("staging")).Render("STAGING"), s.Text.Bold(true).Foreground(envColor(set.Lookup("staging"))).Render("STAGING"); got != want {
		t.Fatalf("staging pill = %q, want its badge color %q", got, want)
	}
	if got, want := s.EnvPillStyle(set.Lookup("qa")).Render("QA"), s.Text.Bold(true).Foreground(colorUnknownEnvironment).Render("QA"); got != want {
		t.Fatalf("unknown pill = %q, want gray %q", got, want)
	}
}

func TestEnvironmentPaletteAvoidsWarningColors(t *testing.T) {
	for _, c := range environmentPalette {
		for _, reserved := range []string{"166", "214", "160", "203", "25", "97", "61", "240"} {
			if c == reserved {
				t.Fatalf("palette color %s is reserved (production, writes, cache, resource or agent badges, unknown)", c)
			}
		}
	}
}
