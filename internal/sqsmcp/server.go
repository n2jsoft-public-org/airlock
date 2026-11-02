// Package sqsmcp serves the MCP tools of an SQS and SNS session: the queues and topics of an AWS
// profile and region, reached through the AWS API.
package sqsmcp

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
	// Engine is the engine of the tool events of SQS sessions.
	Engine = "sqs"
)

// topics runs the SNS tool operations; Task 2 fills it in.
type topics struct{}

// ServerConfig describes the queues and topics an MCP server gives access to.
type ServerConfig struct {
	Name        string
	Profile     string
	Region      string
	AllowWrites bool
}

// command is the readable command of a call: the operation and its non-empty parts; never content.
func command(op string, parts ...string) string {
	words := []string{op}
	for _, p := range parts {
		if p != "" {
			words = append(words, p)
		}
	}
	return strings.Join(words, " ")
}

// NewServer builds the MCP server of the queues q and topics t; every call is passed to record.
// q and t may be nil for a server only listed (the gateway's prototype).
func NewServer(cfg *ServerConfig, q *queues, t *topics, record func(*mcpserve.ToolEvent)) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: cfg.Name, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions(cfg)},
	)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	access := "read-only"
	if cfg.AllowWrites {
		access = "read-write"
	}
	base := mcpserve.ToolEvent{Server: cfg.Name, Profile: cfg.Profile, Region: cfg.Region, Target: cfg.Region, Engine: Engine, Access: access}
	start := func(tool, cmd string) (*mcpserve.ToolEvent, func(int64, error)) {
		e, done := mcpserve.StartEvent(&base, tool, record)
		e.Command = cmd
		return e, done
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_queues", Annotations: readOnly,
		Description: "List the SQS queues of the session's account and region, 100 per page. " +
			"Narrow with prefix; continue with next as start.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listQueuesInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("list_queues", command("list_queues", in.Prefix))
		out, err := q.List(ctx, &in)
		done(int64(len(out.Queues)), err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_queue", Annotations: readOnly,
		Description: "Read the configuration of a queue by name or URL: message counts (visible, in flight, delayed), timeouts, " +
			"retention, encryption, dead-letter redrive policy, access policy, tags and the queues using it as dead-letter queue.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queueInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("get_queue", command("get_queue", nameOf(in.Queue)))
		out, err := q.Get(ctx, &in)
		done(1, err)
		return mcpserve.ToolResult(out, err)
	})
	return server
}

func instructions(cfg *ServerConfig) string {
	access := "Access is read-only."
	if cfg.AllowWrites {
		access = "WRITES ARE ENABLED: peek_messages raises the receive count of every message it returns, so a message may reach " +
			"its dead-letter queue; repost_message only after the user confirms."
	}
	return fmt.Sprintf(
		"Amazon SQS queues and SNS topics of AWS profile %s, region %s, reached through the AWS API (no tunnel). %s "+
			"Use list_queues, then get_queue for the configuration, counts and dead-letter policy of a queue. "+
			"Listings return one page per call: pass next as start for the next one.",
		cfg.Profile, cfg.Region, access,
	)
}
