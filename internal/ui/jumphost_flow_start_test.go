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
	// accounts maps a profile to the AWS account it reaches; lookups expecting another one are refused.
	accounts map[string]string
	saved    *[]jumphost.SavedConnection
	lookups  *[]jumphost.Options
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

func (r historyRunner) FindResource(_ context.Context, options *jumphost.Options, account string) (jumphost.ResourceSummary, error) {
	*r.lookups = append(*r.lookups, *options)
	if r.lookupErr != nil {
		return jumphost.ResourceSummary{}, r.lookupErr
	}
	if got, known := r.accounts[options.AwsProfile]; known && account != "" && got != account {
		return jumphost.ResourceSummary{}, &jumphost.AccountMismatchError{Profile: options.AwsProfile, ProfileAccount: got, EntryAccount: account}
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

func TestWizardForgetWaitsForTheReload(t *testing.T) {
	other := savedArchive
	other.Ref.Name = "reporting"
	runner := newHistoryRunner(savedArchive, other)
	m := startWizard(t, runner)
	m, _ = wizardUpdate(t, m, key("d"))
	m, forget := wizardUpdate(t, m, key("y"))
	if !m.loading {
		t.Fatal("forgetting must show a loading screen until the history is reloaded")
	}
	m, cmd := wizardUpdate(t, m, key("enter"))
	if cmd != nil || m.flow != flowStart || len(*runner.lookups) != 0 {
		t.Fatalf("flow = %v lookups = %d, enter must wait for the reload", m.flow, len(*runner.lookups))
	}
	m = runChain(t, m, forget, 3)
	if m.loading || m.step != jumphostStepStart || len(m.recent.list.Items()) != 1 {
		t.Fatalf("loading = %v step = %v items = %d, want the reloaded start screen", m.loading, m.step, len(m.recent.list.Items()))
	}
}

func TestWizardLateHistoryReloadKeepsTheActiveFlow(t *testing.T) {
	for _, shortcut := range []string{"n", "s", "enter"} {
		m := startWizard(t, newHistoryRunner(savedArchive))
		m, _ = wizardUpdate(t, m, key(shortcut))
		flow, step, loading := m.flow, m.step, m.loading
		m, _ = wizardUpdate(t, m, jumphostSavedLoadedMsg{connections: []jumphost.SavedConnection{savedArchive}})
		if m.flow != flow || m.step != step || m.loading != loading {
			t.Fatalf("%s: flow = %v step = %v loading = %v, want %v %v %v", shortcut, m.flow, m.step, m.loading, flow, step, loading)
		}
	}
}

// portFailRunner remembers connections but cannot suggest a local port.
type portFailRunner struct {
	historyRunner
}

func (portFailRunner) ResolveDefaultLocalPort(context.Context, jumphost.Options) (int, error) {
	return 0, errors.New("no free local port")
}

func TestWizardRecentPortFailureRestoresBaseOptions(t *testing.T) {
	m := startWizard(t, portFailRunner{newHistoryRunner(savedArchive)})
	m, cmd := wizardUpdate(t, m, key("enter"))
	m = runChain(t, m, cmd, 3)
	if m.step != jumphostStepStart || m.flow != flowStart || !strings.Contains(m.errorMsg, "no free local port") {
		t.Fatalf("step = %v flow = %v error = %q, want the start screen with the error", m.step, m.flow, m.errorMsg)
	}
	m, _ = wizardUpdate(t, m, key("n"))
	if m.options.AwsRegion != "" || m.options.Name != "" || m.options.ResourceType != "" || m.options.AwsProfile == "acme-staging" {
		t.Fatalf("options = %+v, want the base options", m.options)
	}
}

// noRegionHistoryRunner remembers connections of profiles without default region.
type noRegionHistoryRunner struct {
	historyRunner
}

func (noRegionHistoryRunner) ResolveRegion(context.Context, string) string { return "" }

func TestWizardSearchRegionPickerCanceledOnStartScreenEndsTheSearch(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), noRegionHistoryRunner{newHistoryRunner(savedArchive)}, jumphost.Options{AwsProfile: "dev"})
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = runChain(t, m, m.Init(), 1)
	m, _ = wizardUpdate(t, m, key("s"))
	if !m.pickingRegion || m.step != jumphostStepStart {
		t.Fatalf("picking = %v step = %v, want the region picker over the start screen", m.pickingRegion, m.step)
	}
	m, _ = wizardUpdate(t, m, key("esc"))
	if m.pickingRegion || m.flow != flowStart || m.step != jumphostStepStart {
		t.Fatalf("picking = %v flow = %v step = %v, want back on the start screen", m.pickingRegion, m.flow, m.step)
	}
}

func TestWizardRecentRefusesProfileOfAnotherAccount(t *testing.T) {
	missing := savedArchive
	missing.ProfileMissing = true
	runner := newHistoryRunner(missing)
	runner.accounts = map[string]string{"dev": "1", "prod": "2"}
	m := startWizard(t, runner)
	m, cmd := wizardUpdate(t, m, key("enter"))
	m = runChain(t, m, cmd, 3)

	m, _ = wizardUpdate(t, m, key("down")) // prod
	m, cmd = wizardUpdate(t, m, key("enter"))
	m = runChain(t, m, cmd, 3)
	if m.step != jumphostStepProfile || m.flow != flowRecent || m.loading {
		t.Fatalf("step = %v flow = %v loading = %v, want the profile step again", m.step, m.flow, m.loading)
	}
	if want := "profile prod is account 2, this entry is account 1"; m.errorMsg != want {
		t.Fatalf("error = %q, want %q", m.errorMsg, want)
	}

	m, _ = wizardUpdate(t, m, key("up")) // dev
	m, cmd = wizardUpdate(t, m, key("enter"))
	m = runChain(t, m, cmd, 3)
	if m.step != jumphostStepReview || m.options.AwsProfile != "dev" {
		t.Fatalf("step = %v profile = %q, want the review with dev", m.step, m.options.AwsProfile)
	}
}

func TestWizardRecentSavedProfileOfAnotherAccountReturnsToStart(t *testing.T) {
	runner := newHistoryRunner(savedArchive)
	runner.accounts = map[string]string{"acme-staging": "9"}
	m := startWizard(t, runner)
	m, cmd := wizardUpdate(t, m, key("enter"))
	m = runChain(t, m, cmd, 3)
	if m.step != jumphostStepStart || m.flow != flowStart || !strings.Contains(m.errorMsg, "profile acme-staging is account 9, this entry is account 1") {
		t.Fatalf("step = %v flow = %v error = %q", m.step, m.flow, m.errorMsg)
	}
}
