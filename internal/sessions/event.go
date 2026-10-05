package sessions

import "github.com/n2jsoft/airlock/internal/mcpserve"

// EventKind tells what an Event changes.
type EventKind string

const (
	// EventAdded announces a new session; Session holds it whole.
	EventAdded EventKind = "added"
	// EventReplaced tells that a restart or reconnection took the place of session ID; Session holds
	// it whole.
	EventReplaced EventKind = "replaced"
	// EventUpdated tells that Session holds session ID without its log nor MCP calls, which the
	// subscriber keeps.
	EventUpdated EventKind = "updated"
	// EventLines tells that Lines were appended to the log of session ID.
	EventLines EventKind = "lines"
	// EventMCPQuery tells that Query was made through the MCP server of session ID.
	EventMCPQuery EventKind = "mcp-query"
	// EventRemoved tells that session ID was closed.
	EventRemoved EventKind = "removed"
	// EventNotice carries Notice, a neutral message for the dashboards.
	EventNotice EventKind = "notice"
	// EventPasswordRejected tells that the direct database of session ID rejected the password of
	// User; the session ended, and the dashboard showing it may ask for the password of Name again.
	EventPasswordRejected EventKind = "password-rejected"
	// EventResync tells that the subscriber missed events; it takes a new snapshot.
	EventResync EventKind = "resync"
	// EventClosed tells that the sessions are gone (daemon stopped, connection lost); Err says why.
	EventClosed EventKind = "closed"
)

// Event is one change of the sessions. Seq orders the events and snapshots of one service.
type Event struct {
	Session *Session            `json:"session,omitempty"`
	Query   *mcpserve.ToolEvent `json:"query,omitempty"`
	Kind    EventKind           `json:"kind"`
	Notice  string              `json:"notice,omitempty"`
	Name    string              `json:"name,omitempty"`
	User    string              `json:"user,omitempty"`
	Err     string              `json:"err,omitempty"`
	Lines   []LogLine           `json:"lines,omitempty"`
	Seq     uint64              `json:"seq"`
	ID      ID                  `json:"id,omitempty"`
}

// Snapshot is every session at event Seq: apply only the events with a greater Seq.
type Snapshot struct {
	Sessions []Session `json:"sessions"`
	Seq      uint64    `json:"seq"`
}

// Apply returns list with ev applied: how a subscriber keeps its copy of the sessions in sync.
// Events about unknown sessions are ignored.
func Apply(list []Session, ev *Event) []Session {
	index := -1
	for i := range list {
		if list[i].ID == ev.ID {
			index = i
			break
		}
	}
	switch ev.Kind {
	case EventAdded:
		if ev.Session != nil && index < 0 {
			list = append(list, ev.Session.Clone())
		}
	case EventReplaced:
		if ev.Session != nil && index >= 0 {
			list[index] = ev.Session.Clone()
		}
	case EventUpdated:
		if ev.Session != nil && index >= 0 {
			old := list[index]
			updated := ev.Session.Clone()
			updated.Lines = old.Lines
			if updated.MCP != nil && old.MCP != nil {
				updated.MCP.Events = old.MCP.Events
			}
			list[index] = updated
		}
	case EventLines:
		if index >= 0 {
			list[index].Lines = appendLines(list[index].Lines, ev.Lines)
		}
	case EventMCPQuery:
		if index >= 0 && list[index].MCP != nil && ev.Query != nil {
			appendQuery(list[index].MCP, ev.Query)
		}
	case EventRemoved:
		if index >= 0 {
			list = append(list[:index:index], list[index+1:]...)
		}
	}
	return list
}
