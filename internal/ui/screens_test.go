package ui

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

var updateScreens = flag.Bool("update-screens", false, "rewrite testdata/screens from the current rendering")

// volatileText matches what changes between runs: durations ("3m12s", "59s") and clock times.
var volatileText = regexp.MustCompile(`(\d+(ms|[smhd]))+|\d{2}:\d{2}(:\d{2})?`)

// spaceRun is the padding that shifts when a masked time changes length ("59s", "1m0s").
var spaceRun = regexp.MustCompile(` {2,}`)

// screenText is a rendered screen as the snapshots keep it: no ANSI, no trailing spaces, no times.
// The borders stay, so a panel one column narrower changes the snapshot; only on lines holding a
// time is the padding squeezed, since its width follows the time's.
func screenText(rendered string) string {
	lines := strings.Split(ansi.Strip(rendered), "\n")
	for idx, line := range lines {
		line = strings.TrimRight(line, " ")
		if volatileText.MatchString(line) {
			line = spaceRun.ReplaceAllString(volatileText.ReplaceAllString(line, "#"), "  ")
		}
		lines[idx] = line
	}
	return strings.Join(lines, "\n")
}

// checkScreen compares rendered with testdata/screens/<name>.txt (-update-screens rewrites it).
func checkScreen(t *testing.T, name, rendered string) {
	t.Helper()
	path := filepath.Join("testdata", "screens", name+".txt")
	got := screenText(rendered)
	if *updateScreens {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("%v (go test ./internal/ui -run TestScreens -update-screens writes it)", err)
	}
	if got != string(want) {
		t.Errorf("screen %s changed:\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

// TestScreens pins every screen with a bordered panel, at the sizes the tests use and at 80x24.
func TestScreens(t *testing.T) {
	pinScreens(t, "")
}

// TestScreensFrench pins the same screens in French: longer text must still fit the panels.
func TestScreensFrench(t *testing.T) {
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	pinScreens(t, ".fr")
}

// TestFrenchWizardHasNoEnglishCard catches text frozen in package-level vars before the language
// is set: the resource type screen must differ between French and English.
func TestFrenchWizardHasNoEnglishCard(t *testing.T) {
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	w := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{AwsProfile: "dev"})
	w, _ = wizardUpdate(t, w, tea.WindowSizeMsg{Width: 120, Height: 40})
	french := screenText(pastStart(t, w).render())
	i18n.SetLanguage("en")
	english := screenText(pastStart(t, w).render())
	if french == english {
		t.Fatalf("the resource type screen is the same in French and English:\n%s", french)
	}
}

// pinScreens checks every screen against testdata/screens/<name><suffix>.txt.
func pinScreens(t *testing.T, suffix string) {
	t.Helper()
	resize := func(t *testing.T, m ssmModel, width, height int) ssmModel {
		t.Helper()
		m, _ = update(t, m, tea.WindowSizeMsg{Width: width, Height: height})
		return m
	}
	t.Run("dashboard", func(t *testing.T) {
		m, _ := newSessionsModel(t, "orders-db", "archive-db")
		checkScreen(t, "dashboard-120x40"+suffix, m.render())
		checkScreen(t, "dashboard-80x24"+suffix, resize(t, m, 80, 24).render())
	})
	t.Run("dashboard-empty", func(t *testing.T) {
		m := newServiceModel(t, newFakeService())
		m.mode = ssmModeSessions
		checkScreen(t, "dashboard-empty"+suffix, m.render())
	})
	t.Run("help", func(t *testing.T) {
		m, _ := newSessionsModel(t, "orders-db")
		m, _ = update(t, m, key("?"))
		checkScreen(t, "help"+suffix, m.render())
	})
	t.Run("confirm-quit", func(t *testing.T) {
		m := newDaemonView(t, newFakeService(testSession(1, "archive-db")))
		m, _ = update(t, m, key("Q"))
		checkScreen(t, "confirm-quit"+suffix, m.render())
	})
	t.Run("approval", func(t *testing.T) {
		m := newDaemonView(t, newFakeService(testSession(1, "archive-db")))
		m, _ = update(t, m, approvalEvent())
		checkScreen(t, "approval"+suffix, m.render())
	})
	t.Run("copy", func(t *testing.T) {
		m, _, _ := newCopyTestModel(t, new(copyTestSession()))
		m, _ = update(t, m, key("c"))
		checkScreen(t, "copy"+suffix, m.render())
	})
	t.Run("password", func(t *testing.T) {
		m, _, _ := newPasswordTestModel(t, pickedSecret, nil, archiveSecretAuth)
		checkScreen(t, "password"+suffix, revealed(t, m).render())
	})
	t.Run("sign-in", func(t *testing.T) {
		checkScreen(t, "sign-in"+suffix, choosingModel(t, newFakeService()).render())
	})
	t.Run("wizard", func(t *testing.T) {
		w := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{AwsProfile: "dev"})
		w, _ = wizardUpdate(t, w, tea.WindowSizeMsg{Width: 120, Height: 40})
		checkScreen(t, "wizard-start"+suffix, w.render())
		checkScreen(t, "wizard-resource-types"+suffix, pastStart(t, w).render())
		review := wizardAtReviewOfType(t, jumphost.Options{}, "rds", jumphost.ResourceSummary{Name: "orders-db", Type: "rds"})
		checkScreen(t, "wizard-review"+suffix, review.render())
	})
	t.Run("direct", func(t *testing.T) {
		checkScreen(t, "direct-form"+suffix, directFormWizard(t, fakeSessionRunner{}, &fakeDirectStore{}, "").render())
		review := directReview(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()}, "legacy-billing")
		checkScreen(t, "direct-review"+suffix, review.render())
	})
	t.Run("settings", func(t *testing.T) {
		m := pressWizard(t, settingsWizard(t, newFakeSettingsStore()), key(","))
		m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
		checkScreen(t, "settings-80x24"+suffix, m.render())
		// Up from the first key wraps to the last one, which the organization locks.
		checkScreen(t, "settings-locked-80x24"+suffix, pressWizard(t, m, key("k")).render())
	})
	t.Run("direct-80x24", func(t *testing.T) {
		m := directFormWizard(t, fakeSessionRunner{}, &fakeDirectStore{}, "")
		m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
		checkScreen(t, "direct-form-80x24"+suffix, m.render())
	})
	t.Run("pickers", func(t *testing.T) {
		checkScreen(t, "region-picker"+suffix, newRegionPicker("eu-west-3").View(NewStyles(true), 100, 30))
		checkScreen(t, "secret-picker"+suffix, newSecretPicker("eu-west-3").View(NewStyles(true), 100, 30))
	})
}

// View hands Bubble Tea the rendered screen on the alternate screen, like tea.WithAltScreen did.
func TestViewUsesTheAltScreen(t *testing.T) {
	m, _ := newSessionsModel(t, "orders-db")
	v := m.View()
	if !v.AltScreen || v.Content != m.render() {
		t.Fatalf("AltScreen = %v, content matches render: %v", v.AltScreen, v.Content == m.render())
	}
}
