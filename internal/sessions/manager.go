package sessions

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// subscriberBuffer is how many events a subscriber may lag behind before it gets EventResync.
const subscriberBuffer = 1024

// Runner prepares the sessions; *jumphost.Runner implements it.
type Runner interface {
	Prepare(ctx context.Context, options jumphost.Options) (*jumphost.Session, error)
	SaveConnectionPrefs(ctx context.Context, options jumphost.Options) error
}

// RunnerSource returns the runner of the next opening: the daemon rebuilds it when config.json
// changed.
type RunnerSource func() (Runner, error)

// Fixed is the RunnerSource of a runner that never changes.
func Fixed(r Runner) RunnerSource { return func() (Runner, error) { return r, nil } }

// Config holds what a Manager depends on; zero fields take the real implementations.
type Config struct {
	Runner RunnerSource
	// Launch starts the aws processes (AWSLauncher when nil).
	Launch Launcher
	Now    func() time.Time
	// AfterFunc runs f after d, until stop is called (time.AfterFunc when nil).
	AfterFunc func(d time.Duration, f func()) (stop func() bool)
}

// Manager owns the sessions: it is the only one changing them, and tells its subscribers.
type Manager struct {
	ctx       context.Context
	runner    RunnerSource
	launch    Launcher
	now       func() time.Time
	afterFunc func(d time.Duration, f func()) func() bool
	// identityStop cancels the scheduled identity check (nil when none is).
	identityStop func() bool
	subs         map[*subscriber]struct{}
	// ended is closed, then replaced, each time a session ends (StopAll waits on it).
	ended   chan struct{}
	entries []*entry
	// approvals are the pending agent openings, oldest first; views counts the dashboards able to
	// answer them (daemon views).
	approvals    []*pendingApproval
	nextApproval ApprovalID
	views        int
	seq          uint64
	nextID       ID
	closing      bool
	closed       bool
	mu           sync.Mutex
}

// entry is the manager's side of a session: its public state (Task 4 adds what runs it).
type entry struct {
	session *jumphost.Session
	cancel  context.CancelFunc
	// process is the running aws process (nil before it starts and after it ends).
	process         Process
	stopTimer       func() bool
	cancelReconnect context.CancelFunc
	// identity checks the AWS sign-in of the session; nil when its runner cannot.
	identity IdentityChecker
	options  jumphost.Options
	info     Session
	// signInAttempt numbers the sign-in listings and checks; a result of an older one is dropped.
	signInAttempt int
}

type subscriber struct {
	ch     chan Event
	lagged bool
}

// NewManager returns a manager whose sessions live until ctx ends or Shutdown is called.
func NewManager(ctx context.Context, cfg Config) *Manager {
	m := &Manager{
		ctx: ctx, runner: cfg.Runner, launch: cfg.Launch, now: cfg.Now, afterFunc: cfg.AfterFunc,
		subs: map[*subscriber]struct{}{}, ended: make(chan struct{}),
	}
	if m.launch == nil {
		m.launch = AWSLauncher
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.afterFunc == nil {
		m.afterFunc = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	return m
}

// Subscribe implements Service.
func (m *Manager) Subscribe(ctx context.Context) (Snapshot, <-chan Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub := &subscriber{ch: make(chan Event, subscriberBuffer)}
	m.subs[sub] = struct{}{}
	snapshot := m.snapshotLocked()
	context.AfterFunc(ctx, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.subs[sub]; ok {
			delete(m.subs, sub)
			close(sub.ch)
		}
	})
	return snapshot, sub.ch, nil
}

// Snapshot implements Service.
func (m *Manager) Snapshot(context.Context) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked(), nil
}

func (m *Manager) snapshotLocked() Snapshot {
	list := make([]Session, 0, len(m.entries))
	for _, e := range m.entries {
		list = append(list, e.info.Clone())
	}
	approvals := make([]Approval, 0, len(m.approvals))
	for _, p := range m.approvals {
		approvals = append(approvals, p.info)
	}
	return Snapshot{Seq: m.seq, Sessions: list, Approvals: approvals}
}

// publish numbers ev and hands it to every subscriber, never blocking: a full subscriber is
// marked lagged and gets EventResync instead of the events it missed.
func (m *Manager) publish(ev *Event) {
	m.seq++
	ev.Seq = m.seq
	for sub := range m.subs {
		if sub.lagged {
			select {
			case sub.ch <- Event{Kind: EventResync, Seq: ev.Seq}:
				sub.lagged = false
			default:
			}
			continue
		}
		select {
		case sub.ch <- *ev:
		default:
			sub.lagged = true
		}
	}
}

// credentialsTimeout bounds the Secrets Manager calls revealing passwords.
const credentialsTimeout = 30 * time.Second

// Open implements Service.
func (m *Manager) Open(ctx context.Context, req OpenRequest) (ID, error) { //nolint:gocritic // signature fixed by Service.
	m.mu.Lock()
	closing := m.closing
	m.mu.Unlock()
	if closing {
		return 0, ErrClosed
	}
	session, cancel, identity, err := m.prepare(ctx, &req.Options, req.Interactive)
	if err != nil {
		return 0, err
	}
	if req.Warning != "" {
		session.Intro = append(session.Intro, "Warning: "+req.Warning)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		session.Release()
		cancel()
		return 0, ErrClosed
	}
	e := m.addLocked(session, &req.Options, cancel, identity)
	return e.info.ID, nil
}

// Adopt runs session, already prepared with options (the first session of `airlock <type> <name>`).
func (m *Manager) Adopt(session *jumphost.Session, options *jumphost.Options) ID {
	var identity IdentityChecker
	if runner, err := m.runner(); err == nil {
		identity = identityOf(runner)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.addLocked(session, options, func() {}, identity)
	return e.info.ID
}

func (m *Manager) addLocked(session *jumphost.Session, options *jumphost.Options, cancel context.CancelFunc, identity IdentityChecker) *entry {
	e := m.newEntryLocked(session, options, cancel, identity, 0, nil, "")
	m.entries = append(m.entries, e)
	m.publish(&Event{Kind: EventAdded, ID: e.info.ID, Session: new(e.info.Clone())})
	m.startLocked(e)
	return e
}

// prepare runs the runner's Prepare. The preparation stops when ctx ends; once prepared, the
// session keeps a context of its own, ended by cancel when the session ends. identity checks the
// AWS sign-in of the session later; nil when the runner cannot.
func (m *Manager) prepare(ctx context.Context, options *jumphost.Options, interactive jumphost.InteractiveFunc) (session *jumphost.Session, cancel context.CancelFunc, identity IdentityChecker, err error) {
	runner, err := m.runner()
	if err != nil {
		return nil, nil, nil, err
	}
	prepareCtx, cancel := context.WithCancel(m.ctx)
	if interactive != nil {
		prepareCtx = jumphost.WithInteractive(prepareCtx, interactive)
	}
	stop := context.AfterFunc(ctx, cancel)
	session, err = runner.Prepare(prepareCtx, *options)
	if !stop() {
		// ctx ended during the preparation: free what it reserved rather than open it.
		if session != nil {
			session.Release()
		}
		cancel()
		return nil, nil, nil, ctx.Err()
	}
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	return session, cancel, identityOf(runner), nil
}

// newEntryLocked builds the entry of a prepared session; id 0 takes a new ID. history holds the
// log of the ended session a restart replaces, followed by note.
func (m *Manager) newEntryLocked(session *jumphost.Session, options *jumphost.Options, cancel context.CancelFunc, identity IdentityChecker, id ID, history []LogLine, note string) *entry {
	if id == 0 {
		m.nextID++
		id = m.nextID
	}
	e := &entry{session: session, options: *options, cancel: cancel, identity: identity}
	e.info = Session{
		ID: id, Label: session.Label, ResourceType: session.ResourceType, Target: session.Target,
		Profile: session.Profile, Region: session.Region, Details: session.Details, LocalPort: session.LocalPort,
		Environment: session.Environment, Production: session.Production, OpenedBy: options.OpenedBy, Options: withoutPassword(*options), State: StateStarting, StartedAt: m.now(),
		Lines: append([]LogLine(nil), history...), HasSignIn: session.SignIn != nil, HasCredentials: session.ReadCredentials != nil,
	}
	if server := session.MCPServer(); server != nil {
		e.info.MCP = &MCP{LogDir: server.QueryLogDir(), Writes: server.AllowsWrites()}
		server.Subscribe(func(query mcpserve.ToolEvent) { m.addQuery(e, &query) })
	}
	var lines []string
	if note != "" {
		lines = append(lines, "", note, "")
	}
	lines = append(append(lines, session.Intro...), "", "Starting session...")
	e.info.Lines = appendLines(e.info.Lines, m.logLines(lines))
	return e
}

func (m *Manager) logLines(texts []string) []LogLine {
	now := m.now()
	lines := make([]LogLine, len(texts))
	for i, text := range texts {
		lines[i] = LogLine{At: now, Text: text}
	}
	return lines
}

// lines appends texts to the log of e and tells the subscribers.
func (m *Manager) lines(e *entry, texts ...string) {
	added := m.logLines(texts)
	e.info.Lines = appendLines(e.info.Lines, added)
	m.publish(&Event{Kind: EventLines, ID: e.info.ID, Lines: added})
}

// changed tells the subscribers the state of e changed.
func (m *Manager) changed(e *entry) {
	m.publish(&Event{Kind: EventUpdated, ID: e.info.ID, Session: e.info.meta()})
}

func (m *Manager) addQuery(e *entry, query *mcpserve.ToolEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.currentLocked(e) || e.info.MCP == nil {
		return
	}
	appendQuery(e.info.MCP, query)
	m.publish(&Event{Kind: EventMCPQuery, ID: e.info.ID, Query: query})
}

// startLocked starts e: its aws process, or, for a direct database, its sign-in right away.
func (m *Manager) startLocked(e *entry) {
	if e.info.Direct() {
		e.info.State, e.info.Ready = StateRunning, true
		m.lines(e, "Connected directly to "+e.info.Details.ConnectedServer+": no tunnel to wait for.")
		m.changed(e)
		m.startSignInLocked(e)
		return
	}
	if e.identity != nil {
		m.watchIdentityLocked()
	}
	args := append(append([]string{}, e.session.CommandArgs...), e.session.AwsArgs...)
	go m.run(e, args)
}

// run launches the aws process of e and waits for it.
func (m *Manager) run(e *entry, args []string) {
	process, err := m.launch(args, func(line string) { m.output(e, line) })
	m.mu.Lock()
	if err != nil {
		m.endLocked(e, err)
		m.mu.Unlock()
		return
	}
	e.process = process
	e.info.State = StateRunning
	if e.info.StopRequested || m.closed {
		e.info.State = StateStopping
		process.Terminate()
	}
	m.changed(e)
	m.mu.Unlock()

	err = process.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.currentLocked(e) {
		return
	}
	m.endLocked(e, err)
	m.scheduleReconnectLocked(e)
}

// output logs a line of the aws process of e, and follows session-manager-plugin's progress.
func (m *Manager) output(e *entry, line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.currentLocked(e) {
		return
	}
	wasReady, connections := e.info.Ready, e.info.Connections
	m.lines(e, line)
	switch {
	case strings.Contains(line, "Waiting for connections"):
		e.info.Ready = true
	case strings.Contains(line, "Connection accepted"):
		e.info.Ready = true
		e.info.Connections++
	}
	if e.info.Ready != wasReady || e.info.Connections != connections {
		m.changed(e)
	}
	if !wasReady && e.info.Ready {
		m.startSignInLocked(e)
	}
}

// endLocked marks e ended on err and releases what its preparation reserved (port, MCP server,
// IDE data source).
func (m *Manager) endLocked(e *entry, err error) {
	e.info.State, e.info.EndedAt, e.process = StateEnded, m.now(), nil
	if err != nil {
		e.info.Err, e.info.LocalErr = errTexts(err)
	}
	switch {
	case e.info.StopRequested:
		m.lines(e, "", "Session stopped.")
	case err != nil:
		m.lines(e, "", "Session ended with error: "+err.Error())
	default:
		m.lines(e, "", "Session ended.")
	}
	if cleanup := e.session.Release(); cleanup != "" {
		e.info.Cleanup = cleanup
		m.lines(e, cleanup)
	}
	e.cancel()
	m.endSignInLocked(e)
	m.changed(e)
	close(m.ended)
	m.ended = make(chan struct{})
}

// Stop implements Service.
func (m *Manager) Stop(_ context.Context, id ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.findLocked(id)
	if e == nil {
		return ErrUnknownSession
	}
	m.stopLocked(e)
	return nil
}

func (m *Manager) stopLocked(e *entry) {
	pending := e.info.ReconnectPending()
	m.stopReconnectingLocked(e)
	if !e.info.Active() || e.info.State == StateStopping {
		// An ended session waiting for its reconnection no longer waits: the dashboards must know.
		if pending {
			m.changed(e)
		}
		return
	}
	e.info.StopRequested = true
	m.lines(e, "", "Stopping session...")
	switch {
	case e.info.Direct():
		// No process to wait for: the session ends here.
		m.endLocked(e, nil)
	case e.process != nil:
		e.info.State = StateStopping
		e.process.Terminate()
		m.changed(e)
	default:
		// The process is being launched: run sees StopRequested.
		m.changed(e)
	}
}

// Remove implements Service.
func (m *Manager) Remove(_ context.Context, id ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.findLocked(id)
	if e == nil {
		return ErrUnknownSession
	}
	if e.info.Active() {
		return i18n.NewError("sessions.manager.still_running_stop_first", e.info.Target)
	}
	m.stopReconnectingLocked(e)
	index := m.indexLocked(e)
	m.entries = append(m.entries[:index:index], m.entries[index+1:]...)
	m.publish(&Event{Kind: EventRemoved, ID: id})
	return nil
}

// StopAll implements Service.
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	m.closing = true
	for _, e := range m.entries {
		m.stopLocked(e)
	}
	m.mu.Unlock()
	for {
		m.mu.Lock()
		active, ended := m.hasActiveLocked(), m.ended
		m.mu.Unlock()
		if !active {
			return nil
		}
		select {
		case <-ended:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Shutdown terminates every aws process and releases every session, without waiting: the
// program is exiting (or the terminal hung up). Safe to call repeatedly.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closing, m.closed = true, true
	if m.identityStop != nil {
		m.identityStop()
		m.identityStop = nil
	}
	var processes []Process
	var released []*entry
	for _, e := range m.entries {
		m.stopReconnectingLocked(e)
		if e.process != nil {
			processes = append(processes, e.process)
		}
		released = append(released, e)
	}
	m.mu.Unlock()

	for _, p := range processes {
		p.Terminate()
	}
	for _, e := range released {
		cleanup := e.session.Release()
		m.mu.Lock()
		if e.info.Cleanup == "" {
			e.info.Cleanup = cleanup
		}
		m.mu.Unlock()
	}
}

// Credentials implements Service.
func (m *Manager) Credentials(ctx context.Context, id ID) ([]jumphost.DBCredentials, error) {
	m.mu.Lock()
	e := m.findLocked(id)
	var read func(context.Context) ([]jumphost.DBCredentials, error)
	if e != nil {
		read = e.session.ReadCredentials
	}
	m.mu.Unlock()
	if e == nil {
		return nil, ErrUnknownSession
	}
	if read == nil {
		return nil, i18n.NewError("sessions.manager.this_session_does_not")
	}
	ctx, cancel := context.WithTimeout(ctx, credentialsTimeout)
	defer cancel()
	return read(ctx)
}

func (m *Manager) findLocked(id ID) *entry {
	for _, e := range m.entries {
		if e.info.ID == id {
			return e
		}
	}
	return nil
}

func (m *Manager) indexLocked(e *entry) int {
	for i, other := range m.entries {
		if other == e {
			return i
		}
	}
	return -1
}

// currentLocked reports whether e is still the manager's: neither removed nor replaced.
func (m *Manager) currentLocked(e *entry) bool { return m.indexLocked(e) >= 0 }

func (m *Manager) hasActiveLocked() bool {
	for _, e := range m.entries {
		if e.info.Active() {
			return true
		}
	}
	return false
}

// meta is s without its log nor MCP calls: what EventUpdated carries.
func (s *Session) meta() *Session {
	c := s.Clone()
	c.Lines = nil
	if c.MCP != nil {
		c.MCP.Events = nil
	}
	return &c
}

// withoutPassword returns options as sessions show them: the typed password stays in the manager.
func withoutPassword(options jumphost.Options) jumphost.Options { //nolint:gocritic // returns a modified copy.
	options.DBPassword = ""
	return options
}
