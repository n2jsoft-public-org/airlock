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

type ssmOutputMsg struct {
	line string
}

type ssmFinishedMsg struct {
	err error
}

type ssmStartedMsg struct {
	cmd *exec.Cmd
}

type ssmModel struct {
	ctx         context.Context
	awsArgs     []string
	commandArgs []string
	details     jumphost.SessionDetails
	out         io.Writer
	events      chan tea.Msg
	viewport    viewport.Model
	lines       []string
	width       int
	height      int
	confirmQuit bool
	confirmYes  bool
	stopping    bool
	finished    bool
	finishErr   error
	runningCmd  *exec.Cmd
	startedAt   time.Time
	endedAt     time.Time
	styles      Styles
}

func RunSSMSessionTUI(ctx context.Context, awsArgs []string, commandArgs []string, details jumphost.SessionDetails, _ io.Reader, out io.Writer, errOut io.Writer) error {
	_ = errOut

	model := ssmModel{
		ctx:         ctx,
		awsArgs:     append([]string{}, awsArgs...),
		commandArgs: append([]string{}, commandArgs...),
		details:     details,
		out:         out,
		events:      make(chan tea.Msg, 256),
		styles:      NewStyles(),
	}

	program := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return err
	}

	result, ok := finalModel.(ssmModel)
	if !ok {
		return nil
	}

	if result.finishErr != nil {
		if result.stopping {
			result.printSessionSummary()
			return nil
		}
		return result.finishErr
	}

	if result.finished || result.stopping {
		result.printSessionSummary()
	}

	return nil
}

func (m ssmModel) Init() tea.Cmd {
	m.startedAt = time.Now()
	m.viewport = viewport.New(0, 0)
	m.viewport.SetContent("Starting session...")
	m.launchCommand()
	return waitSSMEvent(m.events)
}

func (m ssmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = maxInt(1, msg.Width-4)
		headerLines := 8
		if strings.TrimSpace(m.details.IDEDataSource) != "" {
			headerLines++
		}
		m.viewport.Height = maxInt(3, msg.Height-headerLines)
		m.refreshViewport()
		return m, nil
	case ssmOutputMsg:
		m.lines = append(m.lines, msg.line)
		if len(m.lines) > 1000 {
			m.lines = m.lines[len(m.lines)-1000:]
		}
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, waitSSMEvent(m.events)
	case ssmFinishedMsg:
		m.finished = true
		m.endedAt = time.Now()
		m.finishErr = msg.err
		if msg.err != nil {
			m.lines = append(m.lines, "", "Session ended with error: "+msg.err.Error())
		} else {
			m.lines = append(m.lines, "", "Session ended.")
		}
		m.refreshViewport()
		return m, tea.Quit
	case ssmStartedMsg:
		m.runningCmd = msg.cmd
		return m, waitSSMEvent(m.events)
	case tea.KeyMsg:
		if m.confirmQuit {
			return m.handleConfirmKey(msg)
		}

		switch msg.String() {
		case "ctrl+c":
			if m.finished {
				return m, tea.Quit
			}
			m.confirmQuit = true
			m.confirmYes = true
			return m, nil
		case "pgup":
			m.viewport.HalfViewUp()
			return m, nil
		case "pgdown":
			m.viewport.HalfViewDown()
			return m, nil
		case "up":
			m.viewport.LineUp(1)
			return m, nil
		case "down":
			m.viewport.LineDown(1)
			return m, nil
		}
	}

	return m, nil
}

func (m ssmModel) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "right", "tab":
		m.confirmYes = !m.confirmYes
		return m, nil
	case "esc":
		m.confirmQuit = false
		return m, nil
	case "enter":
		if !m.confirmYes {
			m.confirmQuit = false
			return m, nil
		}
		m.confirmQuit = false
		m.stopping = true
		if m.runningCmd != nil && m.runningCmd.Process != nil {
			_ = syscall.Kill(-m.runningCmd.Process.Pid, syscall.SIGTERM)
		}
		return m, waitSSMEvent(m.events)
	default:
		return m, nil
	}
}

func (m ssmModel) View() string {
	title := m.styles.Title.Render("SSM Port Forward Session")
	subtitle := m.styles.Subtitle.Render("Ctrl+C: quit confirmation")
	body := m.viewport.View()

	if strings.TrimSpace(body) == "" {
		body = "Waiting for output..."
	}

	content := title + "\n" + subtitle
	if strings.TrimSpace(m.details.IDEDataSource) != "" {
		content += "\n" + m.styles.Muted.Render(m.details.IDEDataSource)
	}
	content += "\n\n" + body
	if m.confirmQuit {
		yes := "[ Yes ]"
		no := "[ No ]"
		if m.confirmYes {
			yes = m.styles.Subtitle.Render("[ Yes ]")
		} else {
			no = m.styles.Subtitle.Render("[ No ]")
		}
		content += "\n\n" + m.styles.Title.Render("Do you want to quit?") + "\n" + yes + "  " + no
		if strings.TrimSpace(m.details.RestartCommand) != "" {
			content += "\n" + m.styles.Muted.Render("New session: "+m.details.RestartCommand)
		}
	}

	if m.stopping && !m.finished {
		content += "\n\n" + m.styles.Muted.Render("Stopping session...")
	}

	return m.styles.Container.Render(content)
}

func (m *ssmModel) launchCommand() {
	go func() {
		args := append([]string{}, m.commandArgs...)
		args = append(args, m.awsArgs...)

		// Use exec.Command (not CommandContext) so we can manage the process group
		// ourselves. Setting Setpgid ensures aws and its child session-manager-plugin
		// are in the same process group and can be killed together.
		cmd := exec.Command("aws", args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			m.events <- ssmFinishedMsg{err: err}
			close(m.events)
			return
		}
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			m.events <- ssmFinishedMsg{err: err}
			close(m.events)
			return
		}

		if err := cmd.Start(); err != nil {
			m.events <- ssmFinishedMsg{err: err}
			close(m.events)
			return
		}

		m.events <- ssmStartedMsg{cmd: cmd}

		// Watch for context cancellation and kill the entire process group so
		// session-manager-plugin (child of aws) is also terminated.
		ctxDone := make(chan struct{})
		go func() {
			select {
			case <-m.ctx.Done():
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			case <-ctxDone:
			}
		}()

		var wg sync.WaitGroup
		wg.Add(2)
		go readPipeLines(stdoutPipe, &wg, m.events)
		go readPipeLines(stderrPipe, &wg, m.events)
		wg.Wait()
		close(ctxDone)

		err = cmd.Wait()
		if err != nil {
			m.events <- ssmFinishedMsg{err: err}
		} else {
			m.events <- ssmFinishedMsg{err: nil}
		}
		close(m.events)
	}()
}

func (m *ssmModel) refreshViewport() {
	if m.viewport.Width <= 0 || m.viewport.Height <= 0 {
		return
	}
	m.viewport.SetContent(strings.Join(m.lines, "\n"))
}

func readPipeLines(reader io.Reader, wg *sync.WaitGroup, events chan<- tea.Msg) {
	defer wg.Done()

	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		events <- ssmOutputMsg{line: scanner.Text()}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		events <- ssmOutputMsg{line: "stream error: " + err.Error()}
	}
}

func waitSSMEvent(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-events
		if !ok {
			return ssmFinishedMsg{err: nil}
		}
		return msg
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m ssmModel) printSessionSummary() {
	if m.out == nil {
		return
	}

	start := m.startedAt
	if start.IsZero() {
		start = time.Now()
	}
	end := m.endedAt
	if end.IsZero() {
		end = time.Now()
	}
	duration := end.Sub(start).Round(time.Second)
	if duration < 0 {
		duration = 0
	}

	server := strings.TrimSpace(m.details.ConnectedServer)
	if server == "" {
		server = "n/a"
	}

	_, _ = fmt.Fprintf(m.out, "\nSession summary:\n- duration: %s\n- server connected: %s\n", duration, server)

	if strings.TrimSpace(m.details.IDEDataSource) != "" {
		_, _ = fmt.Fprintf(m.out, "- %s\n", m.details.IDEDataSource)
	}

	if strings.TrimSpace(m.details.RestartCommand) != "" {
		_, _ = fmt.Fprintf(m.out, "- new session command: %s\n", m.details.RestartCommand)
	}
}
