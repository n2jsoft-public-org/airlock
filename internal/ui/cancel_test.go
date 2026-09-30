package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// targetsLoaded answers the discovery of a wizard with db-00 and db-01.
var targetsLoaded = jumphostTargetsLoadedMsg{targets: []jumphost.ResourceSummary{{Name: "db-00"}, {Name: "db-01"}}}

func TestWizardEscCancelsTargetDiscovery(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{AwsRegion: "eu-west-3"})

	m, late := wizardUpdate(t, m, key("enter")) // rds: the targets load
	if !m.loading || !strings.Contains(ansi.Strip(m.View()), "esc  cancel") {
		t.Fatal("the loading does not offer to cancel it")
	}
	m, _ = wizardUpdate(t, m, key("esc"))
	if m.loading || m.step != jumphostStepType {
		t.Fatalf("loading = %v step = %v, want back on the Type step", m.loading, m.step)
	}

	m = runCmd(t, m, late)
	if m.step != jumphostStepType {
		t.Fatalf("step = %v, the result of a canceled discovery must be dropped", m.step)
	}
}

func TestWizardEscCancelsPortLoadingOnTheTargets(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{AwsRegion: "eu-west-3"})
	m, _ = wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, targetsLoaded)

	m, late := wizardUpdate(t, m, key("enter")) // the port loads
	m, _ = wizardUpdate(t, m, key("esc"))
	m = runCmd(t, m, late)
	if m.loading || m.step != jumphostStepTarget || len(m.targetList.Items()) != 2 {
		t.Fatalf("loading = %v step = %v targets = %d, want the targets kept", m.loading, m.step, len(m.targetList.Items()))
	}
}

func TestWizardEscCancelsTargetReloadAfterRegionSwitch(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{AwsRegion: "eu-west-3"})
	m, _ = wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, targetsLoaded)

	m, _ = wizardUpdate(t, m, key("r"))
	for _, r := range "us-east-1" {
		m, _ = wizardUpdate(t, m, key(string(r)))
	}
	m, _ = wizardUpdate(t, m, key("enter")) // the targets of us-east-1 load
	if !m.loading {
		t.Fatal("switching region did not reload the targets")
	}
	m, _ = wizardUpdate(t, m, key("esc"))
	if m.step == jumphostStepTarget || len(m.targetList.Items()) != 0 {
		t.Fatalf("step = %v targets = %d: the eu-west-3 targets must not be offered for us-east-1", m.step, len(m.targetList.Items()))
	}
}

func TestWizardEscCancelsProfileLoading(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pastStart(t, m)

	m, late := wizardUpdate(t, m, key("enter")) // rds, then the profiles load
	m, _ = wizardUpdate(t, m, key("esc"))
	m = runCmd(t, m, late)
	if m.loading || m.step != jumphostStepType {
		t.Fatalf("loading = %v step = %v, want back on the Type step", m.loading, m.step)
	}
}

func TestWizardEscCancelsRecentLookup(t *testing.T) {
	m := startWizard(t, newHistoryRunner(savedArchive))

	m, late := wizardUpdate(t, m, key("enter")) // the recent target is looked up
	m, _ = wizardUpdate(t, m, key("esc"))
	m = runCmd(t, m, late)
	if m.loading || m.step != jumphostStepStart || m.flow != flowStart || m.options.Name != "" {
		t.Fatalf("loading = %v step = %v flow = %v name = %q, want the start screen back", m.loading, m.step, m.flow, m.options.Name)
	}
}

func TestWizardHistoryLoadingIsNotCanceled(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), newHistoryRunner(savedArchive), jumphost.Options{})
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, _ = wizardUpdate(t, m, key("esc"))
	if !m.loading || strings.Contains(ansi.Strip(m.View()), "esc  cancel") {
		t.Fatal("the history loading, a local file read, must not be canceled")
	}
}

func TestDashboardEscCancelsPreparation(t *testing.T) {
	prepared := &[]jumphost.Options{}
	m := newSSMModel(context.Background(), prepareRecorder{prepared: prepared}, newProcessTracker(), jumphost.Options{})
	m = m.addSession(&jumphost.Session{Label: "db", Target: "db", ResourceType: "rds", Profile: "dev", Region: "eu-west-3"}, jumphost.Options{})
	m.sessions[0].state = ssmSessionRunning
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, _ = update(t, m, key("n"))
	m, cmd := update(t, m, jumphostWizardDoneMsg{options: jumphost.Options{ResourceType: "rds", Name: "other", LocalPortNumber: 5440}})
	if !strings.Contains(plainView(m), "esc") {
		t.Fatal("the preparation does not offer to cancel it")
	}
	late := preparedMsg(t, cmd)

	m, _ = update(t, m, key("esc"))
	if m.mode != ssmModeSessions {
		t.Fatalf("mode = %v, want back to the sessions", m.mode)
	}
	m, _ = update(t, m, late)
	if len(m.sessions) != 1 {
		t.Fatalf("sessions = %d, a canceled preparation must not add its session", len(m.sessions))
	}
}
