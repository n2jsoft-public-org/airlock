// Package logsmcp serves the read-only MCP tools of a CloudWatch Logs session: the log groups of an
// AWS profile and region, reached through the AWS API.
package logsmcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

const (
	// serverVersion is reported to MCP clients.
	serverVersion = "1.0.0"
	// Engine is the engine of the tool events of CloudWatch Logs sessions.
	Engine = "logs"
)

// ServerConfig describes the log groups an MCP server gives access to.
type ServerConfig struct {
	Name    string
	Profile string
	Region  string
}

// command is the readable command of a call: the operation and its non-empty parts.
func command(op string, parts ...string) string {
	words := []string{op}
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			words = append(words, p)
		}
	}
	return strings.Join(words, " ")
}

// span is the time range of a call as given, its defaults filled in: [15m, now].
func span(start, end string) string {
	if start == "" {
		start = "15m" // defaultRange
	}
	if end == "" {
		end = "now"
	}
	return "[" + start + ", " + end + "]"
}

// NewServer builds the MCP server of the log groups l; every call is passed to record. l may be nil
// for a server only listed (the gateway's prototype).
func NewServer(cfg *ServerConfig, l *logs, record func(*mcpserve.ToolEvent)) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: cfg.Name, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions(cfg)},
	)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	base := mcpserve.ToolEvent{Server: cfg.Name, Profile: cfg.Profile, Region: cfg.Region, Target: cfg.Region, Engine: Engine, Access: "read-only"}
	start := func(tool, cmd string) (*mcpserve.ToolEvent, func(int64, error)) {
		e, done := mcpserve.StartEvent(&base, tool, record)
		e.Command = cmd
		return e, done
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_log_groups", Annotations: readOnly,
		Description: "List the log groups of the session's account and region, 50 per page, with their retention, stored bytes and class. " +
			"Narrow with prefix (e.g. /aws/lambda/) or pattern; continue with next as start.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listGroupsInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("list_log_groups", command("list_log_groups", in.Prefix, in.Pattern))
		out, err := l.Groups(ctx, &in)
		done(int64(len(out.Groups)), err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_log_streams", Annotations: readOnly,
		Description: "List the log streams of a group, the most recently written first, 50 per page; continue with next as start.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listStreamsInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("list_log_streams", command("list_log_streams", in.Group))
		out, err := l.Streams(ctx, &in)
		done(int64(len(out.Streams)), err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "query", Annotations: readOnly,
		Description: "Run a CloudWatch Logs Insights query on one or more log groups over a time range (default the last 15 minutes) " +
			"and return its rows (at most 1000) with the records and bytes scanned. Logs Insights bills every GB scanned: keep " +
			"the range short and the groups few, and widen them only when needed. Waits up to 60 seconds, then stops the query.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, queryTimeout+callTimeout)
		defer cancel()
		e, done := start("query", command("query", strings.Join(in.Groups, ","), span(in.Start, in.End), in.Query))
		out, err := l.Query(ctx, &in)
		e.Truncated = out.Truncated
		done(int64(len(out.Rows)), err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "filter_events", Annotations: readOnly,
		Description: "Return the newest events of a log group matching a filter pattern over a time range (default the last 15 " +
			"minutes), oldest first, like a tail: at most 1000, long messages cut. Optionally only some streams.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in filterInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		e, done := start("filter_events", command("filter_events", in.Group, span(in.Start, in.End), in.Pattern))
		out, err := l.Filter(ctx, &in)
		e.Truncated = out.Truncated
		done(int64(len(out.Events)), err)
		return mcpserve.ToolResult(out, err)
	})
	return server
}

func instructions(cfg *ServerConfig) string {
	return fmt.Sprintf(
		"Amazon CloudWatch Logs of AWS profile %s, region %s, reached through the AWS API (no tunnel). Access is read-only. "+
			"Use list_log_groups (narrowed with a prefix such as /aws/lambda/ or /ecs/), then list_log_streams for the recently "+
			"written streams of a group. filter_events tails a group: its newest events matching a filter pattern. query runs a "+
			"Logs Insights query over several groups; it is billed per GB scanned, so start with the default 15 minutes and widen "+
			"the range only when needed. Times are durations before now (15m, 1h, 7d) or RFC 3339.",
		cfg.Profile, cfg.Region,
	)
}
