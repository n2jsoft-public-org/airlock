package ui

import (
	"context"
	"strconv"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// fakeService records the calls of the dashboard and lets the test send events.
type fakeService struct {
	openErr  error
	credsErr error
	// resolveErr fails ResolveApproval.
	resolveErr error
	// resolveInteractive records, per ResolveApproval, whether a sign-in function was passed.
	resolveInteractive []bool
	// subscribeErr fails Subscribe; chooseErr fails ChooseSignIn.
	subscribeErr error
	chooseErr    error
	events       chan sessions.Event
	// openCtx is the context of the last Open.
	openCtx  context.Context
	opened   []sessions.OpenRequest
	calls    []string
	creds    []jumphost.DBCredentials
	snapshot sessions.Snapshot
	nextID   sessions.ID
	mu       sync.Mutex
}

func newFakeService(list ...sessions.Session) *fakeService {
	return &fakeService{events: make(chan sessions.Event, 64), snapshot: sessions.Snapshot{Sessions: list}, nextID: 100}
}

func (f *fakeService) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeService) Subscribe(context.Context) (sessions.Snapshot, <-chan sessions.Event, error) {
	if f.subscribeErr != nil {
		return sessions.Snapshot{}, nil, f.subscribeErr
	}
	return f.snapshot, f.events, nil
}
func (f *fakeService) Snapshot(context.Context) (sessions.Snapshot, error) { return f.snapshot, nil }
func (f *fakeService) Open(ctx context.Context, req sessions.OpenRequest) (sessions.ID, error) { //nolint:gocritic // signature fixed by sessions.Service.
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openCtx = ctx
	f.opened = append(f.opened, req)
	f.nextID++
	return f.nextID, f.openErr
}
func (f *fakeService) Stop(_ context.Context, id sessions.ID) error {
	f.record("stop " + itoa(id))
	return nil
}
func (f *fakeService) Remove(_ context.Context, id sessions.ID) error {
	f.record("remove " + itoa(id))
	return nil
}
func (f *fakeService) Restart(_ context.Context, id sessions.ID, _ jumphost.InteractiveFunc) error {
	f.record("restart " + itoa(id))
	return nil
}
func (f *fakeService) SignInAWS(_ context.Context, id sessions.ID, _ jumphost.InteractiveFunc) error {
	f.record("aws-signin " + itoa(id))
	return nil
}
func (f *fakeService) ChooseSignIn(_ context.Context, id sessions.ID, index int) error {
	f.record("choose " + itoa(id) + " " + itoa(sessions.ID(index)))
	return f.chooseErr
}
func (f *fakeService) SkipSignIn(_ context.Context, id sessions.ID) error {
	f.record("skip " + itoa(id))
	return nil
}
func (f *fakeService) RetrySignIn(_ context.Context, id sessions.ID) error {
	f.record("retry " + itoa(id))
	return nil
}
func (f *fakeService) Credentials(context.Context, sessions.ID) ([]jumphost.DBCredentials, error) {
	f.record("credentials")
	return f.creds, f.credsErr
}
func (f *fakeService) StopAll(context.Context) error { f.record("stop-all"); return nil }

func (f *fakeService) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func itoa(id sessions.ID) string { return strconv.Itoa(int(id)) }

// testSession is a running rds session id named target.
func testSession(id sessions.ID, target string) sessions.Session {
	return sessions.Session{ID: id, Label: "RDS " + target, ResourceType: "rds", Target: target, Profile: "dev",
		Region: "eu-west-3", LocalPort: 15432, State: sessions.StateRunning, Ready: true,
		Lines: []sessions.LogLine{{Text: "Connecting to " + target}}}
}

// newServiceModel returns a dashboard over service, sized 120x40, showing its sessions.
func newServiceModel(t *testing.T, service *fakeService) ssmModel {
	t.Helper()
	m := newSSMModel(t.Context(), service, fakeSessionRunner{}, jumphost.Options{})
	m = m.subscribe()
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	return m
}

// newSessionsModel returns a dashboard over a fake service running a session per target, with
// IDs from 1 and local ports from 15432, the last one selected.
func newSessionsModel(t *testing.T, targets ...string) (ssmModel, *fakeService) {
	t.Helper()
	list := make([]sessions.Session, 0, len(targets))
	for idx, target := range targets {
		s := testSession(sessions.ID(idx+1), target)
		s.LocalPort += idx
		list = append(list, s)
	}
	service := newFakeService(list...)
	m := newServiceModel(t, service)
	return m.selectSession(len(targets) - 1), service
}

// runServiceCmd runs cmd and the commands it batches, ignoring the messages they return. (The
// brief's runCmd: that name is the wizard tests' helper.) A command still running after 300ms,
// like the wait for the next event, is left behind.
func runServiceCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := runWithTimeout(cmd).(tea.BatchMsg); ok {
		for _, c := range batch {
			runServiceCmd(c)
		}
	}
}

// sendEvent feeds ev to m, numbered after the last event m applied.
func sendEvent(t *testing.T, m ssmModel, ev sessions.Event) (ssmModel, tea.Cmd) {
	t.Helper()
	ev.Seq = m.seq + 1
	return update(t, m, ssmEventMsg{ok: true, event: ev})
}

// sendUpdate feeds m the session id changed by change, as the service's EventUpdated.
func sendUpdate(t *testing.T, m ssmModel, id sessions.ID, change func(s *sessions.Session)) (ssmModel, tea.Cmd) {
	t.Helper()
	s := m.session(id)
	if s == nil {
		t.Fatalf("no session %d", id)
	}
	c := s.Clone()
	change(&c)
	c.Lines = nil
	return sendEvent(t, m, sessions.Event{Kind: sessions.EventUpdated, ID: id, Session: &c})
}

// ended marks a session ended on err ("" when none).
func ended(err string) func(s *sessions.Session) {
	return func(s *sessions.Session) {
		s.State, s.Err = sessions.StateEnded, err
	}
}

func (f *fakeService) ResolveApproval(_ context.Context, id sessions.ApprovalID, approve bool, interactive jumphost.InteractiveFunc) error {
	f.mu.Lock()
	f.resolveInteractive = append(f.resolveInteractive, interactive != nil)
	f.mu.Unlock()
	verb := "deny"
	if approve {
		verb = "approve"
	}
	f.record(verb + " " + strconv.FormatUint(uint64(id), 10))
	return f.resolveErr
}
