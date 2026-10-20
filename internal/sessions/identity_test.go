package sessions_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

// identityRunner is a runner whose AWS sign-in the test expires.
type identityRunner struct {
	*sessionstest.Runner
	// checkErr is what CheckIdentity answers; SignIn clears it.
	checkErr error
	signIns  int
	mu       sync.Mutex
}

func (r *identityRunner) CheckIdentity(context.Context, string, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checkErr
}

func (r *identityRunner) SignIn(ctx context.Context, _, _ string) error {
	if err := jumphost.RunInteractive(ctx, "test", "airlock", "sso-login"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signIns++
	r.checkErr = nil
	return nil
}

func (r *identityRunner) expire(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkErr = err
}

func newIdentityManager(t *testing.T) (*sessions.Manager, *identityRunner, *sessionstest.Launcher, *fakeTimers) {
	t.Helper()
	runner, launcher, timers := &identityRunner{Runner: sessionstest.NewRunner()}, sessionstest.NewLauncher(), &fakeTimers{}
	m := sessions.NewManager(context.Background(), sessions.Config{Runner: sessions.Fixed(runner), Launch: launcher.Launch, AfterFunc: timers.AfterFunc})
	t.Cleanup(m.Shutdown)
	return m, runner, launcher, timers
}

func expiredIs(id sessions.ID, expired bool) func(sessions.Event) bool {
	return func(ev sessions.Event) bool {
		return ev.ID == id && ev.Session != nil && ev.Session.AWSSignInExpired == expired
	}
}

func TestExpiredAWSSignInIsReportedAndRenewedWithoutRestarting(t *testing.T) {
	m, runner, launcher, timers := newIdentityManager(t)
	runner.AWS("tf-eks-dev", "eks")
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "eks", Name: "tf-eks-dev"})
	process := launcher.Next(t)
	process.Print("Waiting for connections...")
	if delays := timers.scheduled(); len(delays) != 1 || delays[0] != sessions.IdentityCheckInterval {
		t.Fatalf("scheduled = %v", delays)
	}

	runner.expire(jumphost.ErrSignInRequired)
	timers.fire()
	sessionstest.Wait(t, events, expiredIs(id, true))
	if delays := timers.scheduled(); len(delays) != 2 {
		t.Fatalf("the identity is checked again later: scheduled = %v", delays)
	}

	var ran []string
	interactive := func(_ context.Context, _, name string, args ...string) error {
		ran = append([]string{name}, args...)
		return nil
	}
	if err := m.SignInAWS(t.Context(), id, interactive); err != nil {
		t.Fatal(err)
	}
	sessionstest.Wait(t, events, expiredIs(id, false))
	if len(ran) == 0 || runner.signIns != 1 {
		t.Fatalf("sign-in ran %v, %d times", ran, runner.signIns)
	}
	if process.Terminated() || launcher.Pending() != 0 {
		t.Fatal("signing in again must keep the tunnel open")
	}
}

func TestUnreachableAWSLeavesTheSignInAlone(t *testing.T) {
	m, runner, launcher, timers := newIdentityManager(t)
	runner.AWS("tf-eks-dev", "eks")
	mustOpen(t, m, &jumphost.Options{ResourceType: "eks", Name: "tf-eks-dev"})
	launcher.Next(t)

	//goland:noinspection GoErrorStringFormat // the AWS CLI's own message, verbatim
	runner.expire(errors.New("Could not connect to the endpoint URL"))
	timers.fire()
	if s := snapshotOf(t, m).Sessions[0]; s.AWSSignInExpired {
		t.Fatal("AWS out of reach says nothing about the sign-in")
	}
}

func TestDirectSessionsCheckNoAWSIdentity(t *testing.T) {
	m, runner, _, timers := newIdentityManager(t)
	runner.Direct("orders")
	id := mustOpen(t, m, &jumphost.Options{ResourceType: sessions.DirectResourceType, Name: "orders"})
	if delays := timers.scheduled(); len(delays) != 0 {
		t.Fatalf("scheduled = %v", delays)
	}
	if err := m.SignInAWS(t.Context(), id, nil); err == nil {
		t.Fatal("a direct database has no AWS sign-in")
	}
}

func TestExpiredAWSSignInOfADynamoDBSessionNamesNoTunnel(t *testing.T) {
	m, runner, _, timers := newIdentityManager(t)
	runner.DynamoDB("eu-west-3")
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "dynamodb", Name: "eu-west-3", AwsProfile: "dev"})
	runner.expire(jumphost.ErrSignInRequired)
	timers.fire()
	sessionstest.Wait(t, events, sessionstest.Line(id, "The AWS sign-in of profile dev expired: the calls of the MCP server fail. Press r to sign in again."))
}
