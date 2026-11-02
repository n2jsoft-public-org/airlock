// Package sqsmcp serves the MCP tools of an SQS and SNS session: the queues and topics of an AWS
// profile and region, reached through the AWS API.
package sqsmcp

import (
	"context"
	"fmt"
	"strconv"
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
	destructive := &mcp.ToolAnnotations{DestructiveHint: new(true)}
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
			"retention, encryption, dead-letter redrive policy, access policy, tags and the queues using it as dead-letter queue. " +
			"Tags and source queues are optional: a missing permission shows up in warnings.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queueInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("get_queue", command("get_queue", nameOf(in.Queue)))
		out, err := q.Get(ctx, &in)
		done(1, err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_topics", Annotations: readOnly,
		Description: "List the SNS topics of the session's account and region, one page per call; continue with next as start.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTopicsInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("list_topics", command("list_topics"))
		out, err := t.List(ctx, &in)
		done(int64(len(out.Topics)), err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_topic", Annotations: readOnly,
		Description: "Read a topic by name or ARN: FIFO, encryption, access and delivery policies, tags and its subscriptions " +
			"(protocol, endpoint, pending confirmation; first page, continue with subscriptions_start set to subscriptions_next, " +
			"and use get_subscription for the details of one). Tags are optional: a missing permission shows up in warnings.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in topicInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("get_topic", command("get_topic", in.Topic))
		out, err := t.Get(ctx, &in)
		done(int64(len(out.Subscriptions)), err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_subscription", Annotations: readOnly,
		Description: "Read a subscription by ARN: topic, protocol, endpoint, filter policy and scope, raw message delivery, redrive policy. " +
			"A subscription pending confirmation has no ARN yet (\"PendingConfirmation\") and cannot be read with it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in subscriptionInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("get_subscription", command("get_subscription", in.ARN))
		out, err := t.Subscription(ctx, &in)
		done(1, err)
		return mcpserve.ToolResult(out, err)
	})

	if !cfg.AllowWrites {
		return server
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "peek_messages", Annotations: destructive,
		Description: "Read up to 10 messages of a queue without deleting them. Each read raises the receive count of the messages " +
			"returned: a message at its maxReceiveCount reaches the dead-letter queue. Refused when maxReceiveCount is 2 or less. " +
			"Bodies are cut at 32 KB. Check deliveries_left and at_limit; do not read at_limit messages again.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in peekInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		n := in.Max
		if n < 1 || n > maxPeek {
			n = maxPeek
		}
		_, done := start("peek_messages", command("peek_messages", nameOf(in.Queue), "n="+strconv.Itoa(n)))
		out, err := q.Peek(ctx, &in)
		done(int64(len(out.Messages)), err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "repost_message", Annotations: destructive,
		Description: "Send a fresh copy of an at_limit message (read by peek_messages) to its queue, then try to delete the original. " +
			"Confirm with the user first. Consequences: the copy has a new message ID and age, retention restarts, the receive count resets " +
			"even for messages that really fail, a FIFO copy goes to the end of its message group, and consumers may see a duplicate " +
			"because deleting the original is best effort (check delete_accepted).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in repostInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("repost_message", command("repost_message", nameOf(in.Queue), in.MessageID))
		out, err := q.Repost(ctx, &in)
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
			"Use list_queues, then get_queue for the configuration, counts and dead-letter policy of a queue; "+
			"list_topics, then get_topic for a topic and its subscriptions, and get_subscription for a filter policy. "+
			"Listings return one page per call: pass next as start for the next one.",
		cfg.Profile, cfg.Region, access,
	)
}
