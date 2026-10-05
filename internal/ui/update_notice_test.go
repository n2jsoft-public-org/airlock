package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

const testNotice = "update available: 20261005-3f9a1c2 · run airlock update"

func TestWizardShowsTheUpdateNotice(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	before := ansi.Strip(m.View())
	if strings.Contains(before, "update available") {
		t.Fatalf("notice shown without a newer release:\n%s", before)
	}

	m.updateNotice = testNotice
	after := ansi.Strip(m.View())
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
