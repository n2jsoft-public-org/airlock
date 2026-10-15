package dynamomcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

const (
	// serverVersion is reported to MCP clients.
	serverVersion = "1.0.0"
	// Engine is the engine of the tool events of DynamoDB sessions.
	Engine = "dynamodb"
)

// ServerConfig describes the tables an MCP server gives access to.
type ServerConfig struct {
	Name        string
	Profile     string
	Region      string
	AllowWrites bool
}

// summary is the readable command of a call: the operation, the table, the index in brackets,
// then the expressions; never the values.
func summary(op, table, index string, parts ...string) string {
	words := []string{op, table}
	if index != "" {
		words = append(words, "["+index+"]")
	}
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			words = append(words, p)
		}
	}
	return strings.Join(words, " ")
}

func prefixed(prefix, expression string) string {
	if strings.TrimSpace(expression) == "" {
		return ""
	}
	return prefix + " " + expression
}

// exact decodes the raw arguments of req into in. The go-sdk decodes them to float64 before its
// handlers run, which would round big integers; object keeps them exact.
func exact[T any](req *mcp.CallToolRequest, in *T) error {
	if len(req.Params.Arguments) == 0 {
		return nil
	}
	var v T
	if err := json.Unmarshal(req.Params.Arguments, &v); err != nil {
		return err
	}
	*in = v
	return nil
}

// result is the tool result of out, marshaled once with encoding/json: the tools declare no output
// schema, as the go-sdk would round the json.Number values of out through float64 to validate it.
func result(out any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}, StructuredContent: json.RawMessage(raw)}, nil, nil
}

// NewServer builds the MCP server of the tables t; every call is passed to record. t may be nil
// for a server only listed (the gateway's prototype).
func NewServer(cfg *ServerConfig, t *tables, record func(*mcpserve.ToolEvent)) *mcp.Server {
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
	start := func(tool, table, command string) (*mcpserve.ToolEvent, func(int64, error)) {
		e, done := mcpserve.StartEvent(&base, tool, record)
		e.Table, e.Command = table, command
		return e, done
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_tables", Description: "List the DynamoDB tables of the session's account and region, 100 per page.", Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTablesInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("list_tables", "", "list_tables")
		out, err := t.List(ctx, &in)
		done(int64(len(out.Tables)), err)
		return result(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "describe_table",
		Description: "Describe a table: its partition and sort keys, attribute types, secondary indexes, billing mode, approximate item count and size, TTL attribute and stream.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in describeInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("describe_table", in.Table, summary("describe_table", in.Table, ""))
		out, err := t.Describe(ctx, &in)
		done(1, err)
		return result(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_item", Description: "Read one item by its full primary key.", Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in getItemInput) (*mcp.CallToolResult, any, error) {
		if err := exact(req, &in); err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		e, done := start("get_item", in.Table, summary("get_item", in.Table, ""))
		out, err := t.Get(ctx, &in)
		e.Truncated = out.Truncated
		count := int64(0)
		if out.Found {
			count = 1
		}
		done(count, err)
		return result(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "query",
		Description: "Read the items of one partition key, on the table or a secondary index, one page per call. " +
			"Continue with next_key as start_key.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, any, error) {
		if err := exact(req, &in); err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		e, done := start("query", in.Table, summary("query", in.Table, in.Index, in.KeyCondition, prefixed("filter", in.Filter)))
		out, err := t.Query(ctx, &in)
		e.Truncated = out.Truncated
		done(int64(out.Count), err)
		return result(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "scan",
		Description: "Read a whole table or index page by page, filtering after reading: it consumes read capacity for every " +
			"item evaluated, so prefer query when the partition key is known. One page per call; continue with next_key as start_key.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in scanInput) (*mcp.CallToolResult, any, error) {
		if err := exact(req, &in); err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		e, done := start("scan", in.Table, summary("scan", in.Table, in.Index, prefixed("filter", in.Filter)))
		out, err := t.Scan(ctx, &in)
		e.Truncated = out.Truncated
		done(int64(out.Count), err)
		return result(out, err)
	})

	if !cfg.AllowWrites {
		return server
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "put_item", Description: "Create or replace one item. Confirm with the user first.", Annotations: destructive,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in putItemInput) (*mcp.CallToolResult, any, error) {
		if err := exact(req, &in); err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("put_item", in.Table, summary("put_item", in.Table, "", prefixed("if", in.Condition)))
		out, err := t.Put(ctx, &in)
		done(1, err)
		return result(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "update_item", Description: "Update one item with an update expression and return it. Confirm with the user first.", Annotations: destructive,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateItemInput) (*mcp.CallToolResult, any, error) {
		if err := exact(req, &in); err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("update_item", in.Table, summary("update_item", in.Table, "", in.Update, prefixed("if", in.Condition)))
		out, err := t.Update(ctx, &in)
		done(1, err)
		return result(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "delete_item", Description: "Delete one item by its full primary key. Confirm with the user first.", Annotations: destructive,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in deleteItemInput) (*mcp.CallToolResult, any, error) {
		if err := exact(req, &in); err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("delete_item", in.Table, summary("delete_item", in.Table, "", prefixed("if", in.Condition)))
		out, err := t.Delete(ctx, &in)
		done(1, err)
		return result(out, err)
	})
	return server
}

func instructions(cfg *ServerConfig) string {
	access := "Access is read-only."
	if cfg.AllowWrites {
		access = "WRITES ARE ENABLED through put_item, update_item and delete_item: always confirm with the user before modifying data."
	}
	return fmt.Sprintf(
		"Amazon DynamoDB tables of AWS profile %s, region %s, reached through the AWS API (no tunnel). %s "+
			"Use list_tables, then describe_table for the keys and indexes of a table. get_item reads one item by its full key, "+
			"query reads the items of a partition key (on the table or an index) and scan reads a whole table, consuming capacity: "+
			"prefer query. Keys and values are plain JSON; expressions use DynamoDB's syntax with :placeholders in values and "+
			"#placeholders in names. One page per call: pass next_key as start_key for the next one.",
		cfg.Profile, cfg.Region, access,
	)
}
