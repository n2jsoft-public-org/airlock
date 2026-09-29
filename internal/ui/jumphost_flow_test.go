package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

func wizardUpdate(t *testing.T, m jumphostWizardModel, msg tea.Msg) (jumphostWizardModel, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	result, ok := updated.(jumphostWizardModel)
	if !ok {
		t.Fatalf("Update returned %T, want jumphostWizardModel", updated)
	}
	return result, cmd
}

// wizardAtReview drives a wizard to the review step of an RDS target.
func wizardAtReview(t *testing.T, runner JumphostDiscovery, base jumphost.Options, target jumphost.ResourceSummary) jumphostWizardModel {
	t.Helper()
	base.AwsProfile = "dev"
	m := newJumphostWizardModel(context.Background(), runner, base)
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pastStart(t, m)
	m, _ = wizardUpdate(t, m, key("enter")) // rds
	if m.awaitingRegion {
		m, _ = wizardUpdate(t, m, jumphostRegionResolvedMsg{profile: "dev", region: "eu-west-3"})
	}
	m, _ = wizardUpdate(t, m, jumphostTargetsLoadedMsg{targets: []jumphost.ResourceSummary{target}})
	m, _ = wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, jumphostDefaultPortLoadedMsg{port: 5433})
	if m.step != jumphostStepReview {
		t.Fatalf("step = %v, want the review step", m.step)
	}
	return m
}

// busyPortRunner refuses every local port but 6000.
type busyPortRunner struct {
	fakeSessionRunner
}

func (busyPortRunner) CheckLocalPort(port int) error {
	if port == 6000 {
		return nil
	}
	return fmt.Errorf("port %d is already in use", port)
}

// focusUserField tabs from the port over the passwordless toggle to the database user.
func focusUserField(t *testing.T, m jumphostWizardModel) jumphostWizardModel {
	t.Helper()
	m, _ = wizardUpdate(t, m, key("tab"))
	m, _ = wizardUpdate(t, m, key("tab"))
	if m.reviewFocus != reviewFieldUser {
		t.Fatalf("focus = %d, want the user field", m.reviewFocus)
	}
	return m
}

func clearInput(t *testing.T, m jumphostWizardModel) jumphostWizardModel {
	t.Helper()
	m, _ = wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	return m
}

func TestWizardReviewPrefillsPortAndDatabaseUser(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})

	if !m.portInput.Focused() || m.portInput.Value() != "5433" {
		t.Fatalf("port = %q focused = %v, want the suggested port focused", m.portInput.Value(), m.portInput.Focused())
	}
	if got := m.userInput.Value(); got != testDefaultDBUser {
		t.Fatalf("user input = %q, want prefilled with %q", got, testDefaultDBUser)
	}
	view := m.View()
	for _, want := range []string{"archive", "5433", testDefaultDBUser, "Connect"} {
		if !strings.Contains(view, want) {
			t.Errorf("review does not show %q", want)
		}
	}
}

func TestWizardReviewConnectsWithDefaults(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})

	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) {
		t.Fatal("enter on the review did not connect")
	}
	if m.options.LocalPortNumber != 5433 || m.options.DBUser != testDefaultDBUser {
		t.Fatalf("options = %+v, want port 5433 and the default user", m.options)
	}
}

func TestWizardReviewEditsBothFieldsBeforeConnecting(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})

	m = clearInput(t, m)
	m, _ = wizardUpdate(t, m, key("6000"))
	m = focusUserField(t, m)
	if !m.userInput.Focused() || m.portInput.Focused() {
		t.Fatal("tab did not move the focus to the user field")
	}

	// "q" is part of the typed name here, not a cancel.
	m = clearInput(t, m)
	m, _ = wizardUpdate(t, m, key("qa_ro"))
	if m.canceled {
		t.Fatal("typing q in the review canceled the wizard")
	}

	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) {
		t.Fatal("wizard did not connect from the user field")
	}
	if m.options.DBUser != "qa_ro" || m.options.LocalPortNumber != 6000 {
		t.Fatalf("options = %+v, want DBUser qa_ro on port 6000", m.options)
	}
}

func TestWizardReviewPrefillsDatabaseUserFromOptions(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{DBUser: "app_ro"}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})
	if got := m.userInput.Value(); got != "app_ro" {
		t.Fatalf("user input = %q, want app_ro", got)
	}
}

func TestWizardReviewEmptyDatabaseUserFallsBackToDefault(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{DBUser: "app_ro"}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})

	m = focusUserField(t, m)
	m = clearInput(t, m)
	m, _ = wizardUpdate(t, m, key("enter"))
	if m.options.DBUser != testDefaultDBUser {
		t.Fatalf("DBUser = %q, want %q", m.options.DBUser, testDefaultDBUser)
	}
}

func TestWizardReviewInvalidPortFocusesPort(t *testing.T) {
	m := wizardAtReview(t, busyPortRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})

	m, _ = wizardUpdate(t, m, key("tab"))
	m, cmd := wizardUpdate(t, m, key("enter"))
	if isQuit(cmd) || !strings.Contains(m.errorMsg, "5433") {
		t.Fatalf("quit = %v error = %q, want the busy port refused", isQuit(cmd), m.errorMsg)
	}
	if !m.portInput.Focused() || m.userInput.Focused() {
		t.Fatal("the invalid port field should get the focus back")
	}

	m = clearInput(t, m)
	m, _ = wizardUpdate(t, m, key("abc"))
	if _, cmd = wizardUpdate(t, m, key("enter")); isQuit(cmd) {
		t.Fatal("a non-numeric port was accepted")
	}
}

func TestWizardReviewRejectsDatabaseUserWithSpaces(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})

	m = focusUserField(t, m)
	m = clearInput(t, m)
	m, _ = wizardUpdate(t, m, key("app ro"))
	m, cmd := wizardUpdate(t, m, key("enter"))
	if isQuit(cmd) || m.errorMsg == "" || !m.userInput.Focused() {
		t.Fatalf("quit = %v error = %q, want the user refused with focus kept", isQuit(cmd), m.errorMsg)
	}
}

func TestWizardReviewWithoutIAMAuthOrMCPHasOnlyPort(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{SkipMCP: true}, jumphost.ResourceSummary{Name: "mssql"})

	m, _ = wizardUpdate(t, m, key("tab"))
	if !m.portInput.Focused() || m.userInput.Focused() {
		t.Fatal("tab should stay on the port when there is no other field")
	}
	if strings.Contains(m.View(), testDefaultDBUser) {
		t.Fatal("the review shows a database user for an instance without IAM auth")
	}
	if _, cmd := wizardUpdate(t, m, key("enter")); !isQuit(cmd) {
		t.Fatal("wizard should connect from the review")
	}
}

func TestWizardReviewGoesBackToTarget(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})

	m, _ = wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.step != jumphostStepTarget || m.portInput.Focused() || m.userInput.Focused() {
		t.Fatalf("step = %v, want back on the target list with inputs blurred", m.step)
	}
}

func TestWizardKeepsDatabaseUserForNewConnections(t *testing.T) {
	m := newSSMModel(context.Background(), fakeSessionRunner{}, newProcessTracker(), jumphost.Options{DBUser: "app_ro"})
	m = m.addSession(&jumphost.Session{Label: "db", Target: "db", ResourceType: "rds"})
	m.sessions[0].state = ssmSessionRunning
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, _ = update(t, m, key("n"))
	if m.wizard.options.DBUser != "app_ro" {
		t.Fatalf("wizard DBUser = %q, want app_ro", m.wizard.options.DBUser)
	}
}

// recordingRunner remembers the options of every target discovery.
type recordingRunner struct {
	fakeSessionRunner
	discoveries *[]jumphost.Options
}

func (r recordingRunner) ListResourceSummaries(_ context.Context, options *jumphost.Options) ([]jumphost.ResourceSummary, error) {
	*r.discoveries = append(*r.discoveries, *options)
	return []jumphost.ResourceSummary{{Name: "db"}}, nil
}

func newTestWizard(t *testing.T, runner JumphostDiscovery, base jumphost.Options) jumphostWizardModel {
	t.Helper()
	if base.AwsProfile == "" {
		base.AwsProfile = "dev"
	}
	m := newJumphostWizardModel(context.Background(), runner, base)
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pastStart(t, m)
	return m
}

// runCmd executes cmd and feeds every message it produces, batches included, back into the wizard.
func runCmd(t *testing.T, m jumphostWizardModel, cmd tea.Cmd) jumphostWizardModel {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := runWithTimeout(cmd).(type) {
	case tea.BatchMsg:
		for _, inner := range msg {
			m = runCmd(t, m, inner)
		}
	case nil, loadingTickMsg:
		// Spinner ticks only animate.
	default:
		m, _ = wizardUpdate(t, m, msg)
	}
	return m
}

// runWithTimeout runs cmd, reporting commands that keep waiting as a spinner tick.
func runWithTimeout(cmd tea.Cmd) tea.Msg {
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case msg := <-result:
		return msg
	case <-time.After(300 * time.Millisecond):
		return loadingTickMsg{}
	}
}

func TestWizardStartsOnResourceTypeWithResolvedRegion(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	if m.step != jumphostStepType {
		t.Fatalf("step = %v, want the resource type step", m.step)
	}

	m = runCmd(t, m, m.Init())
	if got := m.currentRegion(); got != "eu-west-3" {
		t.Fatalf("region = %q, want the resolved default eu-west-3", got)
	}
	if m.options.AwsRegion != "" {
		t.Fatalf("AwsRegion = %q, a resolved default must not be forced on the runner", m.options.AwsRegion)
	}
}

func TestWizardKeepsExplicitRegion(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{AwsRegion: "us-east-1"})
	if m.resolveRegionCmd() != nil {
		t.Fatal("no region lookup expected when a region is given")
	}
	if got := m.currentRegion(); got != "us-east-1" {
		t.Fatalf("region = %q, want us-east-1", got)
	}
}

func TestWizardIgnoresRegionResolvedForAnotherProfile(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	m, _ = wizardUpdate(t, m, jumphostRegionResolvedMsg{profile: "prod", region: "us-west-2"})
	if m.defaultRegion != "" {
		t.Fatalf("defaultRegion = %q, want stale answer ignored", m.defaultRegion)
	}
}

func TestWizardRegionShortcutSwitchesRegion(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})

	m, _ = wizardUpdate(t, m, key("r"))
	if !m.pickingRegion {
		t.Fatal("r did not open the region picker")
	}
	// Keys go to the search box, not to the wizard: q must not cancel.
	m, _ = wizardUpdate(t, m, key("q"))
	if m.canceled {
		t.Fatal("typing in the region search canceled the wizard")
	}
	m, _ = wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	m, _ = wizardUpdate(t, m, key("frankfurt"))
	m, _ = wizardUpdate(t, m, key("enter"))

	if m.pickingRegion || m.options.AwsRegion != "eu-central-1" {
		t.Fatalf("picking = %v region = %q, want eu-central-1 chosen", m.pickingRegion, m.options.AwsRegion)
	}
	if m.step != jumphostStepType {
		t.Fatalf("step = %v, want to stay on the type step", m.step)
	}
}

func TestWizardRegionPickerEscKeepsRegion(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{AwsRegion: "eu-west-3"})
	m, _ = wizardUpdate(t, m, key("r"))
	m, _ = wizardUpdate(t, m, key("tokyo"))
	m, _ = wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.pickingRegion || m.options.AwsRegion != "eu-west-3" || m.step != jumphostStepType {
		t.Fatalf("picking = %v region = %q step = %v, want picker closed without change", m.pickingRegion, m.options.AwsRegion, m.step)
	}
}

func TestWizardRegionChangeReloadsTargets(t *testing.T) {
	var discoveries []jumphost.Options
	m := newTestWizard(t, recordingRunner{discoveries: &discoveries}, jumphost.Options{AwsRegion: "eu-west-3"})

	m, cmd := wizardUpdate(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	if m.step != jumphostStepTarget {
		t.Fatalf("step = %v, want the target step", m.step)
	}

	m, _ = wizardUpdate(t, m, key("r"))
	m, _ = wizardUpdate(t, m, key("ireland"))
	m, cmd = wizardUpdate(t, m, key("enter"))
	if !m.loading {
		t.Fatal("changing the region on the target step should reload targets")
	}
	m = runCmd(t, m, cmd)

	if len(discoveries) != 2 || discoveries[1].AwsRegion != "eu-west-1" {
		t.Fatalf("discoveries = %+v, want a second one in eu-west-1", discoveries)
	}
	if m.step != jumphostStepTarget || m.loading {
		t.Fatalf("step = %v loading = %v, want back on the loaded target list", m.step, m.loading)
	}
}

func TestWizardRegionShortcutIsTextWhileFilteringTargets(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	m, _ = wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, jumphostTargetsLoadedMsg{targets: []jumphost.ResourceSummary{{Name: "reporting"}, {Name: "archive"}}})

	m, _ = wizardUpdate(t, m, key("/"))
	m, _ = wizardUpdate(t, m, key("r"))
	m, _ = wizardUpdate(t, m, key("q"))
	if m.pickingRegion || m.canceled {
		t.Fatalf("picking = %v canceled = %v, want r and q typed into the filter", m.pickingRegion, m.canceled)
	}
	if got := m.targetList.FilterValue(); got != "rq" {
		t.Fatalf("filter = %q, want rq", got)
	}
}

func TestWizardDigitPicksResourceType(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	m, _ = wizardUpdate(t, m, key("2"))
	if m.options.ResourceType != "eks" || !m.loading {
		t.Fatalf("type = %q loading = %v, want eks discovery started", m.options.ResourceType, m.loading)
	}
}

func TestWizardNoTargetsMentionsRegion(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{AwsRegion: "eu-west-1"})
	m, _ = wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, jumphostTargetsLoadedMsg{})
	if m.step != jumphostStepType || !strings.Contains(m.errorMsg, "eu-west-1") {
		t.Fatalf("step = %v error = %q, want back on type with the region in the message", m.step, m.errorMsg)
	}
}

func TestWizardViewFitsTerminal(t *testing.T) {
	sizes := []tea.WindowSizeMsg{{Width: 140, Height: 40}, {Width: 70, Height: 24}, {Width: 60, Height: 16}}
	for _, size := range sizes {
		views := map[string]jumphostWizardModel{}

		m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
		m, _ = wizardUpdate(t, m, size)
		views["loading history"] = m
		start := newJumphostWizardModel(context.Background(), newHistoryRunner(savedArchive), jumphost.Options{})
		start, _ = wizardUpdate(t, start, size)
		views["start"] = runCmd(t, start, start.loadSavedCmd())
		m = pastStart(t, m)
		m.errorMsg = "No RDS targets found in a-very-long-region-name-that-overflows-everything"
		views["type"] = m
		picker, _ := wizardUpdate(t, m, key("r"))
		views["picker"] = picker
		m, _ = wizardUpdate(t, m, key("enter")) // rds, then profiles load
		views["loading"] = m
		m, _ = wizardUpdate(t, m, jumphostProfilesLoadedMsg{profiles: []jumphost.AWSProfileSummary{{Name: "dev", Description: "region eu-west-3"}}})
		views["profile"] = m
		m, _ = wizardUpdate(t, m, key("enter"))
		m, _ = wizardUpdate(t, m, jumphostRegionResolvedMsg{profile: "dev", region: "eu-west-3"})
		m, _ = wizardUpdate(t, m, jumphostTargetsLoadedMsg{targets: []jumphost.ResourceSummary{{Name: strings.Repeat("long-name-", 20), IAMAuth: true}}})
		views["target"] = m
		m, _ = wizardUpdate(t, m, key("enter"))
		m, _ = wizardUpdate(t, m, jumphostDefaultPortLoadedMsg{port: 5432})
		views["review"] = m

		for name, view := range views {
			out := view.View()
			if got := lipgloss.Height(out); got > size.Height {
				t.Errorf("%dx%d %s: height %d exceeds terminal", size.Width, size.Height, name, got)
			}
			if got := lipgloss.Width(out); got > size.Width {
				t.Errorf("%dx%d %s: width %d exceeds terminal", size.Width, size.Height, name, got)
			}
		}
	}
}

func TestWizardReviewKeepsEditableFieldsOnShortTerminals(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 60, Height: 16})

	view := m.View()
	for _, want := range []string{"5433", testDefaultDBUser} {
		if !strings.Contains(view, want) {
			t.Errorf("compact review does not show %q:\n%s", want, view)
		}
	}
}

func TestWizardReviewIsPasswordlessByDefault(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})

	if !m.passwordless || strings.Contains(m.View(), "Password") {
		t.Fatal("an IAM target should start passwordless, without a password field")
	}
	m, _ = wizardUpdate(t, m, key("tab"))
	if m.reviewFocus != reviewFieldPasswordless || !strings.Contains(m.View(), "password generated by airlock") {
		t.Fatalf("focus = %d, want the passwordless toggle shown on", m.reviewFocus)
	}
	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) || m.options.DBPassword != "" || m.options.DBUser != testDefaultDBUser {
		t.Fatalf("options = %+v, want an IAM connection with the default user", m.options)
	}
}

// turnPasswordlessOff toggles passwordless sign-in off from the port field.
func turnPasswordlessOff(t *testing.T, m jumphostWizardModel) jumphostWizardModel {
	t.Helper()
	m, _ = wizardUpdate(t, m, key("tab"))
	m, _ = wizardUpdate(t, m, key(" "))
	if m.passwordless {
		t.Fatal("space on the toggle did not turn passwordless off")
	}
	return m
}

func TestWizardReviewAsksPasswordWhenPasswordlessIsOff(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})
	m = turnPasswordlessOff(t, m)
	if !strings.Contains(m.View(), "Password") {
		t.Fatal("the password field should show once passwordless is off")
	}

	m, _ = wizardUpdate(t, m, key("tab"))
	m = clearInput(t, m)
	m, _ = wizardUpdate(t, m, key("app_user"))
	m, _ = wizardUpdate(t, m, key("tab"))
	if !m.passwordInput.Focused() {
		t.Fatal("tab did not move from the user to the password field")
	}
	m, _ = wizardUpdate(t, m, key("s3cret q"))
	if strings.Contains(m.View(), "s3cret") {
		t.Fatal("the password must be masked")
	}

	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) {
		t.Fatal("wizard did not connect with a password")
	}
	if m.options.DBUser != "app_user" || m.options.DBPassword != "s3cret q" {
		t.Fatalf("options = %+v, want app_user signing in with the typed password", m.options)
	}
}

func TestWizardReviewRequiresPasswordAndUserWhenPasswordlessIsOff(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})
	m = turnPasswordlessOff(t, m)

	m, cmd := wizardUpdate(t, m, key("enter"))
	if isQuit(cmd) || !m.passwordInput.Focused() || !strings.Contains(m.errorMsg, "password") {
		t.Fatalf("quit = %v error = %q, want the empty password refused", isQuit(cmd), m.errorMsg)
	}

	m, _ = wizardUpdate(t, m, key("s3cret"))
	m, _ = wizardUpdate(t, m, key("shift+tab"))
	m = clearInput(t, m)
	m, cmd = wizardUpdate(t, m, key("enter"))
	if isQuit(cmd) || !m.userInput.Focused() || !strings.Contains(m.errorMsg, "user") {
		t.Fatalf("quit = %v error = %q, want the empty user refused", isQuit(cmd), m.errorMsg)
	}
}

func TestWizardReviewTurningPasswordlessBackOnDropsPassword(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})
	m = turnPasswordlessOff(t, m)
	m.passwordInput.SetValue("s3cret")

	m, _ = wizardUpdate(t, m, key(" "))
	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) || m.options.DBPassword != "" {
		t.Fatalf("options = %+v, want an IAM connection without password", m.options)
	}
}

func TestWizardReviewResetsPasswordForAnotherTarget(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})
	m = turnPasswordlessOff(t, m)
	m.passwordInput.SetValue("s3cret")

	m, _ = wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, jumphostDefaultPortLoadedMsg{port: 5433})
	if !m.passwordless || m.passwordInput.Value() != "" {
		t.Fatal("a new connection must start passwordless with an empty password")
	}
}

// focusMCPWritesField tabs backwards from the port, wrapping around to the last field: the MCP toggle.
func focusMCPWritesField(t *testing.T, m jumphostWizardModel) jumphostWizardModel {
	t.Helper()
	m, _ = wizardUpdate(t, m, key("shift+tab"))
	if m.reviewFocus != reviewFieldMCPWrites {
		t.Fatalf("focus = %d, want the MCP writes toggle", m.reviewFocus)
	}
	return m
}

func TestWizardReviewMCPIsReadOnlyByDefault(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})

	if m.mcpWrites || !strings.Contains(m.View(), "read-only") {
		t.Fatal("an RDS connection should start with a read-only MCP server")
	}
	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) || m.options.MCPAllowWrites {
		t.Fatalf("options = %+v, want a read-only MCP server", m.options)
	}
}

func TestWizardReviewTogglesMCPWrites(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive"})

	// Without IAM sign-in fields, the toggle directly follows the port.
	m, _ = wizardUpdate(t, m, key("tab"))
	if m.reviewFocus != reviewFieldMCPWrites {
		t.Fatalf("focus = %d, want the MCP writes toggle after the port", m.reviewFocus)
	}
	m, _ = wizardUpdate(t, m, key(" "))
	if !m.mcpWrites || !strings.Contains(m.View(), "writes allowed") {
		t.Fatal("space on the toggle did not allow MCP writes")
	}
	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) || !m.options.MCPAllowWrites {
		t.Fatalf("options = %+v, want MCP writes allowed", m.options)
	}
}

func TestWizardReviewMCPWritesKeepsIAMSignIn(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})
	m = focusMCPWritesField(t, m)
	m, _ = wizardUpdate(t, m, key("l"))

	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) || !m.options.MCPAllowWrites || m.options.DBUser != testDefaultDBUser {
		t.Fatalf("options = %+v, want MCP writes with the default IAM user", m.options)
	}
}

func TestWizardReviewMCPWritesStartsFromFlagAndResetsPerTarget(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{MCPAllowWrites: true}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})
	if !m.mcpWrites {
		t.Fatal("--mcp-allow-writes should turn the toggle on")
	}
	m = focusMCPWritesField(t, m)
	m, _ = wizardUpdate(t, m, key(" "))

	m, _ = wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, jumphostDefaultPortLoadedMsg{port: 5433})
	if !m.mcpWrites {
		t.Fatal("a new connection must start from the --mcp-allow-writes default")
	}
}

func TestWizardReviewHidesMCPToggleWithoutMCP(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{SkipMCP: true}, jumphost.ResourceSummary{Name: "archive"})

	if strings.Contains(m.View(), "read-only") || len(m.reviewFields()) != 1 {
		t.Fatal("the MCP toggle should not show when the MCP server is skipped")
	}
}

// prefsRunner remembers the saved choices in memory, and refuses the local ports in busy.
type prefsRunner struct {
	fakeSessionRunner
	saved map[string]jumphost.ConnectionPrefs
	busy  map[int]bool
	// lastSave is the options of the last SaveConnectionPrefs call.
	lastSave *jumphost.Options
}

func newPrefsRunner() *prefsRunner {
	return &prefsRunner{saved: map[string]jumphost.ConnectionPrefs{}, busy: map[int]bool{}, lastSave: &jumphost.Options{}}
}

func (r *prefsRunner) LoadConnectionPrefs(_ context.Context, options jumphost.Options) (jumphost.ConnectionPrefs, bool) {
	prefs, ok := r.saved[options.AwsRegion+"/"+options.Name]
	return prefs, ok
}

func (r *prefsRunner) SaveConnectionPrefs(_ context.Context, options jumphost.Options) error {
	*r.lastSave = options
	return nil
}

func (r *prefsRunner) CheckLocalPort(port int) error {
	if r.busy[port] {
		return fmt.Errorf("port %d is already in use", port)
	}
	return nil
}

// wizardAtReviewWithRegion is wizardAtReview once the default region of the profile is resolved.
func wizardAtReviewWithRegion(t *testing.T, runner JumphostDiscovery, base jumphost.Options, target jumphost.ResourceSummary) jumphostWizardModel {
	t.Helper()
	base.AwsProfile = "dev"
	m := newJumphostWizardModel(context.Background(), runner, base)
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pastStart(t, m)
	m, _ = wizardUpdate(t, m, jumphostRegionResolvedMsg{profile: "dev", region: "eu-west-3"})
	m, _ = wizardUpdate(t, m, key("enter")) // rds
	m, _ = wizardUpdate(t, m, jumphostTargetsLoadedMsg{targets: []jumphost.ResourceSummary{target}})
	m, cmd := wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, findMsg[jumphostDefaultPortLoadedMsg](t, cmd))
	if m.step != jumphostStepReview {
		t.Fatalf("step = %v, want the review step", m.step)
	}
	return m
}

// findMsg runs cmd (and the commands of a batch) until one returns a T.
func findMsg[T tea.Msg](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	var zero T
	if cmd == nil {
		t.Fatalf("no command returning %T", zero)
	}
	switch msg := cmd().(type) {
	case T:
		return msg
	case tea.BatchMsg:
		for _, inner := range msg {
			if inner == nil {
				continue
			}
			if found, ok := inner().(T); ok {
				return found
			}
		}
	}
	t.Fatalf("no command returning %T", zero)
	return zero
}

func TestWizardReviewPrefillsSavedChoices(t *testing.T) {
	runner := newPrefsRunner()
	runner.saved["eu-west-3/archive"] = jumphost.ConnectionPrefs{DBUser: "acme_rw", SignIn: jumphost.SignInPassword, LocalPort: 6100, MCPAllowWrites: true}

	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})
	if m.portInput.Value() != "6100" || m.userInput.Value() != "acme_rw" || m.passwordless || !m.mcpWrites {
		t.Fatalf("port = %q user = %q passwordless = %v writes = %v, want the saved choices",
			m.portInput.Value(), m.userInput.Value(), m.passwordless, m.mcpWrites)
	}
	if m.passwordInput.Value() != "" {
		t.Fatal("no password may be prefilled")
	}
}

func TestWizardReviewSkipsBusySavedPortAndKeepsUserFlag(t *testing.T) {
	runner := newPrefsRunner()
	runner.saved["eu-west-3/archive"] = jumphost.ConnectionPrefs{DBUser: "acme_rw", LocalPort: 6100}
	runner.busy[6100] = true

	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{DBUser: "flag_user"}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})
	if m.portInput.Value() != "5432" || m.userInput.Value() != "flag_user" || !m.passwordless {
		t.Fatalf("port = %q user = %q passwordless = %v, want the suggested port and the --db-user flag",
			m.portInput.Value(), m.userInput.Value(), m.passwordless)
	}
}

func TestWizardReviewSavesChoicesOnConnect(t *testing.T) {
	runner := newPrefsRunner()
	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})
	m = focusMCPWritesField(t, m)
	m, _ = wizardUpdate(t, m, key(" "))

	if _, cmd := wizardUpdate(t, m, key("enter")); !isQuit(cmd) {
		t.Fatal("enter on the review did not connect")
	}
	saved := *runner.lastSave
	if saved.AwsRegion != "eu-west-3" || saved.Name != "archive" || !saved.MCPAllowWrites || saved.LocalPortNumber != 5432 || saved.DBUser != testDefaultDBUser {
		t.Fatalf("saved options = %+v, want the connection's choices under its region", saved)
	}
}

// noDefaultsRunner has no default database user and no default region.
type noDefaultsRunner struct {
	fakeSessionRunner
}

func (noDefaultsRunner) ResolveDBUser(user string) string { return strings.TrimSpace(user) }

func (noDefaultsRunner) ResolveRegion(context.Context, string) string { return "" }

func TestWizardReviewRequiresUserWithoutDefault(t *testing.T) {
	m := wizardAtReview(t, noDefaultsRunner{}, jumphost.Options{AwsRegion: "eu-west-3"}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true})
	if got := m.userInput.Value(); got != "" {
		t.Fatalf("user input = %q, want empty without a default user", got)
	}
	m, cmd := wizardUpdate(t, m, key("enter"))
	if isQuit(cmd) {
		t.Fatal("the wizard connected with an empty database user")
	}
	if m.reviewFocus != reviewFieldUser || !strings.Contains(m.errorMsg, "database user") {
		t.Fatalf("focus = %d error = %q, want the user field flagged", m.reviewFocus, m.errorMsg)
	}
}

func TestWizardOpensRegionPickerWhenNoRegion(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), noDefaultsRunner{}, jumphost.Options{AwsProfile: "dev"})
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pastStart(t, m)
	m, _ = wizardUpdate(t, m, jumphostRegionResolvedMsg{profile: "dev", region: ""})
	if !m.regionResolved {
		t.Fatal("an empty answer still resolves the region")
	}
	m, _ = wizardUpdate(t, m, key("enter")) // rds
	if !m.pickingRegion || m.loading {
		t.Fatalf("pickingRegion = %v loading = %v, want the region picker before discovery", m.pickingRegion, m.loading)
	}
	if !strings.Contains(m.errorMsg, "region") {
		t.Fatalf("error = %q, want a hint to pick a region", m.errorMsg)
	}
}

func TestWizardHeaderShowsMissingRegion(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), noDefaultsRunner{}, jumphost.Options{AwsProfile: "dev"})
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pastStart(t, m)
	m, _ = wizardUpdate(t, m, jumphostRegionResolvedMsg{profile: "dev", region: ""})
	if view := m.View(); !strings.Contains(view, "none") {
		t.Fatalf("header should show that no region is set:\n%s", view)
	}
}

// pastStart delivers an empty history, which leaves the wizard on the Type step of a new connection.
func pastStart(t *testing.T, m jumphostWizardModel) jumphostWizardModel {
	t.Helper()
	m, _ = wizardUpdate(t, m, jumphostSavedLoadedMsg{})
	return m
}

func TestWizardNewConnectionAsksTypeThenProfile(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !m.loading || m.step != jumphostStepStart {
		t.Fatalf("step = %v loading = %v, want the history loading first", m.step, m.loading)
	}
	m = pastStart(t, m)
	if m.step != jumphostStepType || m.flow != flowNew {
		t.Fatalf("step = %v flow = %v, want the Type step of a new connection", m.step, m.flow)
	}

	m, _ = wizardUpdate(t, m, key("down")) // eks
	m, cmd := wizardUpdate(t, m, key("enter"))
	if m.options.ResourceType != "eks" || !m.loading {
		t.Fatalf("type = %q loading = %v, want eks and profiles loading", m.options.ResourceType, m.loading)
	}
	m, _ = wizardUpdate(t, m, findMsg[jumphostProfilesLoadedMsg](t, cmd))
	if m.step != jumphostStepProfile {
		t.Fatalf("step = %v, want the profile step after the type", m.step)
	}

	m, _ = wizardUpdate(t, m, key("enter")) // dev
	if !m.awaitingRegion {
		t.Fatal("the targets must wait for the region of the chosen profile")
	}
	var discoveries []jumphost.Options
	m.runner = recordingRunner{discoveries: &discoveries}
	m, cmd = wizardUpdate(t, m, jumphostRegionResolvedMsg{profile: "dev", region: "eu-west-3"})
	m = runCmd(t, m, cmd)
	if len(discoveries) != 1 || discoveries[0].ResourceType != "eks" || discoveries[0].AwsProfile != "dev" {
		t.Fatalf("discoveries = %+v, want eks targets of dev", discoveries)
	}
	if m.step != jumphostStepTarget {
		t.Fatalf("step = %v, want the target step", m.step)
	}
}

func TestWizardNewConnectionSkipsGivenProfile(t *testing.T) {
	var discoveries []jumphost.Options
	m := newTestWizard(t, recordingRunner{discoveries: &discoveries}, jumphost.Options{AwsRegion: "eu-west-3"})
	m, cmd := wizardUpdate(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	if m.step != jumphostStepTarget || len(discoveries) != 1 {
		t.Fatalf("step = %v discoveries = %d, want targets without a profile step", m.step, len(discoveries))
	}
}

func TestWizardBreadcrumbFollowsFlow(t *testing.T) {
	asked := pastStart(t, newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{}))
	steps, current := asked.breadcrumb()
	if strings.Join(steps, ",") != "Type,Profile,Target,Connect" || current != 0 {
		t.Fatalf("breadcrumb = %v %d", steps, current)
	}
	given := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	steps, _ = given.breadcrumb()
	if strings.Join(steps, ",") != "Type,Target,Connect" {
		t.Fatalf("breadcrumb with --profile = %v", steps)
	}
}

func TestWizardEscBacktracksNewFlow(t *testing.T) {
	m := pastStart(t, newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{}))
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = wizardUpdate(t, m, key("esc"))
	if m.step != jumphostStepType {
		t.Fatalf("step = %v, esc on the first step without history must stay", m.step)
	}
	m, cmd := wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, findMsg[jumphostProfilesLoadedMsg](t, cmd))
	m, _ = wizardUpdate(t, m, key("esc"))
	if m.step != jumphostStepType {
		t.Fatalf("step = %v, want back on the Type step", m.step)
	}
	m, _ = wizardUpdate(t, m, key("enter"))
	if m.loading || m.step != jumphostStepProfile {
		t.Fatalf("loading = %v step = %v, profiles must load only once", m.loading, m.step)
	}
}

func TestWizardWarnsAboutUnreadableHistory(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m, _ = wizardUpdate(t, m, jumphostSavedLoadedMsg{err: errors.New("failed to parse")})
	if m.step != jumphostStepType || !strings.Contains(m.errorMsg, "connections.json unreadable: failed to parse") {
		t.Fatalf("step = %v error = %q", m.step, m.errorMsg)
	}
}
