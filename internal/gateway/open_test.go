package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

type openReply struct {
	Tools   string `json:"tools"`
	Session int    `json:"session"`
	Reused  bool   `json:"reused"`
	Writes  bool   `json:"writes"`
}

func TestOpenSessionAuto(t *testing.T) {
	runner := sessionstest.NewRunner()
	runner.Direct("billing")
	m, _ := newTestManager(t, runner)
	cs, events := newToolset(t, m, Settings{AutoConnect: AutoConnectAll}, &fakeDiscovery{}, fakeDatabases{"billing": {Name: "billing"}})
	text, isError := callTool(t, cs, "open_session", map[string]any{"type": "direct", "name": "billing", "reason": "check invoices"})
	if isError {
		t.Fatal(text)
	}
	if reply := decodeText[openReply](t, text); reply.Session == 0 || reply.Reused {
		t.Fatalf("reply = %+v", reply)
	}
	prepared := runner.PreparedOptions()
	if len(prepared) != 1 {
		t.Fatalf("prepared = %+v", prepared)
	}
	o := prepared[0]
	if o.OpenedBy != "agent" || !o.SkipMCPRegistration || !o.SkipIDEDataSource || !o.Unattended || o.MCPAllowWrites {
		t.Fatalf("options = %+v", o)
	}
	if len(*events) != 1 || (*events)[0].Tool != "open_session" || (*events)[0].Decision != "auto" {
		t.Fatalf("audit = %+v", *events)
	}
	text, isError = callTool(t, cs, "open_session", map[string]any{"type": "direct", "name": "billing"})
	if reply := decodeText[openReply](t, text); isError || !reply.Reused || len(runner.PreparedOptions()) != 1 {
		t.Fatalf("second opening = %q", text)
	}
}

func TestOpenSessionRefusals(t *testing.T) {
	runner := sessionstest.NewRunner()
	runner.Direct("billing")
	m, _ := newTestManager(t, runner)
	d := &fakeDiscovery{saved: []jumphost.SavedConnection{savedRDS("orders-db", "dev"), savedRDS("orders-db", "prod")}}
	cs, _ := newToolset(t, m, Settings{AutoConnect: AutoConnectNone}, d, fakeDatabases{"billing": {Name: "billing"}})
	cases := []struct {
		args map[string]any
		want string
	}{
		{args: map[string]any{"type": "direct", "name": "billing", "writes": true}, want: "agent writes are off"},
		{args: map[string]any{"type": "direct", "name": "billing"}, want: "no airlock dashboard is open"},
		{args: map[string]any{"type": "rds", "name": "orders-db"}, want: "several profiles (dev, prod)"},
		{args: map[string]any{"type": "rds", "name": "unknown-db"}, want: "pass its profile"},
		{args: map[string]any{"type": "direct", "name": "nope"}, want: "no direct database named"},
		{args: map[string]any{"type": "kafka", "name": "bucket"}, want: "unknown type"},
	}
	for _, tc := range cases {
		if text, isError := callTool(t, cs, "open_session", tc.args); !isError || !strings.Contains(text, tc.want) {
			t.Errorf("%v = %q, want %q", tc.args, text, tc.want)
		}
	}
	if len(runner.PreparedOptions()) != 0 {
		t.Fatal("a refused opening prepares nothing")
	}
}

func TestOpenSessionApprovedInADashboard(t *testing.T) {
	runner := sessionstest.NewRunner()
	runner.Direct("billing")
	var ranInView atomic.Bool
	runner.OnPrepare = func(ctx context.Context) error {
		return jumphost.RunInteractive(ctx, "sign in", "airlock", "sso-login")
	}
	m, _ := newTestManager(t, runner)
	m.ViewsChanged(1)
	_, events, err := m.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cs, audit := newToolset(t, m, Settings{AutoConnect: AutoConnectNone}, &fakeDiscovery{}, fakeDatabases{"billing": {Name: "billing", Environment: "prod"}})
	type result struct {
		text    string
		isError bool
	}
	done := make(chan result, 1)
	go func() {
		text, isError := callTool(t, cs, "open_session", map[string]any{"type": "direct", "name": "billing", "reason": "check invoices"})
		done <- result{text, isError}
	}()
	ev := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventApprovalRequested })
	if a := ev.Approval; a.Agent != "agent" || a.Target != "billing" || a.Reason != "check invoices" || !a.Production || a.Environment != "prod" || a.Writes {
		t.Fatalf("approval = %+v", a)
	}
	go func() {
		if rerr := m.ResolveApproval(context.Background(), ev.Approval.ID, true, func(context.Context, string, string, ...string) error {
			ranInView.Store(true)
			return nil
		}); rerr != nil {
			t.Error(rerr)
		}
	}()
	got := <-done
	if got.isError {
		t.Fatal(got.text)
	}
	if !ranInView.Load() || runner.PreparedOptions()[0].Unattended {
		t.Fatalf("an approved opening signs in on the approving view's terminal: ran=%v options=%+v", ranInView.Load(), runner.PreparedOptions()[0])
	}
	if (*audit)[0].Decision != "approved" {
		t.Fatalf("audit = %+v", *audit)
	}
}

func TestOpenSessionDenied(t *testing.T) {
	runner := sessionstest.NewRunner()
	runner.Direct("billing")
	m, _ := newTestManager(t, runner)
	m.ViewsChanged(1)
	_, events, err := m.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := newToolset(t, m, Settings{}, &fakeDiscovery{}, fakeDatabases{"billing": {Name: "billing"}})
	go func() {
		ev := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventApprovalRequested })
		if rerr := m.ResolveApproval(context.Background(), ev.Approval.ID, false, nil); rerr != nil {
			t.Error(rerr)
		}
	}()
	if text, isError := callTool(t, cs, "open_session", map[string]any{"type": "direct", "name": "billing"}); !isError || !strings.Contains(text, "denied") {
		t.Fatalf("denied = %q", text)
	}
}

// Review Focus 1: an auto opening whose profile needs SSO never signs in in the daemon.
func TestOpenSessionNeedingSSOWithoutView(t *testing.T) {
	runner := sessionstest.NewRunner()
	runner.Errors["orders-db"] = jumphost.ErrSignInRequired
	m, _ := newTestManager(t, runner)
	cs, _ := newToolset(t, m, Settings{AutoConnect: AutoConnectAll}, &fakeDiscovery{saved: []jumphost.SavedConnection{savedRDS("orders-db", "dev")}}, nil)
	text, isError := callTool(t, cs, "open_session", map[string]any{"type": "rds", "name": "orders-db"})
	if !isError || !strings.Contains(text, "sign in to profile dev again from airlock") {
		t.Fatalf("sso = %q", text)
	}
	if o := runner.PreparedOptions()[0]; !o.Unattended || o.AwsProfile != "dev" || o.AwsRegion != "eu-west-3" {
		t.Fatalf("options = %+v", o)
	}
}

func TestOpenSessionUsesTheKeychainPassword(t *testing.T) {
	runner := sessionstest.NewRunner()
	runner.AWS("orders-db", "rds")
	m, launcher := newTestManager(t, runner)
	conn := savedRDS("orders-db", "dev")
	conn.Prefs.SignIn, conn.Prefs.DBUser, conn.Prefs.PasswordInKeychain = jumphost.SignInPassword, "app_ro", true
	d := &fakeDiscovery{saved: []jumphost.SavedConnection{conn}, passwords: map[string]string{"orders-db|app_ro": "s3cret"}}
	cs, _ := newToolset(t, m, Settings{AutoConnect: AutoConnectAll}, d, nil)
	go func() { launcher.Next(t).Print("Waiting for connections...") }()
	if text, isError := callTool(t, cs, "open_session", map[string]any{"type": "rds", "name": "orders-db"}); isError {
		t.Fatal(text)
	}
	if o := runner.PreparedOptions()[0]; o.DBPassword != "s3cret" || o.DBUser != "app_ro" || o.SignIn != jumphost.SignInPassword {
		t.Fatalf("options = %+v", o)
	}
}

// F3: a password kept in the keychain that does not load is most likely an expired AWS sign-in
// (the keychain entry is keyed by the account); a connection without one needs a dashboard opening.
func TestOpenSessionWithoutKeychainPassword(t *testing.T) {
	cases := []struct {
		want     string
		keychain bool
	}{
		{keychain: true, want: "AWS sign-in required (or the OS keychain did not answer): ask the user to sign in to profile dev again from airlock, then retry"},
		{keychain: false, want: "no saved password for this connection"},
	}
	for _, tc := range cases {
		runner := sessionstest.NewRunner()
		runner.AWS("orders-db", "rds")
		m, _ := newTestManager(t, runner)
		conn := savedRDS("orders-db", "dev")
		conn.Prefs.SignIn, conn.Prefs.DBUser, conn.Prefs.PasswordInKeychain = jumphost.SignInPassword, "app_ro", tc.keychain
		cs, _ := newToolset(t, m, Settings{AutoConnect: AutoConnectAll}, &fakeDiscovery{saved: []jumphost.SavedConnection{conn}}, nil)
		if text, isError := callTool(t, cs, "open_session", map[string]any{"type": "rds", "name": "orders-db"}); !isError || !strings.Contains(text, tc.want) {
			t.Errorf("keychain %v: missing password = %q, want %q", tc.keychain, text, tc.want)
		}
		if len(runner.PreparedOptions()) != 0 {
			t.Fatal("nothing is prepared without the password")
		}
	}
}

func TestCloseSession(t *testing.T) {
	runner := sessionstest.NewRunner()
	m, _ := newTestManager(t, runner)
	mine := openDirect(t, m, runner, "mine", nil, false, "")
	agents := openDirect(t, m, runner, "agents", nil, false, "claude-code")
	cs, _ := newToolset(t, m, Settings{}, &fakeDiscovery{}, nil)
	if text, isError := callTool(t, cs, "close_session", map[string]any{"session": int(mine)}); !isError || !strings.Contains(text, "opened by the user") {
		t.Fatalf("closing a user session = %q", text)
	}
	// M3: a quoted id works, as with the relayed tools.
	if text, isError := callTool(t, cs, "close_session", map[string]any{"session": itoa(agents)}); isError {
		t.Fatal(text)
	}
	if text, isError := callTool(t, cs, "close_session", map[string]any{"session": "x"}); !isError || !strings.Contains(text, "session is required") {
		t.Fatalf("closing session x = %q", text)
	}
	snapshot, err := m.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snapshot.Sessions {
		if s.ID == agents && s.Active() {
			t.Fatal("the agent's session must be stopped")
		}
		if s.ID == mine && !s.Active() {
			t.Fatal("the user's session must keep running")
		}
	}
}

// F1: the agent's text reaches the dashboard and the logs without terminal control sequences.
func TestOpenSessionCleansTheAgentText(t *testing.T) {
	m, _ := newTestManager(t, sessionstest.NewRunner())
	m.ViewsChanged(1)
	_, events, err := m.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cs, audit := newToolset(t, m, Settings{}, &fakeDiscovery{}, nil)
	go func() {
		ev := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventApprovalRequested })
		a := ev.Approval
		for _, field := range []string{a.Reason, a.Target, a.Profile, a.Region} {
			if strings.ContainsAny(field, "\x1b\n\r\a") {
				t.Errorf("approval = %+v, want no control characters", a)
			}
		}
		if a.Reason != "check [2K[1Ainvoices now" || a.Target != "orders-db]0;owned" || a.Profile != "dev" {
			t.Errorf("approval = %+v", a)
		}
		if rerr := m.ResolveApproval(context.Background(), a.ID, false, nil); rerr != nil {
			t.Error(rerr)
		}
	}()
	text, isError := callTool(t, cs, "open_session", map[string]any{"type": "rds", "name": "orders-db\x1b]0;owned\a",
		"profile": "dev\n", "region": "eu-west-3", "reason": "check \x1b[2K\x1b[1Ainvoices\nnow"})
	if !isError || !strings.Contains(text, "denied") {
		t.Fatalf("open = %q", text)
	}
	if got := (*audit)[0]; strings.ContainsAny(got.Target+got.Profile+got.Decision, "\x1b\a") {
		t.Fatalf("audit = %+v", got)
	}
}

// F4: agentAutoConnect non-prod still asks for a target of a production profile, and refuses it
// with no dashboard to ask.
func TestOpenSessionNonProdPromptsForAProductionProfile(t *testing.T) {
	runner := sessionstest.NewRunner()
	m, _ := newTestManager(t, runner)
	settings := Settings{AutoConnect: AutoConnectNonProd, Environments: mustEnvironments(t, "prod!=prod*")}
	cs, _ := newToolset(t, m, settings, &fakeDiscovery{saved: []jumphost.SavedConnection{savedRDS("orders-db", "prod-eu")}}, nil)
	args := map[string]any{"type": "rds", "name": "orders-db"}
	if text, isError := callTool(t, cs, "open_session", args); !isError || !strings.Contains(text, "no airlock dashboard is open") {
		t.Fatalf("without a view = %q", text)
	}
	m.ViewsChanged(1)
	_, events, err := m.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		ev := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventApprovalRequested })
		if a := ev.Approval; !a.Production || a.Profile != "prod-eu" || a.Target != "orders-db" {
			t.Errorf("approval = %+v", a)
		}
		if rerr := m.ResolveApproval(context.Background(), ev.Approval.ID, false, nil); rerr != nil {
			t.Error(rerr)
		}
	}()
	if text, isError := callTool(t, cs, "open_session", args); !isError || !strings.Contains(text, "denied") {
		t.Fatalf("with a view = %q", text)
	}
	if len(runner.PreparedOptions()) != 0 {
		t.Fatal("nothing opens without the approval")
	}
}

// F4: asking for writes on a target running read-only is refused, even with agentWrites on.
func TestOpenSessionWritesOnARunningReadOnlySession(t *testing.T) {
	runner := sessionstest.NewRunner()
	m, _ := newTestManager(t, runner)
	id := openDirect(t, m, runner, "billing", dbmcp.NewServer(&dbmcp.ServerConfig{Name: "airlock-billing", Target: "billing"}, &fakeQuerier{}), false, "")
	m.ViewsChanged(1)
	cs, _ := newToolset(t, m, Settings{AutoConnect: AutoConnectAll, AgentWrites: true}, &fakeDiscovery{}, fakeDatabases{"billing": {Name: "billing"}})
	text, isError := callTool(t, cs, "open_session", map[string]any{"type": "direct", "name": "billing", "writes": true})
	if want := fmt.Sprintf("session %d runs billing read-only", id); !isError || !strings.Contains(text, want) {
		t.Fatalf("writes on a read-only session = %q, want %q", text, want)
	}
	if len(runner.PreparedOptions()) != 1 {
		t.Fatalf("prepared = %+v, want the user's opening only", runner.PreparedOptions())
	}
}

// F4: an approved opening failing before it opens releases the approving view at once.
func TestOpenSessionApprovedThenRefusedReleasesTheView(t *testing.T) {
	runner := sessionstest.NewRunner()
	runner.AWS("orders-db", "rds")
	m, _ := newTestManager(t, runner)
	m.ViewsChanged(1)
	_, events, err := m.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn := savedRDS("orders-db", "dev")
	conn.Prefs.SignIn, conn.Prefs.DBUser = jumphost.SignInPassword, "app_ro"
	cs, audit := newToolset(t, m, Settings{}, &fakeDiscovery{saved: []jumphost.SavedConnection{conn}}, nil)
	resolved := make(chan error, 1)
	go func() {
		ev := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventApprovalRequested })
		resolved <- m.ResolveApproval(context.Background(), ev.Approval.ID, true, nil)
	}()
	if text, isError := callTool(t, cs, "open_session", map[string]any{"type": "rds", "name": "orders-db"}); !isError || !strings.Contains(text, "no saved password") {
		t.Fatalf("open = %q", text)
	}
	select {
	case rerr := <-resolved:
		if rerr != nil {
			t.Fatal(rerr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the approving view must be released when the opening fails")
	}
	// M1: the audit tells an approved opening that failed from a refused one.
	if got := (*audit)[0].Decision; got != "approved, then refused: "+reasonNoPassword {
		t.Fatalf("decision = %q", got)
	}
}

// failingSignIn is a sign-in check that always fails with err, like an unreachable database.
type failingSignIn struct{ err error }

func (failingSignIn) Candidates(context.Context) ([]jumphost.SignInCandidate, error) {
	return []jumphost.SignInCandidate{{Label: "password", User: "app"}}, nil
}
func (f failingSignIn) Check(context.Context, jumphost.SignInCandidate) error { return f.err }
func (failingSignIn) Use(jumphost.SignInCandidate, *jumphost.Options)         {}
func (failingSignIn) Forget(jumphost.SignInCandidate, *jumphost.Options) bool { return false }

// A database the session cannot sign in to (unreachable host, refused password) is a failed
// opening: the agent learns why, and the useless session it opened is stopped.
func TestOpenSessionFailsWhenTheSignInFails(t *testing.T) {
	runner := sessionstest.NewRunner()
	runner.Direct("billing").SignIn = failingSignIn{err: fmt.Errorf("billing.internal:5432 is not reachable (VPN or firewall?)")}
	m, _ := newTestManager(t, runner)
	cs, _ := newToolset(t, m, Settings{AutoConnect: AutoConnectAll}, &fakeDiscovery{}, fakeDatabases{"billing": {Name: "billing"}})
	text, isError := callTool(t, cs, "open_session", map[string]any{"type": "direct", "name": "billing"})
	if !isError || !strings.Contains(text, "not reachable (VPN or firewall?)") {
		t.Fatalf("isError = %v, text = %q", isError, text)
	}
	snapshot, err := m.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snapshot.Sessions {
		if s.Active() {
			t.Fatalf("session %d still runs after its sign-in failed", s.ID)
		}
	}
}

func mustEnvironments(t *testing.T, s string) environment.Set {
	t.Helper()
	set, err := environment.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestOpenSessionNonProdPromptsForALegacyProductionDatabase(t *testing.T) {
	runner := sessionstest.NewRunner()
	runner.Direct("orders")
	m, _ := newTestManager(t, runner)
	settings := Settings{AutoConnect: AutoConnectNonProd, Environments: mustEnvironments(t, "live!=*-live; staging=*stg*")}
	cs, _ := newToolset(t, m, settings, &fakeDiscovery{}, fakeDatabases{
		"billing": {Name: "billing", Environment: "prod"},
		"orders":  {Name: "orders", Environment: "staging"},
	})
	text, isError := callTool(t, cs, "open_session", map[string]any{"type": "direct", "name": "billing"})
	if !isError || !strings.Contains(text, "no airlock dashboard is open") {
		t.Fatalf("billing = %q, want production (prod is live's alias) to need a dashboard", text)
	}
	if text, isError = callTool(t, cs, "open_session", map[string]any{"type": "direct", "name": "orders"}); isError {
		t.Fatalf("orders = %q, want staging to open alone under non-prod", text)
	}
}

func TestOpenSessionNonProdPromptsForAProfileOfAProductionNamedLive(t *testing.T) {
	runner := sessionstest.NewRunner()
	m, _ := newTestManager(t, runner)
	settings := Settings{AutoConnect: AutoConnectNonProd, Environments: mustEnvironments(t, "live!=*-live; staging=*stg*")}
	cs, _ := newToolset(t, m, settings, &fakeDiscovery{saved: []jumphost.SavedConnection{savedRDS("orders-db", "shop-live")}}, nil)
	text, isError := callTool(t, cs, "open_session", map[string]any{"type": "rds", "name": "orders-db"})
	if !isError || !strings.Contains(text, "no airlock dashboard is open") {
		t.Fatalf("shop-live = %q, want production (live) to need a dashboard", text)
	}
	if len(runner.PreparedOptions()) != 0 {
		t.Fatal("nothing opens without the approval")
	}
}

func TestOpenSessionNonProdOpensAStagingProfileAlone(t *testing.T) {
	runner := sessionstest.NewRunner()
	runner.AWS("orders-db", "rds")
	m, _ := newTestManager(t, runner)
	settings := Settings{AutoConnect: AutoConnectNonProd, Environments: mustEnvironments(t, "live!=*-live; staging=*stg*")}
	cs, _ := newToolset(t, m, settings, &fakeDiscovery{saved: []jumphost.SavedConnection{savedRDS("orders-db", "shop-stg")}}, nil)
	// The fake AWS session never reports running, so the call may time out waiting for it: what
	// matters is that it went ahead without a dashboard.
	text, _ := callTool(t, cs, "open_session", map[string]any{"type": "rds", "name": "orders-db"})
	if strings.Contains(text, "dashboard") {
		t.Fatalf("shop-stg = %q, want staging to open alone under non-prod", text)
	}
	if len(runner.PreparedOptions()) != 1 {
		t.Fatalf("prepared = %+v", runner.PreparedOptions())
	}
}

func TestRunningFindsTheS3SessionOfTheProfileInAnyRegion(t *testing.T) {
	runner := sessionstest.NewRunner()
	runner.S3()
	m, _ := newTestManager(t, runner)
	if _, err := m.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: sessions.S3ResourceType, Name: "buckets", AwsProfile: "dev", AwsRegion: "eu-west-3"}}); err != nil {
		t.Fatal(err)
	}
	tools := &toolset{manager: m}
	if tools.running(t.Context(), &target{typ: sessions.S3ResourceType, name: "buckets", profile: "dev", region: "us-east-1"}) == nil {
		t.Fatal("the S3 session of dev must be reused from another region")
	}
	if tools.running(t.Context(), &target{typ: sessions.S3ResourceType, name: "buckets", profile: "prod", region: "eu-west-3"}) != nil {
		t.Fatal("the S3 session of dev must not serve prod")
	}
}
