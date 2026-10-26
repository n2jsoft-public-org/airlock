package cachemcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// serverVersion is reported to MCP clients.
const serverVersion = "1.0.0"

// ServerConfig describes the cache an MCP server gives access to.
type ServerConfig struct {
	Name    string
	Target  string
	Profile string
	Region  string
	// Via tells how the cache is reached, ending with a period; empty means the ElastiCache SSM
	// tunnel of Profile and Region.
	Via string
	// Engine is redis or valkey.
	Engine      string
	AllowWrites bool
}

// NewServer builds the MCP server of a cache; every call is passed to record.
func NewServer(cfg *ServerConfig, c *cache, record func(*mcpserve.ToolEvent)) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: cfg.Name, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions(cfg)},
	)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	access := "read-only"
	if cfg.AllowWrites {
		access = "read-write"
	}
	base := mcpserve.ToolEvent{Server: cfg.Name, Profile: cfg.Profile, Region: cfg.Region, Target: cfg.Target, Engine: cfg.Engine, Access: access}
	start := func(tool string, db int, command string) (*mcpserve.ToolEvent, func(int64, error)) {
		e, done := mcpserve.StartEvent(&base, tool, record)
		e.DB, e.Command = &db, command
		return e, done
	}
	// result adds the cluster mode warning, once detection had to assume standalone.
	result := func(out any, err error) (*mcp.CallToolResult, any, error) {
		res, structured, err := mcpserve.ToolResult(out, err)
		if warning := c.clients.modeWarning(); res != nil && warning != "" {
			res.Content = append(res.Content, &mcp.TextContent{Text: "Warning: " + warning})
		}
		return res, structured, err
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "info", Description: "Show the cache state: INFO sections (server, memory, keyspace, stats, replication by default) and the number of keys.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in infoInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		sections := strings.TrimSpace(in.Section)
		if sections == "" {
			sections = strings.Join(defaultInfoSections, ",")
		}
		_, done := start("info", in.DB, "INFO "+strings.ReplaceAll(sections, " ", ""))
		out, err := c.Info(ctx, &in)
		done(int64(len(out.Sections)), err)
		return result(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "scan_keys", Description: "Find keys with SCAN (never KEYS), with their type and TTL. Continue with the returned cursor until it is \"0\".",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in scanInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		args := []string{"SCAN", orValue(in.Cursor, "0"), "MATCH", orValue(in.Pattern, "*")}
		if in.Type != "" {
			args = append(args, "TYPE", in.Type)
		}
		_, done := start("scan_keys", in.DB, commandLine(args))
		out, err := c.Scan(ctx, &in)
		done(int64(len(out.Keys)), err)
		if out.Keys == nil {
			out.Keys = []keyInfo{}
		}
		return result(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_key", Description: "Read a key whatever its type (string, hash, list, set, sorted set, stream), with its TTL and memory usage.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getKeyInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		e, done := start("get_key", in.DB, commandLine([]string{"get_key", in.Key}))
		out, err := c.Get(ctx, &in)
		e.Truncated = out.Truncated
		done(1, err)
		return result(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "command", Description: "Run one read command (GET, HGET, LRANGE, ZRANGE, TTL, XRANGE, MEMORY USAGE, SLOWLOG GET…). Other commands are refused, and so are reads of more than 1000 items: give LRANGE and ZRANGE a range such as 0 99, XRANGE a COUNT, ZRANGEBYSCORE a LIMIT; use get_key to read a whole key.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in commandInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		e, done := start("command", in.DB, commandLine(in.Args))
		out, count, err := c.Command(ctx, &in, false)
		e.Truncated = out.Truncated
		done(count, err)
		return result(out, err)
	})

	if cfg.AllowWrites {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "execute",
			Description: "Run one command that may modify data (SET, DEL, HSET, EXPIRE…). FLUSHALL, KEYS, scripts and blocking commands are refused. Confirm with the user first.",
			Annotations: &mcp.ToolAnnotations{DestructiveHint: new(true)},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in commandInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := context.WithTimeout(ctx, callTimeout)
			defer cancel()
			e, done := start("execute", in.DB, commandLine(in.Args))
			out, count, err := c.Command(ctx, &in, true)
			e.Truncated = out.Truncated
			done(count, err)
			return result(out, err)
		})
	}
	return server
}

func orValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func instructions(cfg *ServerConfig) string {
	access := "Access is read-only."
	if cfg.AllowWrites {
		access = "WRITES ARE ENABLED through the execute tool: always confirm with the user before modifying data."
	}
	via := cfg.Via
	if via == "" {
		via = fmt.Sprintf("(Amazon ElastiCache), reached through an airlock SSM tunnel. AWS profile %s, region %s.", cfg.Profile, cfg.Region)
	}
	return fmt.Sprintf(
		"%s cache %s %s %s "+
			"Use info for the cache state, scan_keys to find keys (KEYS is never allowed), get_key to read a key of any type "+
			"and command for other read commands. A value that is not valid UTF-8 comes back as {\"base64\": \"<bytes in base64>\"}, "+
			"a hash or stream field name as \"base64:<bytes in base64>\".",
		cfg.Engine, cfg.Target, via, access,
	)
}
