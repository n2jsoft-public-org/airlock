package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/platform"
)

const (
	maxSessionLogLines = 1000
	ssmTickInterval    = 150 * time.Millisecond
)

// JumphostSessionRunner discovers targets and prepares sessions for the multi-session TUI.
type JumphostSessionRunner interface {
	JumphostDiscovery
	Prepare(ctx context.Context, options jumphost.Options) (*jumphost.Session, error)
}

type ssmSessionState int

const (
	ssmSessionStarting ssmSessionState = iota
	ssmSessionRunning
	ssmSessionStopping
	ssmSessionEnded
)

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
)

type ssmOutputMsg struct {
	line string
	id   int
}

type ssmFinishedMsg struct {
	err error
	id  int
}

type ssmStartedMsg struct {
	cmd *exec.Cmd
	id  int
}

// ssmTickMsg animates spinners and refreshes uptimes.
type ssmTickMsg time.Time

type ssmPreparedMsg struct {
	session *jumphost.Session
	err     error
	warning string
	options jumphost.Options
	// replaces is the id of the ended session a restart takes the place of; 0 for a new session.
	replaces int
	// id identifies the preparation; a canceled one carries an older id than ssmModel.prepareID.
	id int
	// background marks an automatic reconnection, which never takes the screen.
	background bool
}

type ssmLogLine struct {
	at   time.Time
	text string
}

// ssmSession is one port-forwarding session shown in the sidebar.
type ssmSession struct {
	startedAt time.Time
	endedAt   time.Time
	err       error
	session   *jumphost.Session
	cmd       *exec.Cmd
	// options prepared the session; a restart prepares them again.
	options jumphost.Options
	// mcp holds the calls made through the session's MCP server (nil when none runs).
	mcp *ssmMCPQueries
	// signIn checks the database credentials once the tunnel is ready.
	signIn        ssmSignIn
	lines         []ssmLogLine
	id            int
	connections   int
	state         ssmSessionState
	stopRequested bool
	// ready is set once session-manager-plugin listens on the local port.
	ready bool
	// reconnects counts the automatic reconnections in a row; reconnectAt is when the next one
	// starts (zero when none is scheduled) and reconnecting reports one being prepared.
	reconnects   int
	reconnectAt  time.Time
	reconnecting bool
	// cancelReconnect cancels the context of the reconnection being prepared.
	cancelReconnect context.CancelFunc
}

// stopReconnecting cancels the scheduled or running automatic reconnection of s; the result of a
// running one is released when it comes.
func (s *ssmSession) stopReconnecting() {
	if s.cancelReconnect != nil {
		s.cancelReconnect()
		s.cancelReconnect = nil
	}
	s.reconnectAt, s.reconnecting = time.Time{}, false
}

func (s *ssmSession) active() bool {
	return s.state != ssmSessionEnded
}

// mcpWrites reports whether the MCP server of s lets AI assistants modify data.
func (s *ssmSession) mcpWrites() bool {
	return s.mcp != nil && s.mcp.writes
}

// key identifies the target of s as the wizard's recent connections do (see connectionKey).
func (s *ssmSession) key() string {
	return connectionKey(s.session.Profile, s.session.Region, s.session.ResourceType, s.session.Target)
}

func (s *ssmSession) appendLines(lines ...string) {
	now := time.Now()
	for _, line := range lines {
		s.lines = append(s.lines, ssmLogLine{at: now, text: line})
	}
	if len(s.lines) > maxSessionLogLines {
		s.lines = s.lines[len(s.lines)-maxSessionLogLines:]
	}
}

type ssmModel struct {
	ctx         context.Context
	runner      JumphostSessionRunner
	processes   *processTracker
	events      chan tea.Msg
	wizard      jumphostWizardModel
	styles      Styles
	errorMsg    string
	sessions    []*ssmSession
	loadingUI   loadingState
	baseOptions jumphost.Options
	viewport    viewport.Model
	selected    int
	nextID      int
	frame       int
	width       int
	height      int
	mode        ssmViewMode
	confirmYes  bool
	quitting    bool
	// showQueries shows the MCP queries of the selected session instead of its logs.
	showQueries bool
	// notice is a neutral message shown above the key hints until the next key.
	notice string
	// prepareID identifies the preparation the dashboard waits for (see ssmPreparedMsg.id);
	// cancelPrepare cancels its context, stopping its AWS calls.
	prepareID     int
	cancelPrepare context.CancelFunc
	// copier puts text on the clipboard (copyToClipboard); copyCursor is the copy menu's choice.
	copier     func(text string) error
	copyCursor int
	// password is the password dialog (ssmModePassword).
	password ssmPasswordReveal
	// production flags the AWS profiles of the productionProfiles setting.
	production productionMatcher
}

// RunJumphostSessions shows the running port-forwarding sessions and lets the user open more in parallel.
// first is already prepared; when nil, the dashboard opens on the wizard, as if the user pressed n.
// DashboardOptions are the optional settings of RunJumphostSessions.
type DashboardOptions struct {
	// Handoff, when not nil, suspends the dashboard while it runs a command needing the terminal
	// (the runner's sign-ins).
	Handoff *TerminalHandoff
	// ProductionProfiles are the patterns (* wildcard, any case) of the AWS profiles to flag.
	ProductionProfiles []string
}

// New sessions are prepared with runner, reusing base for their defaults.
func RunJumphostSessions(ctx context.Context, runner JumphostSessionRunner, first *jumphost.Session, base jumphost.Options, out io.Writer, opts DashboardOptions) error {
	handoff := opts.Handoff
	processes := newProcessTracker()
	stopHangupWatch := jumphost.RunOnHangup(processes.shutdown)
	defer stopHangupWatch()

	model := newSSMModel(ctx, runner, processes, base)
	model.production = productionMatcher(opts.ProductionProfiles)
	if first != nil {
		model = model.addSession(first, base)
	} else {
		model = model.startupWizard()
	}

	program := tea.NewProgram(model, tea.WithAltScreen())
	if handoff != nil {
		handoff.attach(program)
		defer handoff.attach(nil)
	}
	finalModel, err := program.Run()
	// Whatever happened, never leave aws processes or IDE data sources behind.
	processes.shutdown()
	if err != nil {
		return err
	}

	result, ok := finalModel.(ssmModel)
	if !ok {
		return nil
	}
	result.printSessionSummary(out)

	// A lone session failing on its own keeps reporting its error, as before parallel sessions existed.
	if len(result.sessions) == 1 && !result.sessions[0].stopRequested {
		return result.sessions[0].err
	}
	return nil
}

func newSSMModel(ctx context.Context, runner JumphostSessionRunner, processes *processTracker, base jumphost.Options) ssmModel {
	return ssmModel{
		ctx:         ctx,
		runner:      runner,
		processes:   processes,
		baseOptions: base,
		events:      make(chan tea.Msg, 256),
		viewport:    viewport.New(0, 0),
		styles:      NewStyles(),
		copier:      copyToClipboard,
		nextID:      1,
	}
}

func (m ssmModel) Init() tea.Cmd {
	for _, s := range m.sessions {
		if s.state == ssmSessionStarting {
			m.launchCommand(s)
		}
	}
	cmds := []tea.Cmd{waitSSMEvent(m.events), ssmTickCmd()}
	if m.mode == ssmModeWizard {
		cmds = append(cmds, m.wizard.Init())
	}
	return tea.Batch(cmds...)
}

func ssmTickCmd() tea.Cmd {
	return tea.Tick(ssmTickInterval, func(t time.Time) tea.Msg { return ssmTickMsg(t) })
}

// addSession registers a session prepared with options as a new tab and selects it. The process is
// launched separately.
func (m ssmModel) addSession(session *jumphost.Session, options jumphost.Options) ssmModel {
	m.sessions = append(m.sessions, m.newSession(session, options, nil, ""))
	m.selected = len(m.sessions) - 1
	m.refreshViewport()
	m.viewport.GotoBottom()
	return m
}

// newSession builds the tab of a prepared session; history holds the log lines of the ended run a
// restart replaces, followed by note.
func (m *ssmModel) newSession(session *jumphost.Session, options jumphost.Options, history []ssmLogLine, note string) *ssmSession {
	s := &ssmSession{
		id:        m.nextID,
		session:   session,
		options:   options,
		state:     ssmSessionStarting,
		startedAt: time.Now(),
		lines:     append([]ssmLogLine{}, history...),
	}
	if server := session.MCPServer(); server != nil {
		s.mcp = subscribeMCPQueries(s.id, server, m.events)
	}
	if note != "" {
		s.appendLines("", note, "")
	}
	s.appendLines(session.Intro...)
	s.appendLines("", "Starting session...")

	m.nextID++
	m.processes.track(session)
	return s
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
		return m, ssmTickCmd()
	case ssmOutputMsg:
		var cmd tea.Cmd
		if s := m.session(msg.id); s != nil {
			wasReady := s.ready
			s.appendLines(msg.line)
			trackPluginOutput(s, msg.line)
			if !wasReady && s.ready {
				cmd = m.startSignIn(s)
			}
			m.refreshSelected(s)
		}
		return m, tea.Batch(waitSSMEvent(m.events), cmd)
	case ssmMCPQueryMsg:
		if s := m.session(msg.id); s != nil && s.mcp != nil {
			s.mcp.add(&msg.event)
			if m.queriesShown(s) {
				m.refreshSelected(s)
			}
		}
		return m, waitSSMEvent(m.events)
	case ssmStartedMsg:
		if s := m.session(msg.id); s != nil {
			s.cmd = msg.cmd
			s.state = ssmSessionRunning
			if s.stopRequested {
				s.state = ssmSessionStopping
				terminateProcessGroup(s.cmd)
			}
		}
		return m, waitSSMEvent(m.events)
	case ssmFinishedMsg:
		return m.handleFinished(msg)
	case ssmPreparedMsg:
		return m.handlePrepared(msg)
	case ssmCopiedMsg:
		return m.handleCopied(msg)
	case ssmPasswordMsg:
		return m.handlePassword(msg)
	case ssmSignInListedMsg:
		return m.handleSignInListed(msg)
	case ssmSignInCheckedMsg:
		return m.handleSignInChecked(msg)
	case ssmPrefsSavedMsg:
		return m.handlePrefsSaved(msg)
	case ssmReconnectMsg:
		return m.handleReconnect(msg)
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
	case tea.KeyMsg:
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
		default:
			return m.handleSessionKey(msg)
		}
	}

	if m.mode == ssmModeWizard {
		return m.updateWizard(msg)
	}
	return m, nil
}

func (m ssmModel) handleSessionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		if s := m.selectedSession(); s != nil && s.mcp != nil {
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
		if !s.active() {
			return m.removeSelected(), nil
		}
		if s.state == ssmSessionStopping {
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
// canceled preparation's session is released when it comes.
func (m ssmModel) handlePreparingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
func (m ssmModel) handleHelpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mode = ssmModeSessions
	m.refreshViewport()
	if msg.String() == "ctrl+c" {
		return m.handleSessionKey(msg)
	}
	return m, nil
}

func (m ssmModel) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	if !confirmed {
		m.refreshViewport()
		return m, nil
	}
	if mode == ssmModeConfirmQuit {
		m.quitting = true
		for _, s := range m.sessions {
			m.stopSession(s)
		}
		if !m.hasActiveSessions() {
			return m, tea.Quit
		}
	} else if s := m.selectedSession(); s != nil {
		m.stopSession(s)
	}
	m.refreshViewport()
	return m, nil
}

func (m ssmModel) handleFinished(msg ssmFinishedMsg) (tea.Model, tea.Cmd) {
	s := m.session(msg.id)
	if s == nil {
		return m, waitSSMEvent(m.events)
	}

	s.state = ssmSessionEnded
	s.endedAt = time.Now()
	s.err = msg.err
	switch {
	case s.stopRequested:
		s.appendLines("", "Session stopped.")
	case msg.err != nil:
		s.appendLines("", "Session ended with error: "+msg.err.Error())
	default:
		s.appendLines("", "Session ended.")
	}
	if cleanup := s.session.Release(); cleanup != "" {
		s.appendLines(cleanup)
	}
	reconnect := m.scheduleReconnect(s)
	m = m.endSignIn(s)
	m.refreshSelected(s)

	// Only "stop all & quit" leaves on its own: an ended session stays on screen with its logs.
	if m.quitting && !m.hasActiveSessions() {
		return m, tea.Quit
	}
	return m, tea.Batch(waitSSMEvent(m.events), reconnect)
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
	return m.showWizard(base)
}

func (m ssmModel) showWizard(base jumphost.Options) ssmModel {
	m.wizard = newJumphostWizardModel(m.ctx, m.runner, base)
	m.wizard.embedded = true
	m.wizard.hostHasSessions = len(m.sessions) > 0
	m.wizard.production = m.production
	m.wizard.active = m.activeConnectionKeys()
	m.mode = ssmModeWizard
	m.errorMsg = ""
	return m
}

func (m ssmModel) openWizard() (tea.Model, tea.Cmd) {
	m = m.showWizard(m.wizardDefaults())

	initCmd := m.wizard.Init()
	if m.width > 0 && m.height > 0 {
		updated, _ := m.wizard.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		if wizard, ok := updated.(jumphostWizardModel); ok {
			m.wizard = wizard
		}
	}
	return m, initCmd
}

// activeSessionIndex returns the position of the running session of key (see connectionKey), or -1.
func (m ssmModel) activeSessionIndex(key string) int {
	for index, s := range m.sessions {
		if s.active() && s.session != nil && s.key() == key {
			return index
		}
	}
	return -1
}

// activeConnectionKeys identifies the targets of the running sessions, as connectionKey builds them.
func (m ssmModel) activeConnectionKeys() map[string]bool {
	keys := map[string]bool{}
	for _, s := range m.sessions {
		if !s.active() || s.session == nil {
			continue
		}
		keys[s.key()] = true
	}
	return keys
}

func (m ssmModel) updateWizard(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.wizard.Update(msg)
	if wizard, ok := updated.(jumphostWizardModel); ok {
		m.wizard = wizard
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
	return m.prepare(ssmPreparedMsg{options: msg.options, warning: msg.warning}, label)
}

// restartSelected prepares the ended selected session again: same target, profile, region, local
// port and sign-in choices, so the tools pointed at it reconnect as they were.
func (m ssmModel) restartSelected() (tea.Model, tea.Cmd) {
	s := m.selectedSession()
	if s == nil || s.active() {
		return m, nil
	}
	session := s.session
	if m.activeSessionIndex(s.key()) >= 0 {
		m.errorMsg = session.Target + " is already connected in another session"
		return m, nil
	}

	// A restart by hand takes over an automatic reconnection, whose result is then released.
	s.stopReconnecting()
	label := fmt.Sprintf("Restarting %s %s...", strings.ToUpper(session.ResourceType), session.Target)
	return m.prepare(ssmPreparedMsg{options: restartOptions(s), replaces: s.id}, label)
}

// restartOptions prepares the session of s again: same target, profile, region, local port and
// sign-in choices, so the tools pointed at it reconnect as they were.
func restartOptions(s *ssmSession) jumphost.Options {
	options := s.options
	options.ResourceType = s.session.ResourceType
	options.Name = s.session.Target
	options.AwsProfile = s.session.Profile
	options.AwsRegion = s.session.Region
	options.LocalPortNumber = s.session.LocalPort
	return options
}

// prepare prepares the session of pending.options in the background; pending carries what the
// result needs besides the session (warning, replaced session).
func (m ssmModel) prepare(pending ssmPreparedMsg, label string) (tea.Model, tea.Cmd) {
	m.mode = ssmModePreparing
	m.prepareID++
	pending.id = m.prepareID
	// Canceled by esc only: a prepared session may keep using its context.
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelPrepare = cancel
	prepare := func() tea.Msg {
		result := pending
		result.session, result.err = m.runner.Prepare(ctx, pending.options)
		return result
	}
	return m, tea.Batch(
		prepare,
		m.loadingUI.start(label, []string{
			"Reserving a cozy local port...",
			"Asking the jumphost for one more tunnel...",
			"Keeping your other sessions warm...",
		}),
	)
}

func (m ssmModel) handlePrepared(msg ssmPreparedMsg) (tea.Model, tea.Cmd) {
	if msg.background {
		return m.handleReconnected(msg)
	}
	// The user canceled this preparation: free what it reserved rather than open it.
	if msg.id != m.prepareID {
		if msg.session != nil {
			msg.session.Release()
		}
		return m, nil
	}
	m.loadingUI.stop()
	m.mode = ssmModeSessions
	if msg.err != nil {
		m.errorMsg = "Could not open connection: " + msg.err.Error()
		if msg.replaces != 0 {
			m.errorMsg = "Could not restart: " + msg.err.Error()
		}
		m.refreshViewport()
		return m, nil
	}

	appendWarning(msg.session, msg.warning)
	// A restart takes the slot of the ended session, keeping its logs above the new run.
	if index := m.sessionIndex(msg.replaces); index >= 0 {
		s := m.newSession(msg.session, msg.options, m.sessions[index].lines, "Restarting session...")
		m.sessions[index] = s
		m = m.selectSession(index)
		m.launchCommand(s)
		return m, nil
	}
	m = m.addSession(msg.session, msg.options)
	m.launchCommand(m.sessions[len(m.sessions)-1])
	return m, nil
}

func (m *ssmModel) stopSession(s *ssmSession) {
	if !s.active() || s.state == ssmSessionStopping {
		return
	}
	s.stopRequested = true
	s.appendLines("", "Stopping session...")
	if s.cmd != nil {
		s.state = ssmSessionStopping
		terminateProcessGroup(s.cmd)
	}
	m.refreshSelected(s)
}

func (m ssmModel) removeSelected() ssmModel {
	if m.selected < 0 || m.selected >= len(m.sessions) {
		return m
	}
	m.sessions[m.selected].stopReconnecting()
	m.sessions = append(append([]*ssmSession{}, m.sessions[:m.selected]...), m.sessions[m.selected+1:]...)
	return m.selectSession(m.selected)
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

func (m ssmModel) session(id int) *ssmSession {
	if index := m.sessionIndex(id); index >= 0 {
		return m.sessions[index]
	}
	return nil
}

// sessionIndex returns the position of the session id in the sidebar, or -1.
func (m ssmModel) sessionIndex(id int) int {
	for index, s := range m.sessions {
		if s.id == id {
			return index
		}
	}
	return -1
}

func (m ssmModel) selectedSession() *ssmSession {
	if m.selected < 0 || m.selected >= len(m.sessions) {
		return nil
	}
	return m.sessions[m.selected]
}

func (m ssmModel) hasActiveSessions() bool {
	for _, s := range m.sessions {
		if s.active() {
			return true
		}
	}
	return false
}

// trackPluginOutput derives readiness and connection count from session-manager-plugin output.
func trackPluginOutput(s *ssmSession, line string) {
	switch {
	case strings.Contains(line, "Waiting for connections"):
		s.ready = true
	case strings.Contains(line, "Connection accepted"):
		s.ready = true
		s.connections++
	}
}

// refreshSelected redraws the log when s is the visible session, following the tail if already at the bottom.
func (m *ssmModel) refreshSelected(s *ssmSession) {
	if m.selectedSession() != s {
		return
	}
	atBottom := m.viewport.AtBottom()
	m.refreshViewport()
	if atBottom {
		m.viewport.GotoBottom()
	}
}

// launchCommand starts the aws process of s and streams its output as tea messages.
func (m ssmModel) launchCommand(s *ssmSession) {
	id := s.id
	awsArgs := append(append([]string{}, s.session.CommandArgs...), s.session.AwsArgs...)
	events := m.events
	processes := m.processes
	ctx := m.ctx

	go func() {
		// Use exec.Command (not CommandContext) so we can manage the process group
		// ourselves: aws and its child session-manager-plugin must be stopped together.
		cmd := exec.Command("aws", awsArgs...) // #nosec G204 -- arguments are built by the jumphost runner.
		platform.StartInOwnGroup(cmd)
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			events <- ssmFinishedMsg{id: id, err: err}
			return
		}
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			events <- ssmFinishedMsg{id: id, err: err}
			return
		}

		if err := cmd.Start(); err != nil {
			events <- ssmFinishedMsg{id: id, err: err}
			return
		}
		processes.started(cmd)
		defer processes.exited(cmd)

		events <- ssmStartedMsg{id: id, cmd: cmd}

		// Watch for context cancellation and kill the entire process group so
		// session-manager-plugin (child of aws) is also terminated.
		ctxDone := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				terminateProcessGroup(cmd)
			case <-ctxDone:
			}
		}()

		var wg sync.WaitGroup
		wg.Add(2)
		go readPipeLines(id, stdoutPipe, &wg, events)
		go readPipeLines(id, stderrPipe, &wg, events)
		wg.Wait()
		close(ctxDone)

		events <- ssmFinishedMsg{id: id, err: cmd.Wait()}
	}()
}

func readPipeLines(id int, reader io.Reader, wg *sync.WaitGroup, events chan<- tea.Msg) {
	defer wg.Done()

	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		events <- ssmOutputMsg{id: id, line: scanner.Text()}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		events <- ssmOutputMsg{id: id, line: "stream error: " + err.Error()}
	}
}

func waitSSMEvent(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-events
	}
}

func terminateProcessGroup(cmd *exec.Cmd) {
	platform.TerminateGroup(cmd)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m ssmModel) printSessionSummary(out io.Writer) {
	if out == nil || len(m.sessions) == 0 {
		return
	}

	_, _ = fmt.Fprintf(out, "\nSession summary:\n")
	for _, s := range m.sessions {
		end := s.endedAt
		if end.IsZero() {
			end = time.Now()
		}
		duration := max(end.Sub(s.startedAt).Round(time.Second), 0)

		server := strings.TrimSpace(s.session.Details.ConnectedServer)
		if server == "" {
			server = "n/a"
		}

		_, _ = fmt.Fprintf(out, "- %s -> localhost:%d\n", s.session.Label, s.session.LocalPort)
		_, _ = fmt.Fprintf(out, "  duration: %s\n  server connected: %s\n", duration, server)
		if s.err != nil && !s.stopRequested {
			_, _ = fmt.Fprintf(out, "  ended with error: %v\n", s.err)
		}
		if cleanup := s.session.Release(); cleanup != "" {
			_, _ = fmt.Fprintf(out, "  %s\n", cleanup)
		}
		if strings.TrimSpace(s.session.Details.RestartCommand) != "" {
			_, _ = fmt.Fprintf(out, "  new session command: %s\n", s.session.Details.RestartCommand)
		}
	}
}

// appendWarning adds warning, if any, as the last intro line of session.
func appendWarning(session *jumphost.Session, warning string) {
	if warning != "" {
		session.Intro = append(session.Intro, "Warning: "+warning)
	}
}

// processTracker remembers running aws processes and prepared sessions so they can be torn down
// from outside the TUI loop (terminal hangup, program exit).
type processTracker struct {
	commands map[*exec.Cmd]struct{}
	sessions []*jumphost.Session
	mu       sync.Mutex
}

func newProcessTracker() *processTracker {
	return &processTracker{commands: map[*exec.Cmd]struct{}{}}
}

func (t *processTracker) track(session *jumphost.Session) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessions = append(t.sessions, session)
}

func (t *processTracker) started(cmd *exec.Cmd) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.commands[cmd] = struct{}{}
}

func (t *processTracker) exited(cmd *exec.Cmd) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.commands, cmd)
}

// shutdown terminates every running aws process and releases every session. Safe to call repeatedly.
func (t *processTracker) shutdown() {
	t.mu.Lock()
	commands := make([]*exec.Cmd, 0, len(t.commands))
	for cmd := range t.commands {
		commands = append(commands, cmd)
	}
	sessions := append([]*jumphost.Session{}, t.sessions...)
	t.mu.Unlock()

	for _, cmd := range commands {
		terminateProcessGroup(cmd)
	}
	for _, session := range sessions {
		session.Release()
	}
}
