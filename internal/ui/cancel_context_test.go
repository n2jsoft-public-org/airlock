package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
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
	m := newSSMModel(context.Background(), runner, newProcessTracker(), jumphost.Options{})
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, cmd := update(t, m, jumphostWizardDoneMsg{options: jumphost.Options{ResourceType: "rds", Name: "db", LocalPortNumber: 5432}})
	runInBackground(cmd)
	_, _ = update(t, m, key("esc"))
	wantCanceled(t, runner)
}

func TestClosingDuringTheReconnectionCancelsIt(t *testing.T) {
	runner := newBlockingRunner()
	m := newSSMModel(context.Background(), runner, newProcessTracker(), jumphost.Options{})
	m = m.addSession(&jumphost.Session{Label: "db", Target: "db", ResourceType: "rds", Profile: "dev", Region: "eu-west-3", LocalPort: 5432}, jumphost.Options{})
	m.sessions[0].state, m.sessions[0].ready = ssmSessionRunning, true
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = update(t, m, ssmFinishedMsg{id: m.sessions[0].id})

	m, cmd := update(t, m, ssmReconnectMsg{id: m.sessions[0].id})
	runInBackground(cmd)
	_, _ = update(t, m, key("x"))
	wantCanceled(t, runner)
}

// installEKSAWS puts an aws executable on PATH answering what Prepare asks for an EKS cluster.
func installEKSAWS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cluster := `{"name":"platform","endpoint":"https://abcd.gr7.eu-west-3.eks.amazonaws.com","certificateAuthority":{"data":"LS0tLS1CRUdJTg=="}}`
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*get-caller-identity*) echo arn:aws:iam::111111111111:user/dev ;;\n" +
		"*describe-instances*) echo i-0123 ;;\n" +
		"*describe-cluster*) echo '" + cluster + "' ;;\n" +
		"esac\n"
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestCanceledPreparationReleasesItsSession(t *testing.T) {
	installEKSAWS(t)
	kubeDir := t.TempDir()
	runner := jumphost.NewRunner(jumphost.RunnerOptions{Out: io.Discard, Err: io.Discard, KubeconfigDir: kubeDir})
	m := newSSMModel(context.Background(), runner, newProcessTracker(), jumphost.Options{})
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, cmd := update(t, m, jumphostWizardDoneMsg{options: jumphost.Options{
		ResourceType: "eks", Name: "platform", AwsProfile: "dev", AwsRegion: "eu-west-3", LocalPortNumber: 15443, SkipIDEDataSource: true,
	}})
	late := preparedMsg(t, cmd) // the preparation ends after the user canceled it
	if late.err != nil || late.session.Details.Kubeconfig == "" {
		t.Fatalf("err = %v kubeconfig = %q, want a prepared EKS session", late.err, late.session.Details.Kubeconfig)
	}

	m, _ = update(t, m, key("esc"))
	m, _ = update(t, m, late)
	if len(m.sessions) != 0 {
		t.Fatal("a canceled preparation opened its session")
	}
	if _, err := os.Stat(late.session.Details.Kubeconfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat = %v: the canceled session was not released (its kubeconfig is left)", err)
	}
}
