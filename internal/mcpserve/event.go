package mcpserve

import (
	"strings"
	"time"
)

// ToolEvent is one MCP tool call made through a session's server. Its fields are in the key order
// of the audit log; the per-kind fields are omitted when empty.
//
//nolint:govet // fieldalignment: the field order is the audit log key order.
type ToolEvent struct {
	Time time.Time `json:"time"`
	// Rows is the number of rows, items or keys returned (or affected by execute); nil on error.
	Rows *int64 `json:"rows,omitempty"`
	// DB is the cache database index (cache tools only).
	DB      *int   `json:"db,omitempty"`
	Server  string `json:"server"`
	Profile string `json:"profile"`
	Region  string `json:"region"`
	Target  string `json:"target"`
	// Engine is postgres, sqlserver, kubernetes, redis or valkey.
	Engine string `json:"engine"`
	Access string `json:"access"`
	Tool   string `json:"tool"`
	// Database, Schema, Table and SQL describe database tool calls.
	Database string `json:"database,omitempty"`
	Schema   string `json:"schema,omitempty"`
	Table    string `json:"table,omitempty"`
	SQL      string `json:"sql,omitempty"`
	// Namespace, Resource and Name describe Kubernetes tool calls.
	Namespace string `json:"namespace,omitempty"`
	Resource  string `json:"resource,omitempty"`
	Name      string `json:"name,omitempty"`
	// Command is the cache command line, cut at 1 KiB (cache tools only).
	Command string `json:"command,omitempty"`
	Error   string `json:"error,omitempty"`
	// Decision is how the gateway answered an opening (gateway tools only): auto, approved, reused,
	// or refused: <reason>; and "refused: <reason>" for a relayed call the gateway refused itself.
	Decision string `json:"decision,omitempty"`
	// LogError explains why the event could not be written to the audit log (empty when it was).
	LogError string `json:"-"`
	// Duration travels in nanoseconds between the daemon and the dashboards; the audit log writes
	// duration_ms instead.
	Duration  time.Duration `json:"duration_ns,omitempty"`
	MaxRows   int           `json:"max_rows,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
}

// Detail is what the call ran, for display: the SQL, the cache command or the Kubernetes request;
// empty for the tools listing databases, tables or resource types.
func (e *ToolEvent) Detail() string {
	switch {
	case e.SQL != "":
		return e.SQL
	case e.Command != "":
		return e.Command
	case e.Resource == "":
		return ""
	}
	var b strings.Builder
	switch {
	case e.Tool == "pod_logs":
		b.WriteString("logs " + e.Resource + "/" + e.Name)
	case e.Name != "":
		b.WriteString("get " + e.Resource + "/" + e.Name)
	default:
		b.WriteString("list " + e.Resource)
	}
	if e.Namespace != "" {
		b.WriteString(" -n " + e.Namespace)
	}
	return b.String()
}

// StartEvent returns a copy of base for tool, stamped now, and a function completing it with the
// number of items returned or the error, then passing it to record.
func StartEvent(base *ToolEvent, tool string, record func(*ToolEvent)) (e *ToolEvent, done func(count int64, err error)) {
	event := *base
	event.Tool = tool
	event.Time = time.Now()
	return &event, func(count int64, err error) {
		event.Duration = time.Since(event.Time)
		if err != nil {
			event.Error = err.Error()
		} else {
			event.Rows = &count
		}
		record(&event)
	}
}
