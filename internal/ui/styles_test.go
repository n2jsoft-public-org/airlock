package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
	"github.com/n2jsoft/airlock/internal/jumphost"
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
	m.wizard = newStyledWizardModel(t.Context(), fakeSessionRunner{}, jumphost.Options{AwsProfile: "dev"}, m.styles)
	if m.wizard.styles.Dark {
		t.Fatal("the wizard did not inherit the dashboard palette")
	}
}
