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
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/example/acme-cli/internal/jumphost"
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
	// mcp holds the calls made through the session's MCP server (nil when none runs).
	mcp           *ssmMCPQueries
	lines         []ssmLogLine
	id            int
	connections   int
	state         ssmSessionState
	stopRequested bool
	// ready is set once session-manager-plugin listens on the local port.
	ready bool
}

func (s *ssmSession) active() bool {
	return s.state != ssmSessionEnded
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
}

// RunJumphostSessions shows the running port-forwarding sessions and lets the user open more in parallel.
// first is already prepared; new sessions are prepared with runner, reusing base for their defaults.
func RunJumphostSessions(ctx context.Context, runner JumphostSessionRunner, first *jumphost.Session, base jumphost.Options, out io.Writer) error {
	processes := newProcessTracker()
	stopHangupWatch := jumphost.RunOnHangup(processes.shutdown)
	defer stopHangupWatch()

	model := newSSMModel(ctx, runner, processes, base)
	model = model.addSession(first)

	program := tea.NewProgram(model, tea.WithAltScreen())
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
		nextID:      1,
	}
}

func (m ssmModel) Init() tea.Cmd {
	for _, s := range m.sessions {
		if s.state == ssmSessionStarting {
			m.launchCommand(s)
		}
	}
	return tea.Batch(waitSSMEvent(m.events), ssmTickCmd())
}

func ssmTickCmd() tea.Cmd {
	return tea.Tick(ssmTickInterval, func(t time.Time) tea.Msg { return ssmTickMsg(t) })
}

// addSession registers a prepared session as a new tab and selects it. The process is launched separately.
func (m ssmModel) addSession(session *jumphost.Session) ssmModel {
	s := &ssmSession{
		id:        m.nextID,
		session:   session,
		state:     ssmSessionStarting,
		startedAt: time.Now(),
	}
	if server := session.MCPServer(); server != nil {
		s.mcp = subscribeMCPQueries(s.id, server, m.events)
	}
	s.appendLines(session.Intro...)
	s.appendLines("", "Starting session...")

	m.nextID++
	m.processes.track(session)
	m.sessions = append(m.sessions, s)
	m.selected = len(m.sessions) - 1
	m.refreshViewport()
	m.viewport.GotoBottom()
	return m
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
		if s := m.session(msg.id); s != nil {
			s.appendLines(msg.line)
			trackPluginOutput(s, msg.line)
			m.refreshSelected(s)
		}
		return m, waitSSMEvent(m.events)
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
			return m, nil
		case ssmModeConfirmQuit, ssmModeConfirmStop:
			return m.handleConfirmKey(msg)
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

	if m.errorMsg != "" {
		m.errorMsg = ""
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

func (m ssmModel) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "right", "tab":
		m.confirmYes = !m.confirmYes
		return m, nil
	case "esc":
		m.mode = ssmModeSessions
		m.refreshViewport()
		return m, nil
	case "enter":
		confirmed := m.confirmYes
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
	default:
		return m, nil
	}
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
	m.refreshSelected(s)

	// Leave once nothing runs anymore, unless the user is busy opening another connection.
	if !m.hasActiveSessions() && (m.quitting || m.mode == ssmModeSessions || m.mode == ssmModeConfirmStop) {
		return m, tea.Quit
	}
	return m, waitSSMEvent(m.events)
}

func (m ssmModel) openWizard() (tea.Model, tea.Cmd) {
	base := jumphost.Options{
		SkipIDEDataSource: m.baseOptions.SkipIDEDataSource,
		SkipMCP:           m.baseOptions.SkipMCP,
		MCPAllowWrites:    m.baseOptions.MCPAllowWrites,
		DBUser:            m.baseOptions.DBUser,
	}
	m.wizard = newJumphostWizardModel(m.ctx, m.runner, base)
	m.wizard.embedded = true
	m.mode = ssmModeWizard
	m.errorMsg = ""

	initCmd := m.wizard.Init()
	if m.width > 0 && m.height > 0 {
		updated, _ := m.wizard.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		if wizard, ok := updated.(jumphostWizardModel); ok {
			m.wizard = wizard
		}
	}
	return m, initCmd
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
		m.refreshViewport()
		if !m.hasActiveSessions() {
			return m, tea.Quit
		}
		return m, nil
	}

	m.mode = ssmModePreparing
	options := msg.options
	prepare := func() tea.Msg {
		session, err := m.runner.Prepare(m.ctx, options)
		return ssmPreparedMsg{session: session, err: err}
	}
	return m, tea.Batch(
		prepare,
		m.loadingUI.start(fmt.Sprintf("Preparing %s %s...", strings.ToUpper(options.ResourceType), options.Name), []string{
			"Reserving a cozy local port...",
			"Asking the jumphost for one more tunnel...",
			"Keeping your other sessions warm...",
		}),
	)
}

func (m ssmModel) handlePrepared(msg ssmPreparedMsg) (tea.Model, tea.Cmd) {
	m.loadingUI.stop()
	m.mode = ssmModeSessions
	if msg.err != nil {
		m.errorMsg = "Could not open connection: " + msg.err.Error()
		m.refreshViewport()
		return m, nil
	}

	m = m.addSession(msg.session)
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
	for _, s := range m.sessions {
		if s.id == id {
			return s
		}
	}
	return nil
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
		// ourselves. Setting Setpgid ensures aws and its child session-manager-plugin
		// are in the same process group and can be killed together.
		cmd := exec.Command("aws", awsArgs...) // #nosec G204 -- arguments are built by the jumphost runner.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
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
