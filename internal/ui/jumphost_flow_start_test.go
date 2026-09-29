package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// historyRunner remembers connections in memory and answers lookups.
type historyRunner struct {
	fakeSessionRunner
	lookupErr error
	saved     *[]jumphost.SavedConnection
	lookups   *[]jumphost.Options
}

func newHistoryRunner(saved ...jumphost.SavedConnection) historyRunner {
	return historyRunner{saved: &saved, lookups: &[]jumphost.Options{}}
}

func (r historyRunner) ListSavedConnections(context.Context, string, string) ([]jumphost.SavedConnection, error) {
	return append([]jumphost.SavedConnection(nil), *r.saved...), nil
}

func (r historyRunner) ForgetConnection(_ context.Context, ref jumphost.TargetRef) error {
	kept := (*r.saved)[:0]
	for i := range *r.saved {
		if (*r.saved)[i].Ref != ref {
			kept = append(kept, (*r.saved)[i])
		}
	}
	*r.saved = kept
	return nil
}

func (r historyRunner) FindResource(_ context.Context, options jumphost.Options) (jumphost.ResourceSummary, error) {
	*r.lookups = append(*r.lookups, options)
	if r.lookupErr != nil {
		return jumphost.ResourceSummary{}, r.lookupErr
	}
	return jumphost.ResourceSummary{Name: options.Name, Type: options.ResourceType, IAMAuth: true}, nil
}

var savedArchive = jumphost.SavedConnection{
	Ref:   jumphost.TargetRef{Account: "1", Region: "eu-west-1", Type: "rds", Name: "archive"},
	Prefs: jumphost.ConnectionPrefs{Profile: "acme-staging", DBUser: "app"},
}

// runChain runs cmd and the commands its messages lead to, depth levels deep: a lookup leads to
// the port loading, a forget to the history reload.
func runChain(t *testing.T, m jumphostWizardModel, cmd tea.Cmd, depth int) jumphostWizardModel {
	t.Helper()
	if cmd == nil || depth == 0 {
		return m
	}
	switch msg := runWithTimeout(cmd).(type) {
	case tea.BatchMsg:
		for _, inner := range msg {
			m = runChain(t, m, inner, depth)
		}
	case nil, loadingTickMsg:
		// Spinner ticks only animate.
	default:
		var next tea.Cmd
		m, next = wizardUpdate(t, m, msg)
		m = runChain(t, m, next, depth-1)
	}
	return m
}

// startWizard opens a wizard without --profile on its start screen.
func startWizard(t *testing.T, runner JumphostDiscovery) jumphostWizardModel {
	t.Helper()
	m := newJumphostWizardModel(context.Background(), runner, jumphost.Options{})
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = runChain(t, m, m.loadSavedCmd(), 1)
	return m
}

func TestWizardOpensOnRecentConnections(t *testing.T) {
	m := startWizard(t, newHistoryRunner(savedArchive))
	if m.step != jumphostStepStart || m.flow != flowStart || !m.hasHistory {
		t.Fatalf("step = %v flow = %v, want the start screen", m.step, m.flow)
	}
	view := m.View()
	for _, want := range []string{"Recent connections", "archive", "New connection", "Search a target"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
}

func TestWizardRecentConnectionOpensPrefilledReview(t *testing.T) {
	runner := newHistoryRunner(savedArchive)
	m := startWizard(t, runner)
	m, cmd := wizardUpdate(t, m, key("enter"))
	m = runChain(t, m, cmd, 3)

	if m.step != jumphostStepReview || m.flow != flowRecent {
		t.Fatalf("step = %v flow = %v, want the review step", m.step, m.flow)
	}
	lookup := (*runner.lookups)[0]
	if lookup.AwsProfile != "acme-staging" || lookup.AwsRegion != "eu-west-1" || lookup.ResourceType != "rds" || lookup.Name != "archive" {
		t.Fatalf("lookup = %+v", lookup)
	}
	if !m.targetIAMAuth {
		t.Fatal("the lookup's IAM auth flag must reach the review")
	}
	steps, _ := m.breadcrumb()
	if strings.Join(steps, ",") != "Connect" {
		t.Fatalf("breadcrumb = %v", steps)
	}
}

func TestWizardRecentConnectionWithMissingProfileAsksOne(t *testing.T) {
	missing := savedArchive
	missing.ProfileMissing = true
	runner := newHistoryRunner(missing)
	m := startWizard(t, runner)
	m, cmd := wizardUpdate(t, m, key("enter"))
	m = runChain(t, m, cmd, 3)
	if m.step != jumphostStepProfile || !m.recentNeedsProfile {
		t.Fatalf("step = %v, want the profile step", m.step)
	}
	m, _ = wizardUpdate(t, m, key("down")) // prod
	m, cmd = wizardUpdate(t, m, key("enter"))
	m = runChain(t, m, cmd, 3)
	if m.step != jumphostStepReview || (*runner.lookups)[0].AwsProfile != "prod" {
		t.Fatalf("step = %v lookups = %+v", m.step, *runner.lookups)
	}
}

func TestWizardRecentLookupErrorReturnsToStart(t *testing.T) {
	runner := newHistoryRunner(savedArchive)
	runner.lookupErr = errors.New("RDS target archive no longer exists in eu-west-1")
	m := startWizard(t, runner)
	m, cmd := wizardUpdate(t, m, key("enter"))
	m = runChain(t, m, cmd, 3)
	if m.step != jumphostStepStart || m.flow != flowStart || !strings.Contains(m.errorMsg, "no longer exists") {
		t.Fatalf("step = %v flow = %v error = %q", m.step, m.flow, m.errorMsg)
	}
	if len(*runner.saved) != 1 {
		t.Fatal("a failed lookup must keep the entry")
	}
}

func TestWizardBackToStartResetsOptions(t *testing.T) {
	m := startWizard(t, newHistoryRunner(savedArchive))
	m, cmd := wizardUpdate(t, m, key("enter"))
	m = runChain(t, m, cmd, 3)
	m, _ = wizardUpdate(t, m, key("esc"))
	if m.step != jumphostStepStart {
		t.Fatalf("step = %v, esc on the review of a recent connection goes to the start screen", m.step)
	}
	m, _ = wizardUpdate(t, m, key("n"))
	if m.step != jumphostStepType || m.options.AwsRegion != "" || m.options.Name != "" || m.options.ResourceType != "" || m.options.AwsProfile == "acme-staging" {
		t.Fatalf("step = %v options = %+v, want the base options", m.step, m.options)
	}
	m, _ = wizardUpdate(t, m, key("esc"))
	if m.step != jumphostStepStart {
		t.Fatalf("step = %v, esc on the Type step goes back to the start screen", m.step)
	}
}

func TestWizardStartSearchShortcut(t *testing.T) {
	m := startWizard(t, newHistoryRunner(savedArchive))
	m, _ = wizardUpdate(t, m, key("s"))
	if m.flow != flowSearch || m.step != jumphostStepProfile {
		t.Fatalf("flow = %v step = %v", m.flow, m.step)
	}
}

func TestWizardForgettingLastEntryLeavesStartScreen(t *testing.T) {
	runner := newHistoryRunner(savedArchive)
	m := startWizard(t, runner)
	m, _ = wizardUpdate(t, m, key("d"))
	m, cmd := wizardUpdate(t, m, key("y"))
	m = runChain(t, m, cmd, 3)
	if len(*runner.saved) != 0 {
		t.Fatal("the entry was not forgotten")
	}
	if m.step != jumphostStepType || m.hasHistory {
		t.Fatalf("step = %v hasHistory = %v, want the Type step once the history is empty", m.step, m.hasHistory)
	}
}

func TestWizardQIsTextWhileFilteringRecent(t *testing.T) {
	m := startWizard(t, newHistoryRunner(savedArchive))
	m, _ = wizardUpdate(t, m, key("/"))
	m, cmd := wizardUpdate(t, m, key("q"))
	if isQuit(cmd) || m.canceled {
		t.Fatal("q typed in the filter must not quit")
	}
}
