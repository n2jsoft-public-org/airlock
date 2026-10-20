package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/dockerdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// fakeDockerContainers lists list, or fails with err; calls counts the listings.
type fakeDockerContainers struct {
	err   error
	list  []dockerdb.Container
	calls int
}

func (f *fakeDockerContainers) List(context.Context) ([]dockerdb.Container, error) {
	f.calls++
	return f.list, f.err
}

func (f *fakeDockerContainers) Get(ctx context.Context, name string) (dockerdb.Container, bool, error) {
	list, err := f.List(ctx)
	for idx := range list {
		if list[idx].Name == name {
			return list[idx], true, err
		}
	}
	return dockerdb.Container{}, false, err
}

func testContainers() []dockerdb.Container {
	return []dockerdb.Container{
		{Name: "mssql", Image: "mcr.microsoft.com/mssql/server:2022-latest", Engine: dockerdb.EngineSQLServer, Host: "127.0.0.1", Port: 1433, User: "sa", Password: "Str0ng!pass"},
		{Name: "pg", Image: "postgres:16", Engine: dockerdb.EnginePostgres, Host: "127.0.0.1", Port: 55432, User: "postgres", Password: "secret", Database: "appdb"},
		{Name: "unpublished", Image: "postgres:16", Engine: dockerdb.EnginePostgres, User: "postgres", Database: "postgres",
			Problem: "port 5432 is not published: run the container with -p <host-port>:5432"},
	}
}

// newDockerWizard is a wizard on its Type step whose Docker card lists source (no direct store:
// the Docker card is the 6th).
func newDockerWizard(t *testing.T, source *fakeDockerContainers) jumphostWizardModel {
	t.Helper()
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.docker = source
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	return pastStart(t, m)
}

// dockerListStep picks the Docker card and delivers the listing.
func dockerListStep(t *testing.T, m jumphostWizardModel) jumphostWizardModel {
	t.Helper()
	m, cmd := wizardUpdate(t, m, key("7"))
	if m.step != jumphostStepTarget || !m.isDocker() {
		t.Fatalf("step = %v docker = %v, want the Docker list", m.step, m.isDocker())
	}
	m, _ = wizardUpdate(t, m, findMsg[dockerListedMsg](t, cmd))
	return m
}

func TestWizardOffersDockerCardOnlyWithSource(t *testing.T) {
	without := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	if len(without.typeOptions()) != 6 || strings.Contains(wizardView(without), "Docker database") {
		t.Fatalf("options = %d, want no Docker card without source", len(without.typeOptions()))
	}
	with := newDockerWizard(t, &fakeDockerContainers{})
	if len(with.typeOptions()) != 7 || !strings.Contains(wizardView(with), "Docker database") {
		t.Fatalf("options = %d, want the Docker card:\n%s", len(with.typeOptions()), wizardView(with))
	}
}

func TestDockerListLoadsInTheBackground(t *testing.T) {
	source := &fakeDockerContainers{list: testContainers()}
	m := newDockerWizard(t, source)
	m, cmd := wizardUpdate(t, m, key("7"))
	if source.calls != 0 || cmd == nil || !strings.Contains(wizardView(m), "Looking for database and cache containers") {
		t.Fatalf("calls = %d, want the listing in a command:\n%s", source.calls, wizardView(m))
	}
	m, _ = wizardUpdate(t, m, findMsg[dockerListedMsg](t, cmd))
	view := wizardView(m)
	for _, want := range []string{"pg", "postgres · 127.0.0.1:55432 · appdb · postgres:16", "port 5432 is not published", "3 containers"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view misses %q:\n%s", want, view)
		}
	}
}

func TestDockerListShowsTheDiscoveryError(t *testing.T) {
	m := dockerListStep(t, newDockerWizard(t, &fakeDockerContainers{err: dockerdb.ErrDockerNotFound}))
	if !strings.Contains(wizardView(m), "docker CLI not found") {
		t.Fatalf("view:\n%s", wizardView(m))
	}
}

func TestDockerListRefusesAContainerWithAProblem(t *testing.T) {
	m := dockerListStep(t, newDockerWizard(t, &fakeDockerContainers{list: testContainers()}))
	m.dockerList.selectName("unpublished")
	m, _ = wizardUpdate(t, m, key("enter"))
	if m.step != jumphostStepTarget || m.errorMsg != "port 5432 is not published: run the container with -p <host-port>:5432" {
		t.Fatalf("step = %v error = %q", m.step, m.errorMsg)
	}
}

func TestDockerListReloads(t *testing.T) {
	source := &fakeDockerContainers{list: testContainers()}
	m := dockerListStep(t, newDockerWizard(t, source))
	m.dockerList.selectName("pg")
	m, cmd := wizardUpdate(t, m, key("r"))
	m, _ = wizardUpdate(t, m, findMsg[dockerListedMsg](t, cmd))
	if item, ok := m.dockerList.selectedItem(); source.calls != 2 || !ok || item.c.Name != "pg" {
		t.Fatalf("calls = %d selected = %+v, want a second listing keeping pg", source.calls, item)
	}
}

func TestDockerListSwitchesToARunningSession(t *testing.T) {
	pg := testContainers()[1]
	m := newDockerWizard(t, &fakeDockerContainers{list: testContainers()})
	m.active = map[string]bool{dockerConnectionKey(&pg): true}
	m = dockerListStep(t, m)
	m.dockerList.selectName("pg")
	m, _ = wizardUpdate(t, m, key("enter"))
	if !m.canceled || m.switchTo != dockerConnectionKey(&pg) {
		t.Fatalf("canceled = %v switchTo = %q", m.canceled, m.switchTo)
	}
}

func TestDockerListPicksAContainer(t *testing.T) {
	m := dockerListStep(t, newDockerWizard(t, &fakeDockerContainers{list: testContainers()}))
	m.dockerList.selectName("pg")
	m, _ = wizardUpdate(t, m, key("enter"))
	if m.step != jumphostStepReview || m.dockerContainer.Name != "pg" {
		t.Fatalf("step = %v container = %q", m.step, m.dockerContainer.Name)
	}
}
