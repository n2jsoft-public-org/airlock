package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

const testNotice = "update available: v1.4.0 · run airlock update"

func TestWizardShowsTheUpdateNotice(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	before := ansi.Strip(m.render())
	if strings.Contains(before, "update available") {
		t.Fatalf("notice shown without a newer release:\n%s", before)
	}

	m.updateNotice = testNotice
	after := ansi.Strip(m.render())
	if !strings.Contains(after, testNotice) {
		t.Fatalf("the wizard does not show the notice:\n%s", after)
	}
	if strings.Count(after, "\n") != strings.Count(before, "\n") {
		t.Fatalf("the notice changed the wizard height: %d lines, was %d", strings.Count(after, "\n"), strings.Count(before, "\n"))
	}
}

func TestDashboardPassesTheUpdateNoticeToTheWizard(t *testing.T) {
	m, _ := newSessionsModel(t, "db")
	m.updateNotice = testNotice
	m, _ = update(t, m, key("n"))
	if m.wizard.updateNotice != testNotice {
		t.Fatalf("the wizard opened from the dashboard has notice %q", m.wizard.updateNotice)
	}
}

func TestWizardShowsOrganizationNotices(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	before := ansi.Strip(m.render())
	m.updateNotice = testNotice
	m.organizationNotice = []string{"the organization config org.json is not applied: boom", "The organization config org.json changed:", "  ~ mode   cli → daemon"}
	after := ansi.Strip(m.render())
	for _, want := range append([]string{testNotice}, m.organizationNotice...) {
		if !strings.Contains(after, want) {
			t.Fatalf("the wizard does not show %q:\n%s", want, after)
		}
	}
	if strings.Count(after, "\n") != strings.Count(before, "\n") {
		t.Fatalf("the notices changed the wizard height: %d lines, was %d", strings.Count(after, "\n"), strings.Count(before, "\n"))
	}
}

func TestDashboardPassesTheOrganizationNoticeToTheWizard(t *testing.T) {
	m, _ := newSessionsModel(t, "db")
	m.organizationNotice = []string{"the organization config org.json is not applied: boom"}
	m, _ = update(t, m, key("n"))
	if !slices.Equal(m.wizard.organizationNotice, m.organizationNotice) {
		t.Fatalf("the wizard opened from the dashboard has notice %q", m.wizard.organizationNotice)
	}
}

func TestWizardCutsLongOrganizationNotices(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	before := ansi.Strip(m.render())
	m.updateNotice = testNotice
	m.organizationNotice = []string{"org line 1", "org line 2", "org line 3", "org line 4", "org line 5", "org line 6", "org line 7", "org line 8"}
	after := ansi.Strip(m.render())
	for _, want := range []string{testNotice, "org line 4", "… 4 more lines, printed when airlock exits"} {
		if !strings.Contains(after, want) {
			t.Fatalf("the wizard does not show %q:\n%s", want, after)
		}
	}
	if strings.Contains(after, "org line 5") {
		t.Fatalf("the cut banner shows a hidden line:\n%s", after)
	}
	if strings.Count(after, "\n") != strings.Count(before, "\n") {
		t.Fatalf("the notices changed the wizard height: %d lines, was %d", strings.Count(after, "\n"), strings.Count(before, "\n"))
	}
}

func TestWizardNoticeKeepsToItsLine(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	before := ansi.Strip(m.render())
	m.organizationNotice = []string{"fetch failed:\nconnection reset", "second notice"}
	after := ansi.Strip(m.render())
	if !strings.Contains(after, "fetch failed: connection reset") {
		t.Fatalf("the multi-line notice is not flattened:\n%s", after)
	}
	if strings.Count(after, "\n") != strings.Count(before, "\n") {
		t.Fatalf("the notices changed the wizard height: %d lines, was %d", strings.Count(after, "\n"), strings.Count(before, "\n"))
	}
}

// TestDashboardOptionsCarryTheOrganizationNotice covers the daemon view too: it runs the dashboard
// on a copy of the options.
func TestDashboardOptionsCarryTheOrganizationNotice(t *testing.T) {
	notice := []string{"the organization config org.json is not applied: boom"}
	m := newDashboardModel(t.Context(), newFakeService(), fakeSessionRunner{}, 0, jumphost.Options{}, &DashboardOptions{OrganizationNotice: notice})
	if !slices.Equal(m.wizard.organizationNotice, notice) || !m.organizationNoticeShown {
		t.Fatalf("startup wizard notice %q, shown %v", m.wizard.organizationNotice, m.organizationNoticeShown)
	}
	long := slices.Repeat(notice, maxNoticeLines+1)
	if m = newDashboardModel(t.Context(), newFakeService(), fakeSessionRunner{}, 0, jumphost.Options{}, &DashboardOptions{OrganizationNotice: long}); m.organizationNoticeShown {
		t.Fatal("a cut banner counts as shown")
	}
	daemon := newDashboardModel(t.Context(), newFakeService(testSession(1, "db")), fakeSessionRunner{}, 0, jumphost.Options{}, &DashboardOptions{Detach: true, OrganizationNotice: notice})
	if daemon.mode == ssmModeWizard || daemon.organizationNoticeShown {
		t.Fatal("a daemon view opening on its sessions has not shown the notice")
	}
}
