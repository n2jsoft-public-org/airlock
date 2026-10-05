package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// daemonStoppedReason is the reason the daemon gives its views when it stops on purpose.
const daemonStoppedReason = "daemon stopped"

const ssmTickInterval = 150 * time.Millisecond

type ssmViewMode int

const (
	ssmModeSessions ssmViewMode = iota
	ssmModeWizard
	ssmModePreparing
	ssmModeConfirmQuit
	ssmModeConfirmStop
	// ssmModeHelp lists every key of the dashboard.
	ssmModeHelp
	// ssmModeCopy offers to copy the values of the selected session.
	ssmModeCopy
	// ssmModePassword shows the password the selected session reads from Secrets Manager.
	ssmModePassword
	// ssmModeSignIn asks which credentials the selected session signs in with.
	ssmModeSignIn
	// ssmModeApproval asks whether an AI agent may open a connection through the MCP gateway.
	ssmModeApproval
)

// ssmTickMsg animates spinners and refreshes uptimes.
type ssmTickMsg time.Time

// ssmEventMsg is one change of the sessions; ok is false once the channel closed.
type ssmEventMsg struct {
	// events is the channel the message came from; nil matches any subscription.
	events <-chan sessions.Event
	event  sessions.Event
	ok     bool
}

// ssmSnapshotMsg is the snapshot taken after an EventResync.
type ssmSnapshotMsg struct {
	err      error
	snapshot sessions.Snapshot
}

// ssmOpenedMsg is the result of an opening or restart; prepareID tells which one.
type ssmOpenedMsg struct {
	err       error
	id        sessions.ID
	prepareID int
	restart   bool
}

// ssmReconnectedMsg carries the service of the daemon reached again.
type ssmReconnectedMsg struct {
	service sessions.Service
	err     error
}

// ssmActionMsg reports a request to the service; prefix starts its error message.
type ssmActionMsg struct {
	err    error
	prefix string
}

// ssmModel is the dashboard: it shows the sessions of its service and asks it for every change.
type ssmModel struct {
	ctx       context.Context
	service   sessions.Service
	discovery JumphostDiscovery
	// events delivers the service's changes; nil until subscribe.
	events      <-chan sessions.Event
	wizard      jumphostWizardModel
	styles      Styles
	errorMsg    string
	sessions    []sessions.Session
	loadingUI   loadingState
	baseOptions jumphost.Options
	viewport    viewport.Model
	// seq is the Seq of the last snapshot or event applied.
	seq      uint64
	selected int
	frame    int
	width    int
	height   int
	mode     ssmViewMode
	// pendingSelect is the session an opening returned, selected once its EventAdded arrives.
	pendingSelect sessions.ID
	// discarded are the sessions opened after the user canceled their preparation: stopped, then
	// closed once ended.
	discarded  []sessions.ID
	confirmYes bool
	quitting   bool
	// showQueries shows the MCP queries of the selected session instead of its logs.
	showQueries bool
	// notice is a neutral message shown above the key hints until the next key.
	notice string
	// prepareID identifies the opening the dashboard waits for (see ssmOpenedMsg.prepareID);
	// cancelPrepare cancels its context, which makes the service release it.
	prepareID     int
	cancelPrepare context.CancelFunc
	// copier puts text on the clipboard (copyToClipboard); copyCursor is the copy menu's choice.
	copier     func(text string) error
	copyCursor int
	// signInCursor is the candidate the sign-in dialog chooses on enter.
	signInCursor int
	// approvals are the pending agent openings; approvalID is the one the approval dialog shows,
	// approvalAllow its focused button, answered the approvals this view answered (their
	// resolution may not have arrived yet).
	approvals     []sessions.Approval
	answered      map[sessions.ApprovalID]bool
	approvalID    sessions.ApprovalID
	approvalAllow bool
	// password is the password dialog (ssmModePassword).
	password ssmPasswordReveal
	// production flags the AWS profiles of the productionProfiles setting.
	production productionMatcher
	// updateNotice, when not empty, tells the wizard a newer airlock release exists.
	updateNotice string
	// databases holds the direct database definitions (DashboardOptions.Databases).
	databases DirectDatabaseStore
	// settings reads and writes config.json (DashboardOptions.Settings); nil hides the Settings screen.
	settings SettingsStore
	// openDirect is the direct database the startup wizard opens on (DashboardOptions.OpenDirect).
	openDirect string
	// interactive runs the terminal commands of the openings (aws sso login); projectDir is set on
	// every opening.
	interactive jumphost.InteractiveFunc
	projectDir  string
	// detach makes q leave the view only (daemon mode).
	detach bool
	// connLost, when not empty, says why the daemon is out of reach; r calls reconnect.
	connLost string
	// reconnecting is set while a reconnection runs, so that r starts only one.
	reconnecting bool
	// exitNotice is printed once the dashboard left the screen.
	exitNotice string
	reconnect  func(ctx context.Context) (sessions.Service, error)
}

// DashboardOptions are the optional settings of RunJumphostSessions.
type DashboardOptions struct {
	// Handoff, when not nil, suspends the dashboard while it runs a command needing the terminal
	// (the runner's sign-ins).
	Handoff *TerminalHandoff
	// ProductionProfiles are the patterns (* wildcard, any case) of the AWS profiles to flag.
	ProductionProfiles []string
	// UpdateNotice, when not empty, is shown in the wizard: a newer airlock release exists.
	UpdateNotice string
	// Databases holds the direct database definitions the wizard lists and edits; nil hides the
	// Direct database card.
	Databases DirectDatabaseStore
	// Settings reads and writes config.json for the Settings screen; nil hides it.
	Settings SettingsStore
	// OpenDirect, when not empty, opens the startup wizard on the review step of that direct
	// database (`airlock direct <name>`).
	OpenDirect string
	// ProjectDir is set on every opening's Options.ProjectDir.
	ProjectDir string
	// Detach marks a daemon view: q detaches (Task 13).
	Detach bool
	// Reconnect reaches the daemon again after its connection was lost (daemon views).
	Reconnect func(ctx context.Context) (sessions.Service, error)
	// Notice, when not empty, is shown from the start (a daemon view: why the MCP gateway is off).
	Notice string
	// lightBackground selects the palette for a terminal with a light background; RunJumphostSessions
	// asks the terminal before starting (false, a dark background, when it cannot tell).
	lightBackground bool
}

// shutdowner is the in-process service, whose sessions end with the dashboard (CLI mode).
type shutdowner interface {
	Shutdown()
}

// RunJumphostSessions shows the sessions of service and lets the user open more in parallel.
// first, when not 0, is selected at start; otherwise the dashboard opens on the wizard.
func RunJumphostSessions(ctx context.Context, service sessions.Service, discovery JumphostDiscovery, first sessions.ID, base jumphost.Options, out io.Writer, opts DashboardOptions) error { //nolint:gocritic // base is the caller's copy, kept by the model.
	owned, ownsSessions := service.(shutdowner)
	ownsSessions = ownsSessions && !opts.Detach
	if ownsSessions {
		stopHangupWatch := jumphost.RunOnHangup(owned.Shutdown)
		defer stopHangupWatch()
	}
	subscribeCtx, unsubscribe := context.WithCancel(ctx)
	defer unsubscribe()

	opts.lightBackground = !lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	model := newDashboardModel(subscribeCtx, service, discovery, first, base, &opts)

	program := tea.NewProgram(model)
	if opts.Handoff != nil {
		opts.Handoff.attach(program)
		defer opts.Handoff.attach(nil)
	}
	finalModel, err := program.Run()
	if result, ok := finalModel.(ssmModel); ok && result.exitNotice != "" {
		if _, writeErr := fmt.Fprintln(out, result.exitNotice); writeErr != nil && err == nil {
			err = writeErr
		}
	}
	if !ownsSessions {
		return err
	}
	// Whatever happened, never leave aws processes or IDE data sources behind.
	owned.Shutdown()
	if err != nil {
		return err
	}
	snapshot, snapErr := service.Snapshot(ctx)
	if snapErr != nil {
		return nil
	}
	printSessionSummary(out, snapshot.Sessions)
	// A lone session failing on its own keeps reporting its error.
	if result, ok := finalModel.(ssmModel); ok && len(result.sessions) == 1 && len(snapshot.Sessions) == 1 {
		if s := snapshot.Sessions[0]; !s.StopRequested && s.Err != "" {
			return errors.New(s.Err)
		}
	}
	return nil
}

// newDashboardModel is the dashboard of RunJumphostSessions, subscribed and on its first screen.
func newDashboardModel(ctx context.Context, service sessions.Service, discovery JumphostDiscovery, first sessions.ID, base jumphost.Options, opts *DashboardOptions) ssmModel { //nolint:gocritic // base is kept by the model.
	model := newSSMModel(ctx, service, discovery, base)
	model.styles = NewStyles(!opts.lightBackground)
	model.production = productionMatcher(opts.ProductionProfiles)
	model.updateNotice, model.databases, model.openDirect = opts.UpdateNotice, opts.Databases, opts.OpenDirect
	model.settings, model.notice = opts.Settings, opts.Notice
	model.projectDir, model.detach, model.reconnect = opts.ProjectDir, opts.Detach, opts.Reconnect
	if opts.Handoff != nil {
		model.interactive = opts.Handoff.Run
	}
	return model.subscribe().startupView(first)
}

func newSSMModel(ctx context.Context, service sessions.Service, discovery JumphostDiscovery, base jumphost.Options) ssmModel { //nolint:gocritic // base is kept by the model.
	return ssmModel{
		ctx: ctx, service: service, discovery: discovery, baseOptions: base,
		viewport: viewport.New(), styles: NewStyles(true), copier: copyToClipboard,
	}
}

// startupView chooses the first screen: session first when it is given, otherwise the wizard. A
// daemon view showing sessions opens on them, unless the command named something to open
// (`airlock direct <name>`, `airlock <type>`); a view that could not reach the daemon shows why.
func (m ssmModel) startupView(first sessions.ID) ssmModel {
	if index := m.sessionIndex(first); first != 0 && index >= 0 {
		return m.selectSession(index)
	}
	if m.connLost != "" {
		return m
	}
	asked := m.openDirect != "" || m.baseOptions.ResourceType != ""
	if len(m.sessions) == 0 || first == 0 && (!m.detach || asked) {
		return m.startupWizard()
	}
	return m
}

// subscribe takes the service's sessions and starts following its events. A daemon view that cannot
// follow them shows the lost connection, which r reaches again.
func (m ssmModel) subscribe() ssmModel {
	snapshot, events, err := m.service.Subscribe(m.ctx)
	if err != nil {
		m.events = nil
		if m.detach {
			m.connLost = "could not follow the sessions: " + err.Error()
		} else {
			m.errorMsg = "Could not follow the sessions: " + err.Error()
		}
		m.refreshViewport()
		return m
	}
	m.events, m.sessions, m.seq, m.approvals = events, snapshot.Sessions, snapshot.Seq, snapshot.Approvals
	m = m.pruneAnswered()
	m.refreshViewport()
	return m
}

func waitSSMEvent(events <-chan sessions.Event) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-events
		return ssmEventMsg{event: ev, ok: ok, events: events}
	}
}

func (m ssmModel) Init() tea.Cmd {
	var wizardInit tea.Cmd
	if m.mode == ssmModeWizard {
		wizardInit = m.wizard.Init()
	}
	return tea.Batch(waitSSMEvent(m.events), ssmTickCmd(), wizardInit)
}

func ssmTickCmd() tea.Cmd {
	return tea.Tick(ssmTickInterval, func(t time.Time) tea.Msg { return ssmTickMsg(t) })
}

func (m ssmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.refreshViewport()
		if m.mode == ssmModeWizard {
			return m.updateWizard(msg)
		}
		return m, nil
	case ssmTickMsg:
		m.frame++
		m = m.settleApprovals()
		return m, ssmTickCmd()
	case ssmEventMsg:
		return m.handleEvent(msg)
	case ssmSnapshotMsg:
		return m.handleSnapshot(msg)
	case ssmReconnectedMsg:
		return m.handleReconnected(msg)
	case ssmOpenedMsg:
		return m.handleOpened(msg)
	case ssmActionMsg:
		if msg.err != nil {
			// A refused "stop all & quit" leaves the dashboard usable.
			m.quitting = false
			m.errorMsg = msg.prefix + msg.err.Error()
			m.refreshViewport()
		}
		return m, nil
	case ssmCopiedMsg:
		return m.handleCopied(msg)
	case ssmPasswordMsg:
		return m.handlePassword(msg)
	case jumphostWizardDoneMsg:
		return m.handleWizardDone(msg)
	case loadingTickMsg:
		switch m.mode {
		case ssmModeWizard:
			return m.updateWizard(msg)
		case ssmModePreparing:
			return m, m.loadingUI.update(msg)
		default:
			return m, nil
		}
	case tea.KeyPressMsg:
		switch m.mode {
		case ssmModeWizard:
			return m.updateWizard(msg)
		case ssmModePreparing:
			return m.handlePreparingKey(msg)
		case ssmModeConfirmQuit, ssmModeConfirmStop:
			return m.handleConfirmKey(msg)
		case ssmModeHelp:
			return m.handleHelpKey(msg)
		case ssmModeCopy:
			return m.handleCopyKey(msg)
		case ssmModePassword:
			return m.handlePasswordKey(msg)
		case ssmModeSignIn:
			return m.handleSignInKey(msg)
		case ssmModeApproval:
			return m.handleApprovalKey(msg)
		default:
			return m.handleSessionKey(msg)
		}
	}

	if m.mode == ssmModeWizard {
		return m.updateWizard(msg)
	}
	return m, nil
}

func (m ssmModel) handleEvent(msg ssmEventMsg) (tea.Model, tea.Cmd) {
	if msg.events != nil && msg.events != m.events {
		// A previous subscription (before a reconnection).
		return m, nil
	}
	if !msg.ok {
		if m.detach {
			return m.loseConnection("connection to the daemon lost")
		}
		// The service stopped (CLI mode at exit).
		return m, nil
	}
	ev := msg.event
	switch ev.Kind {
	case sessions.EventClosed:
		if ev.Err == daemonStoppedReason {
			m.exitNotice = "The airlock daemon stopped."
			return m, tea.Quit
		}
		return m.loseConnection(ev.Err)
	case sessions.EventResync:
		service, ctx := m.service, m.ctx
		return m, func() tea.Msg {
			snapshot, err := service.Snapshot(ctx)
			return ssmSnapshotMsg{snapshot: snapshot, err: err}
		}
	case sessions.EventNotice:
		m.notice = ev.Notice
		m.refreshViewport()
		return m, waitSSMEvent(m.events)
	}
	if ev.Seq <= m.seq {
		return m, waitSSMEvent(m.events)
	}
	m.seq = ev.Seq
	if ev.Kind == sessions.EventApprovalRequested || ev.Kind == sessions.EventApprovalResolved {
		m.approvals = sessions.ApplyApproval(m.approvals, &ev)
		return m.settleApprovals(), waitSSMEvent(m.events)
	}
	before := map[sessions.ID]*sessions.Session{ev.ID: m.sessionCopy(ev.ID)}
	m.sessions = sessions.Apply(m.sessions, &ev)
	if ev.Kind == sessions.EventRemoved {
		m = m.selectSession(min(m.selected, max(len(m.sessions)-1, 0)))
	}
	m, closeCmd, quit := m.settle([]sessions.ID{ev.ID}, before)
	if quit {
		return m, tea.Quit
	}
	var cmd tea.Cmd
	if ev.Kind == sessions.EventPasswordRejected {
		var next tea.Model
		next, cmd = m.askDirectPasswordAgain(ev)
		if asked, ok := next.(ssmModel); ok {
			m = asked
		}
	}
	return m, tea.Batch(waitSSMEvent(m.events), cmd, closeCmd)
}

// handleSnapshot replaces the sessions with the snapshot taken after an EventResync, then follows
// the change as an event would.
func (m ssmModel) handleSnapshot(msg ssmSnapshotMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, waitSSMEvent(m.events)
	}
	before := make(map[sessions.ID]*sessions.Session, len(m.sessions))
	for index := range m.sessions {
		before[m.sessions[index].ID] = m.sessionCopy(m.sessions[index].ID)
	}
	m.sessions, m.seq = msg.snapshot.Sessions, msg.snapshot.Seq
	m.approvals = msg.snapshot.Approvals
	m.selected = min(m.selected, max(len(m.sessions)-1, 0))
	m.refreshViewport()
	changed := make([]sessions.ID, 0, len(m.sessions))
	for index := range m.sessions {
		changed = append(changed, m.sessions[index].ID)
	}
	m, closeCmd, quit := m.settle(changed, before)
	m = m.settleApprovals()
	if quit {
		return m, tea.Quit
	}
	return m, tea.Batch(waitSSMEvent(m.events), closeCmd)
}

// settle follows a change of the sessions changed (before holds their copies from before it), after
// an event or a resync: the opening waited for is selected, the sign-in dialog follows the selected
// session, the discarded sessions that ended are closed. quit is set once "stop all & quit" has
// nothing left running: only it leaves on its own, an ended session stays on screen with its logs.
func (m ssmModel) settle(changed []sessions.ID, before map[sessions.ID]*sessions.Session) (next ssmModel, cmd tea.Cmd, quit bool) {
	if m.pendingSelect != 0 {
		if index := m.sessionIndex(m.pendingSelect); index >= 0 {
			m.pendingSelect = 0
			m = m.selectSession(index)
		}
	}
	var closeCmds []tea.Cmd
	for _, id := range changed {
		if s := m.session(id); s != nil {
			m.openSignInDialog(before[id], s)
			m.closeSignInDialog(s)
			m.refreshSelected(s)
			closeCmds = append(closeCmds, m.closeDiscarded(s))
		}
	}
	if m.quitting && !m.hasActiveSessions() {
		return m, nil, true
	}
	return m, tea.Batch(closeCmds...), false
}

// loseConnection ends the view of a closed service: it quits after "stop all & quit", otherwise
// it shows why and waits for r.
func (m ssmModel) loseConnection(reason string) (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, tea.Quit
	}
	m.connLost, m.reconnecting = reason, false
	// No dialog or wizard may act on the dead service.
	m.mode = ssmModeSessions
	m.approvals = nil
	m = m.pruneAnswered()
	m.refreshViewport()
	return m, nil
}

// reconnectCmd reaches the daemon again in the background.
func (m ssmModel) reconnectCmd() tea.Cmd {
	if m.reconnect == nil {
		return nil
	}
	reconnect, ctx := m.reconnect, m.ctx
	return func() tea.Msg {
		service, err := reconnect(ctx)
		return ssmReconnectedMsg{service: service, err: err}
	}
}

func (m ssmModel) handleReconnected(msg ssmReconnectedMsg) (tea.Model, tea.Cmd) {
	m.reconnecting = false
	if msg.err != nil {
		m.connLost = "could not reach the daemon: " + msg.err.Error()
		m.refreshViewport()
		return m, nil
	}
	// A restarted daemon numbers its sessions from 1 again: the IDs the view waited for mean nothing
	// there, and closing them could close another session.
	m.service, m.connLost = msg.service, ""
	m.discarded, m.pendingSelect = nil, 0
	m = m.subscribe()
	return m, waitSSMEvent(m.events)
}

// sessionCopy returns a copy of session id, nil when unknown.
func (m ssmModel) sessionCopy(id sessions.ID) *sessions.Session {
	if s := m.session(id); s != nil {
		return new(s.Clone())
	}
	return nil
}

// act sends a request to the service in the background; its error is shown after prefix.
func (m ssmModel) act(prefix string, fn func(ctx context.Context) error) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg { return ssmActionMsg{err: fn(ctx), prefix: prefix} }
}

func (m ssmModel) handleSessionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.connLost != "" {
		switch msg.String() {
		case "r":
			if m.reconnecting {
				return m, nil
			}
			cmd := m.reconnectCmd()
			m.reconnecting = cmd != nil
			return m, cmd
		case "q", "ctrl+c", "Q":
			return m, tea.Quit
		}
		return m, nil
	}
	if m.detach && !m.quitting {
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "Q":
			m.mode, m.confirmYes = ssmModeConfirmQuit, true
			return m, nil
		}
	}
	if m.quitting {
		// A second ctrl+c skips waiting; remaining processes are terminated on exit.
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}

	if m.errorMsg != "" || m.notice != "" {
		m.errorMsg, m.notice = "", ""
		m.refreshViewport()
	}
	switch msg.String() {
	case "ctrl+c", "q":
		if !m.hasActiveSessions() {
			return m, tea.Quit
		}
		m.mode = ssmModeConfirmQuit
		m.confirmYes = true
		return m, nil
	case "n":
		return m.openWizard()
	case ",":
		return m.openSettings()
	case "?":
		m.mode = ssmModeHelp
		return m, nil
	case "c":
		return m.openCopyMenu()
	case "p":
		return m.revealPassword()
	case "a":
		return m.retrySignIn()
	case "r":
		return m.restartSelected()
	case "m":
		if s := m.selectedSession(); s != nil && s.MCP != nil {
			m.showQueries = !m.showQueries
			m.refreshViewport()
			m.viewport.GotoBottom()
		}
		return m, nil
	case "x":
		s := m.selectedSession()
		if s == nil {
			return m, nil
		}
		if !s.Active() {
			service, id := m.service, s.ID
			return m, m.act("Could not close: ", func(ctx context.Context) error { return service.Remove(ctx, id) })
		}
		if s.State == sessions.StateStopping {
			return m, nil
		}
		m.mode = ssmModeConfirmStop
		m.confirmYes = true
		return m, nil
	case "tab", "right":
		return m.selectSession(m.selected + 1), nil
	case "shift+tab", "left":
		return m.selectSession(m.selected - 1), nil
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		index := int(msg.String()[0] - '1')
		if index < len(m.sessions) {
			return m.selectSession(index), nil
		}
		return m, nil
	case "g", "home":
		m.viewport.GotoTop()
	case "G", "end":
		m.viewport.GotoBottom()
	case "pgup":
		m.viewport.HalfPageUp()
	case "pgdown":
		m.viewport.HalfPageDown()
	case "up":
		m.viewport.ScrollUp(1)
	case "down":
		m.viewport.ScrollDown(1)
	}
	return m, nil
}

// handlePreparingKey cancels the preparation on esc; ctrl+c cancels it too, then asks to quit. A
// canceled preparation is released by the service.
func (m ssmModel) handlePreparingKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		if m.cancelPrepare != nil {
			m.cancelPrepare()
			m.cancelPrepare = nil
		}
		m.prepareID++
		m.loadingUI.stop()
		m.mode = ssmModeSessions
		m.notice = "Connection canceled"
		m.refreshViewport()
		if msg.String() == "ctrl+c" {
			return m.handleSessionKey(msg)
		}
	}
	return m, nil
}

// handleHelpKey closes the help on any key; ctrl+c then asks to quit, as it does from the sessions.
func (m ssmModel) handleHelpKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.mode = ssmModeSessions
	m.refreshViewport()
	if msg.String() == "ctrl+c" {
		return m.handleSessionKey(msg)
	}
	return m, nil
}

func (m ssmModel) handleConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "right", "tab":
		m.confirmYes = !m.confirmYes
		return m, nil
	case "n", "esc":
		return m.closeDialog(false)
	case "y":
		return m.closeDialog(true)
	case "enter":
		return m.closeDialog(m.confirmYes)
	case "ctrl+c":
		if m.detach {
			// In a daemon view ctrl+c only leaves the view, it never stops the daemon.
			return m, tea.Quit
		}
		// ctrl+c always means quitting: pressed again on the quit dialog, it confirms it.
		if m.mode == ssmModeConfirmQuit {
			return m.closeDialog(true)
		}
		m.mode = ssmModeConfirmQuit
		m.confirmYes = true
		return m, nil
	default:
		return m, nil
	}
}

// closeDialog closes the confirmation dialog, doing what it asked when confirmed.
func (m ssmModel) closeDialog(confirmed bool) (tea.Model, tea.Cmd) {
	mode := m.mode
	m.mode = ssmModeSessions
	m.refreshViewport()
	if !confirmed {
		return m, nil
	}
	service := m.service
	if mode == ssmModeConfirmQuit {
		m.quitting = true
		if !m.hasActiveSessions() && !m.detach {
			return m, tea.Quit
		}
		return m, m.act("Could not stop the sessions: ", service.StopAll)
	}
	if s := m.selectedSession(); s != nil {
		id := s.ID
		return m, m.act("Could not stop: ", func(ctx context.Context) error { return service.Stop(ctx, id) })
	}
	return m, nil
}

// wizardDefaults are the options every new connection inherits from the command line.
func (m ssmModel) wizardDefaults() jumphost.Options {
	return jumphost.Options{
		SkipIDEDataSource: m.baseOptions.SkipIDEDataSource,
		SkipMCP:           m.baseOptions.SkipMCP,
		MCPAllowWrites:    m.baseOptions.MCPAllowWrites,
		DBUser:            m.baseOptions.DBUser,
	}
}

// startupWizard opens the wizard before the program runs; Init starts it. The first connection
// also honors --profile and --region, so the wizard skips the steps they answer, and --db-secret,
// which belongs to that one target (later connections keep their own saved choices).
func (m ssmModel) startupWizard() ssmModel {
	base := m.wizardDefaults()
	base.AwsProfile = m.baseOptions.AwsProfile
	base.AwsRegion = m.baseOptions.AwsRegion
	base.DBSecretARN = m.baseOptions.DBSecretARN
	m = m.showWizard(base)
	// `airlock direct <name>` opens on that database's review; later wizards (n) start as usual.
	m.wizard.openDirect = m.openDirect
	return m
}

func (m ssmModel) showWizard(base jumphost.Options) ssmModel { //nolint:gocritic // base is the wizard's copy.
	m.wizard = newStyledWizardModel(m.ctx, m.discovery, base, m.styles)
	m.wizard.embedded = true
	m.wizard.hostHasSessions = len(m.sessions) > 0
	m.wizard.production = m.production
	m.wizard.updateNotice = m.updateNotice
	m.wizard.databases = m.databases
	m.wizard.settings = m.settings
	m.wizard.active = m.activeConnectionKeys()
	m.mode = ssmModeWizard
	m.errorMsg = ""
	return m
}

func (m ssmModel) openWizard() (tea.Model, tea.Cmd) {
	return m.showWizard(m.wizardDefaults()).startWizard()
}

// openSettings opens the wizard on the Settings screen; esc there returns to the sessions.
func (m ssmModel) openSettings() (tea.Model, tea.Cmd) {
	if m.settings == nil {
		return m, nil
	}
	m = m.showWizard(m.wizardDefaults())
	m.wizard.settingsAtStart = true
	return m.startWizard()
}

// startWizard starts the wizard showWizard set up, sized to the dashboard.
func (m ssmModel) startWizard() (tea.Model, tea.Cmd) {
	initCmd := m.wizard.Init()
	if m.width > 0 && m.height > 0 {
		updated, _ := m.wizard.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		if wizard, ok := updated.(jumphostWizardModel); ok {
			m.wizard = wizard
		}
	}
	return m, initCmd
}

// sessionKey identifies the target of s as the wizard's recent connections do (see connectionKey).
func sessionKey(s *sessions.Session) string {
	if s.Direct() {
		return directConnectionKey(s.Target)
	}
	return connectionKey(s.Profile, s.Region, s.ResourceType, s.Target)
}

// activeSessionIndex returns the position of the running session of key (see connectionKey), or -1.
func (m ssmModel) activeSessionIndex(key string) int {
	for index := range m.sessions {
		if s := &m.sessions[index]; s.Active() && sessionKey(s) == key {
			return index
		}
	}
	return -1
}

// activeConnectionKeys identifies the targets of the running sessions, as connectionKey builds them.
func (m ssmModel) activeConnectionKeys() map[string]bool {
	keys := map[string]bool{}
	for index := range m.sessions {
		if s := &m.sessions[index]; s.Active() {
			keys[sessionKey(s)] = true
		}
	}
	return keys
}

func (m ssmModel) updateWizard(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.wizard.Update(msg)
	if wizard, ok := updated.(jumphostWizardModel); ok {
		m.wizard = wizard
		// The Settings screen may change productionProfiles: the session cards follow it.
		m.production = wizard.production
	}
	return m, cmd
}

func (m ssmModel) handleWizardDone(msg jumphostWizardDoneMsg) (tea.Model, tea.Cmd) {
	if msg.canceled {
		m.mode = ssmModeSessions
		if index := m.activeSessionIndex(msg.switchTo); index >= 0 {
			m = m.selectSession(index)
		}
		m.refreshViewport()
		if len(m.sessions) == 0 {
			return m, tea.Quit
		}
		return m, nil
	}

	label := fmt.Sprintf("Preparing %s %s...", strings.ToUpper(msg.options.ResourceType), msg.options.Name)
	return m.open(msg.options, msg.warning, label)
}

// open asks the service for the session of options, showing label while it is prepared.
func (m ssmModel) open(options jumphost.Options, warning, label string) (tea.Model, tea.Cmd) { //nolint:gocritic // options is modified, then sent.
	m.mode = ssmModePreparing
	m.prepareID++
	prepareID := m.prepareID
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelPrepare = cancel
	if options.ProjectDir == "" {
		options.ProjectDir = m.projectDir
	}
	service, req := m.service, sessions.OpenRequest{Options: options, Warning: warning, Interactive: m.interactive}
	return m, tea.Batch(func() tea.Msg {
		id, err := service.Open(ctx, req)
		return ssmOpenedMsg{id: id, err: err, prepareID: prepareID}
	}, m.loadingUI.start(label, prepareLines(options.ResourceType)))
}

// restartSelected prepares the ended selected session again: same target, profile, region, local
// port and sign-in choices, so the tools pointed at it reconnect as they were.
func (m ssmModel) restartSelected() (tea.Model, tea.Cmd) {
	s := m.selectedSession()
	if s == nil || s.Active() {
		return m, nil
	}
	if m.activeSessionIndex(sessionKey(s)) >= 0 {
		m.errorMsg = s.Target + " is already connected in another session"
		return m, nil
	}
	m.mode = ssmModePreparing
	m.prepareID++
	prepareID, id := m.prepareID, s.ID
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelPrepare = cancel
	service, interactive := m.service, m.interactive
	label := fmt.Sprintf("Restarting %s %s...", strings.ToUpper(s.ResourceType), s.Target)
	return m, tea.Batch(func() tea.Msg {
		return ssmOpenedMsg{id: id, err: service.Restart(ctx, id, interactive), prepareID: prepareID, restart: true}
	}, m.loadingUI.start(label, prepareLines(s.ResourceType)))
}

// prepareLines entertain while a session of resourceType is prepared: a direct database has no
// local port, jumphost or tunnel.
func prepareLines(resourceType string) []string {
	if resourceType == sessions.DirectResourceType {
		return []string{
			"Looking up the database's address...",
			"Setting up a seat for your AI assistant...",
			"Keeping your other sessions warm...",
		}
	}
	return []string{
		"Reserving a cozy local port...",
		"Asking the jumphost for one more tunnel...",
		"Keeping your other sessions warm...",
	}
}

func (m ssmModel) handleOpened(msg ssmOpenedMsg) (tea.Model, tea.Cmd) {
	// The user canceled this preparation (esc). When it failed, or the service saw the cancel, it
	// released what it reserved; an opening that succeeded meanwhile is stopped and closed, as if
	// it never opened. A restart that succeeded meanwhile keeps running in its slot.
	if msg.prepareID != m.prepareID {
		if msg.err != nil || msg.restart {
			return m, nil
		}
		return m.discard(msg.id)
	}
	m.loadingUI.stop()
	m.mode = ssmModeSessions
	// The service detached the session from the request's context: canceling it frees the context.
	if m.cancelPrepare != nil {
		m.cancelPrepare()
	}
	m.cancelPrepare = nil
	if msg.err != nil {
		m.errorMsg = "Could not open connection: " + msg.err.Error()
		if msg.restart {
			m.errorMsg = "Could not restart: " + msg.err.Error()
		}
		m.refreshViewport()
		return m, nil
	}
	if index := m.sessionIndex(msg.id); index >= 0 {
		m = m.selectSession(index)
	} else {
		m.pendingSelect = msg.id
	}
	return m, nil
}

// discard stops session id, opened although its preparation was canceled; it is closed once ended
// (see closeDiscarded).
func (m ssmModel) discard(id sessions.ID) (tea.Model, tea.Cmd) {
	m.discarded = append(append([]sessions.ID(nil), m.discarded...), id)
	service := m.service
	return m, m.act("", func(ctx context.Context) error { return service.Stop(ctx, id) })
}

// closeDiscarded closes s once it ended when the user canceled its opening; nil otherwise.
func (m *ssmModel) closeDiscarded(s *sessions.Session) tea.Cmd {
	index := slices.Index(m.discarded, s.ID)
	if index < 0 || s.Active() {
		return nil
	}
	m.discarded = slices.Delete(slices.Clone(m.discarded), index, index+1)
	service, id := m.service, s.ID
	return m.act("", func(ctx context.Context) error { return service.Remove(ctx, id) })
}

func (m ssmModel) selectSession(index int) ssmModel {
	if len(m.sessions) == 0 {
		m.selected = 0
		m.refreshViewport()
		return m
	}
	m.selected = (index + len(m.sessions)) % len(m.sessions)
	m.refreshViewport()
	m.viewport.GotoBottom()
	return m
}

func (m ssmModel) session(id sessions.ID) *sessions.Session {
	if index := m.sessionIndex(id); index >= 0 {
		return &m.sessions[index]
	}
	return nil
}

// sessionIndex returns the position of the session id in the sidebar, or -1.
func (m ssmModel) sessionIndex(id sessions.ID) int {
	for index := range m.sessions {
		if m.sessions[index].ID == id {
			return index
		}
	}
	return -1
}

func (m ssmModel) selectedSession() *sessions.Session {
	if m.selected < 0 || m.selected >= len(m.sessions) {
		return nil
	}
	return &m.sessions[m.selected]
}

func (m ssmModel) hasActiveSessions() bool {
	for index := range m.sessions {
		if m.sessions[index].Active() {
			return true
		}
	}
	return false
}

// refreshSelected redraws the log when s is the visible session, following the tail if already at the bottom.
func (m *ssmModel) refreshSelected(s *sessions.Session) {
	if m.selectedSession() != s {
		return
	}
	atBottom := m.viewport.AtBottom()
	m.refreshViewport()
	if atBottom {
		m.viewport.GotoBottom()
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// printSessionSummary tells what each session of list did, once the dashboard closed.
func printSessionSummary(out io.Writer, list []sessions.Session) {
	if out == nil || len(list) == 0 {
		return
	}

	printf := func(format string, args ...any) { _, _ = fmt.Fprintf(out, format, args...) } //nolint:errcheck // best-effort terminal output
	printf("\nSession summary:\n")
	for index := range list {
		s := &list[index]
		end := s.EndedAt
		if end.IsZero() {
			end = time.Now()
		}
		duration := max(end.Sub(s.StartedAt).Round(time.Second), 0)

		server := strings.TrimSpace(s.Details.ConnectedServer)
		if server == "" {
			server = "n/a"
		}

		printf("- %s -> %s\n", s.Label, sessionAddress(s))
		printf("  duration: %s\n  server connected: %s\n", duration, server)
		if s.Err != "" && !s.StopRequested {
			printf("  ended with error: %v\n", s.Err)
		}
		if s.Cleanup != "" {
			printf("  %s\n", s.Cleanup)
		}
		if strings.TrimSpace(s.Details.RestartCommand) != "" {
			printf("  new session command: %s\n", s.Details.RestartCommand)
		}
	}
}
