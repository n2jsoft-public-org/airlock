// Package sessionstest provides fakes of the runner and the aws processes, and the contract every
// sessions.Service passes.
package sessionstest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// Runner prepares sessions from Sessions (by Options.Name), or fails with Errors[name].
type Runner struct {
	Sessions map[string]*jumphost.Session
	Errors   map[string]error
	// Block, when not nil, makes Prepare wait for it (or the context) before answering.
	Block chan struct{}
	// OnPrepare, when set, runs at the start of Prepare; its error fails the preparation.
	OnPrepare func(ctx context.Context) error
	Prepared  []jumphost.Options
	Saved     []jumphost.Options
	mu        sync.Mutex
}

// NewRunner returns a runner without sessions.
func NewRunner() *Runner {
	return &Runner{Sessions: map[string]*jumphost.Session{}, Errors: map[string]error{}}
}

// AWS registers an aws session named name of resourceType.
func (r *Runner) AWS(name, resourceType string) *jumphost.Session {
	s := &jumphost.Session{Label: name, ResourceType: resourceType, Target: name, Profile: "dev", Region: "eu-west-3",
		LocalPort: 15432, AwsArgs: []string{"ssm", "start-session"}, Intro: []string{"Connecting to " + name}}
	r.mu.Lock()
	r.Sessions[name] = s
	r.mu.Unlock()
	return s
}

// Direct registers a direct database session named name.
func (r *Runner) Direct(name string) *jumphost.Session {
	s := &jumphost.Session{Label: name, ResourceType: sessions.DirectResourceType, Target: name,
		Details: jumphost.SessionDetails{ConnectedServer: name + ".internal:5432"}}
	r.mu.Lock()
	r.Sessions[name] = s
	r.mu.Unlock()
	return s
}

func (r *Runner) Prepare(ctx context.Context, o jumphost.Options) (*jumphost.Session, error) { //nolint:gocritic // implements sessions.Runner.
	r.mu.Lock()
	r.Prepared = append(r.Prepared, o)
	block := r.Block
	onPrepare := r.OnPrepare
	r.mu.Unlock()
	if onPrepare != nil {
		if err := onPrepare(ctx); err != nil {
			return nil, err
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.Errors[o.Name]; err != nil {
		return nil, err
	}
	s, ok := r.Sessions[o.Name]
	if !ok {
		return nil, errors.New("no such target " + o.Name)
	}
	// The registered session itself: jumphost.Session holds a sync.Once and must not be copied.
	return s, nil
}

func (r *Runner) SaveConnectionPrefs(_ context.Context, o jumphost.Options) error { //nolint:gocritic // implements sessions.Runner.
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Saved = append(r.Saved, o)
	return nil
}

// PreparedOptions returns a copy of the options Prepare was called with.
func (r *Runner) PreparedOptions() []jumphost.Options {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]jumphost.Options(nil), r.Prepared...)
}

// Launcher starts fake processes the test drives.
type Launcher struct {
	started chan *Process
}

// NewLauncher returns a launcher whose processes the test gets with Next.
func NewLauncher() *Launcher { return &Launcher{started: make(chan *Process, 64)} }

// Launch implements sessions.Launcher.
func (l *Launcher) Launch(args []string, output func(string)) (sessions.Process, error) {
	p := &Process{Args: args, output: output, exit: make(chan error, 1)}
	l.started <- p
	return p, nil
}

// Next returns the next started process, failing the test after 5 seconds.
func (l *Launcher) Next(t testing.TB) *Process {
	t.Helper()
	select {
	case p := <-l.started:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no aws process started")
		return nil
	}
}

// Process is a fake aws process.
type Process struct {
	output     func(string)
	exit       chan error
	Args       []string
	mu         sync.Mutex
	terminated bool
}

func (p *Process) PID() int { return 4242 }

// Print writes line on the process output.
func (p *Process) Print(line string) { p.output(line) }

// Exit ends the process with err.
func (p *Process) Exit(err error) {
	select {
	case p.exit <- err:
	default:
	}
}

func (p *Process) Wait() error { return <-p.exit }

func (p *Process) Terminate() {
	p.mu.Lock()
	p.terminated = true
	p.mu.Unlock()
	p.Exit(errors.New("signal: terminated"))
}

// Terminated reports whether Terminate was called.
func (p *Process) Terminated() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminated
}

// Wait returns the first event match accepts, failing the test after 5 seconds.
func Wait(t testing.TB, events <-chan sessions.Event, match func(sessions.Event) bool) sessions.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("events closed")
			}
			if match(ev) {
				return ev
			}
		case <-timeout:
			t.Fatal("event not received")
			return sessions.Event{}
		}
	}
}

// State matches an EventUpdated, EventAdded or EventReplaced of session id in state.
func State(id sessions.ID, state sessions.State) func(sessions.Event) bool {
	return func(ev sessions.Event) bool {
		return ev.ID == id && ev.Session != nil && ev.Session.State == state
	}
}

// Line matches an EventLines of session id holding text.
func Line(id sessions.ID, text string) func(sessions.Event) bool {
	return func(ev sessions.Event) bool {
		if ev.Kind != sessions.EventLines || ev.ID != id {
			return false
		}
		for _, l := range ev.Lines {
			if l.Text == text {
				return true
			}
		}
		return false
	}
}

// SavedCount returns how many times SaveConnectionPrefs was called.
func (r *Runner) SavedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.Saved)
}

// SavedOptions returns a copy of the options SaveConnectionPrefs was called with.
func (r *Runner) SavedOptions() []jumphost.Options {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]jumphost.Options(nil), r.Saved...)
}

// Pending returns how many started processes Next has not returned yet.
func (l *Launcher) Pending() int { return len(l.started) }

func sleepBriefly() { time.Sleep(5 * time.Millisecond) }

// MCPServer is a session MCP server the gateway can relay to.
type MCPServer struct {
	S      *mcp.Server
	Writes bool
}

func (s *MCPServer) Display() string                    { return "fake MCP server" }
func (s *MCPServer) Subscribe(func(mcpserve.ToolEvent)) {}
func (s *MCPServer) QueryLogDir() string                { return "" }
func (s *MCPServer) AllowsWrites() bool                 { return s.Writes }
func (s *MCPServer) Close() string                      { return "" }
func (s *MCPServer) Server() *mcp.Server                { return s.S }

// WithMCP gives the registered session name the MCP server server.
func (r *Runner) WithMCP(name string, server *MCPServer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Sessions[name].SetMCPServer(server)
}
