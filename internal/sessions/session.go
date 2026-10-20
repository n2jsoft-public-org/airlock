// Package sessions runs the port-forwarding sessions of a dashboard, or of the daemon: it prepares
// them with the runner, runs their aws process, keeps their log, checks their sign-in, reconnects
// them and stops them. Dashboards follow them through snapshots and events.
package sessions

import (
	"maps"
	"strings"
	"time"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

const (
	// MaxLogLines caps the log kept per session.
	MaxLogLines = 1000
	// MaxMCPQueries caps the MCP tool calls kept per session; the audit log keeps them all.
	MaxMCPQueries = 500
	// DirectResourceType is the type of direct database sessions: they run no aws process.
	DirectResourceType = "direct"
	// DockerResourceType is the type of the sessions of local Docker databases: like direct ones,
	// they run no aws process.
	DockerResourceType = "docker"
	// DockerCacheResourceType is the type of the sessions of local Redis / Valkey containers: they
	// run no aws process, and serve the cache tools.
	DockerCacheResourceType = "docker-cache"
	// DynamoDBResourceType is the type of DynamoDB sessions: an AWS profile and region reached
	// through the AWS API, with no aws process to run but an AWS identity to watch.
	DynamoDBResourceType = "dynamodb"
	// S3ResourceType is the type of S3 sessions: every bucket of an AWS profile, reached through the
	// AWS API, with no aws process to run but an AWS identity to watch.
	S3ResourceType = "s3"
)

// ID identifies a session; a restart or reconnection keeps it.
type ID int

// State is where a session is in its life.
type State int

const (
	StateStarting State = iota
	StateRunning
	StateStopping
	StateEnded
)

// SignInState is where the database sign-in check of a session is.
type SignInState int

const (
	// SignInIdle means not started (tunnel not ready yet, or no sign-in to check).
	SignInIdle SignInState = iota
	SignInListing
	SignInChoosing
	SignInChecking
	SignInSkipped
	SignInDone
	SignInFailed
)

// LogLine is one line of a session's log.
type LogLine struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// SignIn is the sign-in check of a session (see jumphost.SignInChecker).
type SignIn struct {
	// Failed holds the error of each candidate tried in vain, by CandidateKey.
	Failed map[string]string `json:"failed,omitempty"`
	// LocalFailed is Failed in the user's language, for the dashboards.
	LocalFailed map[string]string `json:"localFailed,omitempty"`
	User        string            `json:"user,omitempty"`
	Err         string            `json:"err,omitempty"`
	// LocalErr is Err in the user's language, for the dashboards; Err stays English for agents.
	LocalErr   string                     `json:"localErr,omitempty"`
	Candidates []jumphost.SignInCandidate `json:"candidates,omitempty"`
	State      SignInState                `json:"state"`
}

// CandidateKey identifies a sign-in candidate in SignIn.Failed.
func CandidateKey(c *jumphost.SignInCandidate) string {
	return c.SecretARN + "|" + c.Label
}

// Untried returns the indexes of the candidates not tried in vain yet.
func (s *SignIn) Untried() []int {
	var indexes []int
	for idx := range s.Candidates {
		if _, failed := s.Failed[CandidateKey(&s.Candidates[idx])]; !failed {
			indexes = append(indexes, idx)
		}
	}
	return indexes
}

// MCP holds the recent tool calls of a session's MCP server.
type MCP struct {
	// LogDir is where every call is logged (empty when the audit log is disabled).
	LogDir string `json:"logDir,omitempty"`
	// Events are the most recent calls, at most MaxMCPQueries; Total counts them all.
	Events []mcpserve.ToolEvent `json:"events,omitempty"`
	Total  int                  `json:"total"`
	// Writes reports that the server may modify data (the execute tool is offered).
	Writes bool `json:"writes,omitempty"`
}

// Session is what a dashboard shows of one session: plain data, encodable as JSON.
type Session struct {
	StartedAt   time.Time `json:"startedAt"`
	EndedAt     time.Time `json:"endedAt"`
	ReconnectAt time.Time `json:"reconnectAt"`
	// MCP is nil when no MCP server runs for the session.
	MCP *MCP `json:"mcp,omitempty"`
	// Options prepared the session, without the password; a restart prepares them again.
	Options jumphost.Options        `json:"options"`
	Details jumphost.SessionDetails `json:"details"`
	Lines   []LogLine               `json:"lines,omitempty"`
	Label   string                  `json:"label"`
	// ResourceType (eks, rds, elasticache, direct), Target, Profile and Region describe the connection.
	ResourceType string `json:"resourceType"`
	Target       string `json:"target"`
	Profile      string `json:"profile"`
	Region       string `json:"region"`
	// Err is the error the session ended on; empty when none.
	Err string `json:"err,omitempty"`
	// LocalErr is Err in the user's language, for the dashboards; Err stays English for agents.
	LocalErr string `json:"localErr,omitempty"`
	// Cleanup describes what ending the session removed (IDE data source, MCP entries).
	Cleanup string `json:"cleanup,omitempty"`
	// OpenedBy names the AI agent that opened the session through the MCP gateway; empty for the user.
	OpenedBy    string `json:"openedBy,omitempty"`
	SignIn      SignIn `json:"signIn"`
	ID          ID     `json:"id"`
	LocalPort   int    `json:"localPort"`
	Connections int    `json:"connections"`
	// Reconnects counts the automatic reconnections in a row.
	Reconnects int   `json:"reconnects"`
	State      State `json:"state"`
	// Environment is a direct database's environment; Production stays for the dashboards of earlier versions.
	Environment   string `json:"environment,omitempty"`
	Production    bool   `json:"production,omitempty"`
	Ready         bool   `json:"ready"`
	StopRequested bool   `json:"stopRequested,omitempty"`
	Reconnecting  bool   `json:"reconnecting,omitempty"`
	// AWSSignInExpired reports a running session whose AWS sign-in expired: the tunnel stays open,
	// but the tools signing in with AWS through it fail until SignInAWS.
	AWSSignInExpired bool `json:"awsSignInExpired,omitempty"`
	// HasSignIn reports a database sign-in to check; HasCredentials, credentials the password
	// dialog can read.
	HasSignIn      bool `json:"hasSignIn,omitempty"`
	HasCredentials bool `json:"hasCredentials,omitempty"`
}

// Active reports whether the session has not ended.
func (s *Session) Active() bool { return s.State != StateEnded }

// Direct reports a session reached without tunnel (a direct database, or a Docker database or
// cache): it runs no aws process.
func (s *Session) Direct() bool { return direct(s.ResourceType) }

// NoTunnel reports a session running no aws process: a direct or Docker session, or an API-only one
// (DynamoDB, S3), which, unlike them, has an AWS profile and region.
func (s *Session) NoTunnel() bool { return noTunnel(s.ResourceType) }

func direct(resourceType string) bool {
	return resourceType == DirectResourceType || resourceType == DockerResourceType || resourceType == DockerCacheResourceType
}

func noTunnel(resourceType string) bool {
	return direct(resourceType) || jumphost.APIOnly(resourceType)
}

// ReconnectPending reports a session waiting for, or going through, an automatic reconnection.
func (s *Session) ReconnectPending() bool { return s.Reconnecting || !s.ReconnectAt.IsZero() }

// MCPWrites reports whether the session's MCP server lets AI assistants modify data.
func (s *Session) MCPWrites() bool { return s.MCP != nil && s.MCP.Writes }

// Key identifies the target of s: two running sessions never share one.
func (s *Session) Key() string {
	if s.Direct() {
		return s.ResourceType + "|" + strings.ToLower(s.Target)
	}
	if s.ResourceType == S3ResourceType {
		// An S3 session covers every region of its profile.
		return strings.ToLower(strings.Join([]string{s.Profile, s.ResourceType, s.Target}, "|"))
	}
	return strings.ToLower(strings.Join([]string{s.Profile, s.Region, s.ResourceType, s.Target}, "|"))
}

// Clone returns a copy of s sharing no slice nor map with it.
func (s *Session) Clone() Session {
	c := *s
	c.Lines = append([]LogLine(nil), s.Lines...)
	c.SignIn.Candidates = append([]jumphost.SignInCandidate(nil), s.SignIn.Candidates...)
	c.SignIn.Failed = maps.Clone(s.SignIn.Failed)
	c.SignIn.LocalFailed = maps.Clone(s.SignIn.LocalFailed)
	if s.MCP != nil {
		mcp := *s.MCP
		mcp.Events = append([]mcpserve.ToolEvent(nil), s.MCP.Events...)
		c.MCP = &mcp
	}
	return c
}

func appendLines(lines, more []LogLine) []LogLine {
	lines = append(lines, more...)
	if len(lines) > MaxLogLines {
		lines = append([]LogLine(nil), lines[len(lines)-MaxLogLines:]...)
	}
	return lines
}

func appendQuery(mcp *MCP, query *mcpserve.ToolEvent) {
	mcp.Events = append(mcp.Events, *query)
	mcp.Total++
	if len(mcp.Events) > MaxMCPQueries {
		mcp.Events = append([]mcpserve.ToolEvent(nil), mcp.Events[len(mcp.Events)-MaxMCPQueries:]...)
	}
}
