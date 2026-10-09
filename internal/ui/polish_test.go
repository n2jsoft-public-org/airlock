package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

func TestPaletteAdaptsToLightTerminals(t *testing.T) {
	dark, light := NewStyles(true), NewStyles(false)
	foregrounds := map[string][2]lipgloss.Style{
		"Text": {dark.Text, light.Text}, "Muted": {dark.Muted, light.Muted}, "Faint": {dark.Faint, light.Faint},
		"Title": {dark.Title, light.Title}, "Subtitle": {dark.Subtitle, light.Subtitle}, "Success": {dark.Success, light.Success},
		"Warning": {dark.Warning, light.Warning}, "Danger": {dark.Danger, light.Danger}, "Rule": {dark.Rule, light.Rule},
	}
	for name, pair := range foregrounds {
		if d, l := pair[0].GetForeground(), pair[1].GetForeground(); d == nil || l == nil || d == l {
			t.Errorf("%s foreground = %v (dark) and %v (light), want a distinct color for light terminals", name, d, l)
		}
	}
	backgrounds := map[string][2]lipgloss.Style{
		"SelectedCard": {dark.SelectedCard, light.SelectedCard}, "KeyCap": {dark.KeyCap, light.KeyCap}, "Button": {dark.Button, light.Button},
	}
	for name, pair := range backgrounds {
		if d, l := pair[0].GetBackground(), pair[1].GetBackground(); d == nil || l == nil || d == l {
			t.Errorf("%s background = %v (dark) and %v (light), want a distinct color for light terminals", name, d, l)
		}
	}
}

func TestCountsArePluralized(t *testing.T) {
	m, _ := newSessionsModel(t, "db")
	m.sessions[0].Connections = 1
	if view := plainView(m); !strings.Contains(view, "1 connection") || strings.Contains(view, "connection(s)") {
		t.Fatalf("details do not say 1 connection:\n%s", view)
	}
	m.sessions[0].Connections = 2
	if view := plainView(m); !strings.Contains(view, "2 connections") {
		t.Fatalf("details do not say 2 connections:\n%s", view)
	}

	m, _ = update(t, m, key("q"))
	if view := plainView(m); !strings.Contains(view, "stops 1 active session and") {
		t.Fatalf("the quit dialog does not say 1 active session:\n%s", view)
	}
}

func TestHelpFitsAShortTerminal(t *testing.T) {
	m, _ := newSessionsModel(t, "db")
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(t, m, key("?"))
	if view := plainView(m); !helpComplete(view) {
		t.Fatalf("the help is cut on a 24-line terminal:\n%s", view)
	}

	w := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	w, _ = wizardUpdate(t, w, tea.WindowSizeMsg{Width: 80, Height: 24})
	w, _ = wizardUpdate(t, w, key("?"))
	if view := ansi.Strip(w.render()); !helpComplete(view) {
		t.Fatalf("the wizard help is cut on a 24-line terminal:\n%s", view)
	}
}

// helpComplete reports that view shows the help down to its closing hint and bottom border.
func helpComplete(view string) bool {
	hint := strings.Index(view, "any key closes this help")
	return hint >= 0 && strings.LastIndex(view, "╰") > hint
}

// TestLabelsFitTheirColumn keeps every review and form label one space short of its column, in
// every language, while English keeps its 9 and 13 columns.
func TestLabelsFitTheirColumn(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("en") })
	for _, lang := range i18n.Supported {
		i18n.SetLanguage(lang)
		review := NewStyles(true).FieldLabel.GetWidth()
		form := formLabelWidth()
		if lang == "en" && (review != 9 || form != 13) {
			t.Fatalf("English label columns = %d and %d, want 9 and 13", review, form)
		}
		for _, key := range reviewLabelKeys {
			if w := lipgloss.Width(i18n.T(key)); w >= review {
				t.Errorf("%s: %q (%d) does not fit the review column (%d)", lang, i18n.T(key), w, review)
			}
		}
		for _, key := range formLabelKeys {
			if w := lipgloss.Width(i18n.T(key)); w >= form {
				t.Errorf("%s: %q (%d) does not fit the form column (%d)", lang, i18n.T(key), w, form)
			}
		}
	}
}

func TestCountsArePluralizedInFrench(t *testing.T) {
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	m, _ := newSessionsModel(t, "db")
	m.sessions[0].Connections = 1
	if view := plainView(m); !strings.Contains(view, "1 connexion") || strings.Contains(view, "1 connexions") {
		t.Fatalf("details do not say 1 connexion:\n%s", view)
	}
	m.sessions[0].Connections = 2
	if view := plainView(m); !strings.Contains(view, "2 connexions") {
		t.Fatalf("details do not say 2 connexions:\n%s", view)
	}
}

func TestDashboardShowsTheLocalizedSessionError(t *testing.T) {
	m, _ := newSessionsModel(t, "db")
	m.sessions[0].Err, m.sessions[0].LocalErr = "no password given", "aucun mot de passe donné"
	if view := plainView(m); !strings.Contains(view, "aucun mot de passe donné") || strings.Contains(view, "no password given") {
		t.Fatalf("the Error row does not show the localized text:\n%s", view)
	}
}

func TestSignInDialogShowsLocalizedCandidateErrors(t *testing.T) {
	service := newFakeService()
	s := testSession(1, "archive-db")
	s.HasSignIn = true
	candidate := jumphost.SignInCandidate{Label: "a", User: "u1"}
	key := sessions.CandidateKey(&candidate)
	s.SignIn = sessions.SignIn{
		State: sessions.SignInChoosing, Candidates: []jumphost.SignInCandidate{candidate},
		Failed: map[string]string{key: "password authentication failed"}, LocalFailed: map[string]string{key: "authentification refusée"},
	}
	service.snapshot = sessions.Snapshot{Sessions: []sessions.Session{s}}
	m := newDaemonView(t, service)
	m.mode = ssmModeSignIn
	if view := plainView(m); !strings.Contains(view, "authentification refusée") || strings.Contains(view, "password authentication failed") {
		t.Fatalf("the candidate error is not the localized one:\n%s", view)
	}
}
