package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/dockerdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// dockerReview opens the review of the container pg from the Docker list.
func dockerReview(t *testing.T, source *fakeDockerContainers) jumphostWizardModel {
	t.Helper()
	m := dockerListStep(t, newDockerWizard(t, source))
	m.dockerList.selectName("pg")
	m, _ = wizardUpdate(t, m, key("enter"))
	if m.step != jumphostStepReview || m.dockerContainer.Name != "pg" {
		t.Fatalf("step = %v container = %q, want the review of pg", m.step, m.dockerContainer.Name)
	}
	return m
}

func TestDockerReviewPrefillsThePasswordAndWrites(t *testing.T) {
	m := dockerReview(t, &fakeDockerContainers{list: testContainers()})
	if m.passwordInput.Value() != "secret" || !m.mcpWrites {
		t.Fatalf("password = %q writes = %v", m.passwordInput.Value(), m.mcpWrites)
	}
	view := wizardView(m)
	for _, want := range []string{"postgres:16", "127.0.0.1:55432", "appdb", "postgres"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view misses %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "secret") {
		t.Fatalf("the password is shown:\n%s", view)
	}
}

func TestDockerReviewConnects(t *testing.T) {
	m := dockerReview(t, &fakeDockerContainers{list: testContainers()})
	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) {
		t.Fatal("enter did not connect")
	}
	got := m.options
	if got.ResourceType != "docker" || got.Name != "pg" || got.DBUser != "postgres" || got.DBPassword != "secret" ||
		got.SignIn != jumphost.SignInPassword || got.RememberPassword || !got.MCPAllowWrites || got.LocalPortNumber != 0 {
		t.Fatalf("options = %+v", got)
	}
}

func TestDockerReviewTurnsWritesOff(t *testing.T) {
	m := dockerReview(t, &fakeDockerContainers{list: testContainers()})
	m, _ = wizardUpdate(t, m, key("tab"))
	m, _ = wizardUpdate(t, m, key(" "))
	m, _ = wizardUpdate(t, m, key("enter"))
	if m.options.MCPAllowWrites {
		t.Fatal("writes toggled off still on")
	}
}

func TestDockerReviewRequiresAPasswordUnlessTrust(t *testing.T) {
	unknown := testContainers()[1]
	unknown.Password = ""
	m := dockerReview(t, &fakeDockerContainers{list: []dockerdb.Container{unknown}})
	m, cmd := wizardUpdate(t, m, key("enter"))
	if cmd != nil || m.errorMsg != "Enter the password of postgres" {
		t.Fatalf("error = %q, want the password asked", m.errorMsg)
	}

	unknown.Trust = true
	m = dockerReview(t, &fakeDockerContainers{list: []dockerdb.Container{unknown}})
	if m, cmd = wizardUpdate(t, m, key("enter")); !isQuit(cmd) || m.options.DBPassword != "" {
		t.Fatalf("trust: connected = %v password = %q", isQuit(cmd), m.options.DBPassword)
	}
}

func TestWizardDockerAtStartOpensTheReview(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.docker = &fakeDockerContainers{list: testContainers()}
	m.dockerAtStart, m.openDocker = true, "pg"
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, cmd := wizardUpdate(t, m, jumphostSavedLoadedMsg{})
	m, _ = wizardUpdate(t, m, findMsg[dockerListedMsg](t, cmd))
	if m.step != jumphostStepReview || m.dockerContainer.Name != "pg" || m.openDocker != "" || m.dockerAtStart {
		t.Fatalf("step = %v container = %q open = %q", m.step, m.dockerContainer.Name, m.openDocker)
	}
	if m, _ = wizardUpdate(t, m, key("esc")); m.step != jumphostStepTarget || !m.isDocker() {
		t.Fatalf("esc: step = %v, want the Docker list", m.step)
	}
}

func TestWizardDockerAtStartWithoutNameShowsTheList(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.docker = &fakeDockerContainers{list: testContainers()}
	m.dockerAtStart = true
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, cmd := wizardUpdate(t, m, jumphostSavedLoadedMsg{})
	m, _ = wizardUpdate(t, m, findMsg[dockerListedMsg](t, cmd))
	if m.step != jumphostStepTarget || !m.isDocker() || m.dockerList.count() != 3 {
		t.Fatalf("step = %v docker = %v", m.step, m.isDocker())
	}
}

func TestWizardDockerAtStartUnknownName(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.docker = &fakeDockerContainers{list: testContainers()}
	m.dockerAtStart, m.openDocker = true, "nope"
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, cmd := wizardUpdate(t, m, jumphostSavedLoadedMsg{})
	m, _ = wizardUpdate(t, m, findMsg[dockerListedMsg](t, cmd))
	if m.step != jumphostStepTarget || m.errorMsg != "no running postgres or sqlserver container named nope" {
		t.Fatalf("step = %v error = %q", m.step, m.errorMsg)
	}
}

func TestRejectedDockerPasswordReopensItsReview(t *testing.T) {
	service := newFakeService()
	m := newSSMModel(context.Background(), service, fakeSessionRunner{}, jumphost.Options{})
	m.docker = &fakeDockerContainers{list: testContainers()}
	m.sessions = []sessions.Session{{ID: 7, ResourceType: sessions.DockerResourceType, Target: "pg", State: sessions.StateEnded}}
	m.mode, m.selected = ssmModeSessions, 0
	next, _ := m.askDirectPasswordAgain(sessions.Event{Kind: sessions.EventPasswordRejected, ID: 7, Name: "pg", User: "postgres"})
	got, ok := next.(ssmModel)
	if !ok {
		t.Fatalf("model = %T", next)
	}
	if !got.wizard.dockerAtStart || got.wizard.openDocker != "pg" || got.wizard.openDirect != "" ||
		got.wizard.openDockerError != "The database rejected the password of postgres: enter it again." {
		t.Fatalf("wizard = docker %v %q direct %q error %q", got.wizard.dockerAtStart, got.wizard.openDocker, got.wizard.openDirect, got.wizard.openDockerError)
	}
}

func TestLateDockerListingAfterEscStaysOnType(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.docker = &fakeDockerContainers{list: testContainers()}
	m.dockerAtStart, m.openDocker = true, "pg"
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, cmd := wizardUpdate(t, m, jumphostSavedLoadedMsg{})
	listed := findMsg[dockerListedMsg](t, cmd)
	m, _ = wizardUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.step != jumphostStepType {
		t.Fatalf("step after esc = %v", m.step)
	}
	m, _ = wizardUpdate(t, m, listed)
	if m.step != jumphostStepType || m.openDocker != "" {
		t.Fatalf("step = %v openDocker = %q", m.step, m.openDocker)
	}
}

func TestRejectedDockerPasswordWithoutDockerSourceKeepsSessions(t *testing.T) {
	m := newSSMModel(context.Background(), newFakeService(), fakeSessionRunner{}, jumphost.Options{})
	m.sessions = []sessions.Session{{ID: 7, ResourceType: sessions.DockerResourceType, Target: "pg", State: sessions.StateEnded}}
	m.mode, m.selected = ssmModeSessions, 0
	next, _ := m.askDirectPasswordAgain(sessions.Event{Kind: sessions.EventPasswordRejected, ID: 7, Name: "pg", User: "postgres"})
	got, ok := next.(ssmModel)
	if !ok || got.mode != ssmModeSessions || got.errorMsg == "" {
		t.Fatalf("model = %T mode %v error %q", next, got.mode, got.errorMsg)
	}
}
