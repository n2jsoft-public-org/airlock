package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// blockingRunner answers the target discovery and the preparation only once their context ends,
// and reports that end on canceled.
type blockingRunner struct {
	fakeSessionRunner
	canceled chan error
}

func newBlockingRunner() blockingRunner {
	return blockingRunner{canceled: make(chan error, 4)}
}

func (r blockingRunner) ListResourceSummaries(ctx context.Context, _ *jumphost.Options) ([]jumphost.ResourceSummary, error) {
	<-ctx.Done()
	r.canceled <- ctx.Err()
	return nil, ctx.Err()
}

func (r blockingRunner) Prepare(ctx context.Context, _ jumphost.Options) (*jumphost.Session, error) {
	<-ctx.Done()
	r.canceled <- ctx.Err()
	return nil, ctx.Err()
}

// runInBackground starts every command of cmd, a batch or not, without waiting for them.
func runInBackground(cmd tea.Cmd) {
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, inner := range batch {
				if inner != nil {
					go inner()
				}
			}
		}
	}()
}

// wantCanceled waits for runner to see its context canceled.
func wantCanceled(t *testing.T, runner blockingRunner) {
	t.Helper()
	select {
	case err := <-runner.canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the AWS call was not canceled: it goes on in the background")
	}
}

func TestWizardEscCancelsTheAWSCall(t *testing.T) {
	runner := newBlockingRunner()
	m := newTestWizard(t, runner, jumphost.Options{AwsRegion: "eu-west-3"})

	m, cmd := wizardUpdate(t, m, key("enter")) // rds: the targets load
	runInBackground(cmd)
	_, _ = wizardUpdate(t, m, key("esc"))
	wantCanceled(t, runner)
}

func TestDashboardEscCancelsThePreparation(t *testing.T) {
	runner := newBlockingRunner()
	manager := sessions.NewManager(t.Context(), sessions.Config{Runner: sessions.Fixed(runner)})
	t.Cleanup(manager.Shutdown)
	m := newSSMModel(t.Context(), manager, runner, jumphost.Options{}).subscribe()
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, cmd := update(t, m, jumphostWizardDoneMsg{options: jumphost.Options{ResourceType: "rds", Name: "db", LocalPortNumber: 5432}})
	runInBackground(cmd)
	_, _ = update(t, m, key("esc"))
	wantCanceled(t, runner)
}
