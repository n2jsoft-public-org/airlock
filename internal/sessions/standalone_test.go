package sessions_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

type openResult struct {
	opened *sessions.Standalone
	err    error
}

func openStandalone(runner *sessionstest.Runner, launcher *sessionstest.Launcher, name string, timeout time.Duration) <-chan openResult {
	done := make(chan openResult, 1)
	go func() {
		opened, err := sessions.OpenStandalone(context.Background(), runner, launcher.Launch, &jumphost.Options{Name: name}, timeout)
		done <- openResult{opened, err}
	}()
	return done
}

func TestOpenStandaloneWaitsForTheTunnelAndClosesIt(t *testing.T) {
	runner, launcher := sessionstest.NewRunner(), sessionstest.NewLauncher()
	runner.AWS("db", "rds").SignIn = &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "AWS IAM", User: "app"}}}
	done := openStandalone(runner, launcher, "db", time.Minute)
	process := launcher.Next(t)
	process.Print("Starting session with SessionId: abc")
	process.Print("Waiting for connections...")
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	if got := result.opened.Client.Address.Port; got != 15432 {
		t.Fatalf("client port = %d", got)
	}
	if process.Terminated() {
		t.Fatal("the tunnel must run until Close")
	}
	result.opened.Close()
	result.opened.Close()
	if !process.Terminated() {
		t.Fatal("Close must stop the tunnel")
	}
}

func TestOpenStandaloneReportsATunnelEndingEarly(t *testing.T) {
	runner, launcher := sessionstest.NewRunner(), sessionstest.NewLauncher()
	runner.AWS("db", "rds")
	done := openStandalone(runner, launcher, "db", time.Minute)
	process := launcher.Next(t)
	process.Print("An error occurred (TargetNotConnected)")
	process.Exit(errors.New("exit status 254"))
	result := <-done
	if result.err == nil || !strings.Contains(result.err.Error(), "TargetNotConnected") || !strings.Contains(result.err.Error(), "exit status 254") {
		t.Fatalf("err = %v", result.err)
	}
}

func TestOpenStandaloneStopsTheTunnelAfterTheTimeout(t *testing.T) {
	runner, launcher := sessionstest.NewRunner(), sessionstest.NewLauncher()
	runner.AWS("db", "rds")
	done := openStandalone(runner, launcher, "db", 50*time.Millisecond)
	process := launcher.Next(t)
	result := <-done
	if result.err == nil || !strings.Contains(result.err.Error(), "not ready after 50ms") {
		t.Fatalf("err = %v", result.err)
	}
	if !process.Terminated() {
		t.Fatal("a tunnel that never listened must be stopped")
	}
}

func TestOpenStandaloneRefusesSeveralCredentials(t *testing.T) {
	runner, launcher := sessionstest.NewRunner(), sessionstest.NewLauncher()
	runner.AWS("db", "rds").SignIn = &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "billing"}, {Label: "orders"}}}
	done := openStandalone(runner, launcher, "db", time.Minute)
	process := launcher.Next(t)
	process.Print("Waiting for connections...")
	result := <-done
	if result.err == nil || !strings.Contains(result.err.Error(), "billing, orders") {
		t.Fatalf("err = %v", result.err)
	}
	if !process.Terminated() {
		t.Fatal("a failed sign-in must stop the tunnel")
	}
}

func TestOpenStandaloneChecksADirectSignInWithoutTunnel(t *testing.T) {
	runner, launcher := sessionstest.NewRunner(), sessionstest.NewLauncher()
	rejected := errors.New("password rejected")
	runner.Direct("billing").SignIn = &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "password", User: "app"}}, failures: map[string]error{"password": rejected}}
	result := <-openStandalone(runner, launcher, "billing", time.Minute)
	if !errors.Is(result.err, rejected) {
		t.Fatalf("err = %v, want the rejection", result.err)
	}
	if launcher.Pending() != 0 {
		t.Fatal("a direct database runs no tunnel")
	}
}
