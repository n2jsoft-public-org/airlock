package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft/airlock/internal/cachemcp"
	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/dynamomcp"
	"github.com/n2jsoft/airlock/internal/kubemcp"
	"github.com/n2jsoft/airlock/internal/logsmcp"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/s3mcp"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// kind is the family of relayed tools a session serves; it prefixes their names.
type kind string

const (
	kindDB     kind = "db"
	kindK8s    kind = "k8s"
	kindCache  kind = "cache"
	kindDynamo kind = "dynamo"
	kindS3     kind = "s3"
	kindLogs   kind = "logs"
)

// kinds is the order the relayed tools are listed in.
var kinds = []kind{kindDB, kindK8s, kindCache, kindDynamo, kindS3, kindLogs}

// kindOf is the kind of the sessions of resourceType; empty when they serve no tool.
func kindOf(resourceType string) kind {
	switch resourceType {
	case "rds", sessions.DirectResourceType, sessions.DockerResourceType:
		return kindDB
	case "eks":
		return kindK8s
	case "elasticache", sessions.DockerCacheResourceType:
		return kindCache
	case sessions.DynamoDBResourceType:
		return kindDynamo
	case sessions.S3ResourceType:
		return kindS3
	case sessions.LogsResourceType:
		return kindLogs
	default:
		return ""
	}
}

// neutralDescriptions replace the session tool descriptions that name one SQL dialect.
var neutralDescriptions = map[string]string{
	"db_query":   "Run a single read-only SELECT or WITH statement, in the session's SQL dialect (see about in list_sessions), and return its rows.",
	"db_execute": "Run a single statement that may modify data or schema, in the session's SQL dialect. Only for sessions opened with writes. Confirm with the user first.",
}

// prototype builds the server of k with writes on and nothing to reach: its tools are those the
// sessions of k serve.
func prototype(k kind) *mcp.Server {
	switch k {
	case kindDB:
		return dbmcp.NewServer(&dbmcp.ServerConfig{Name: "prototype", AllowWrites: true}, nil)
	case kindK8s:
		return kubemcp.NewServer(&kubemcp.ServerConfig{Name: "prototype"}, nil, nil)
	case kindCache:
		return cachemcp.NewServer(&cachemcp.ServerConfig{Name: "prototype", AllowWrites: true}, nil, nil)
	case kindDynamo:
		return dynamomcp.NewServer(&dynamomcp.ServerConfig{Name: "prototype", AllowWrites: true}, nil, nil)
	case kindS3:
		return s3mcp.NewServer(&s3mcp.ServerConfig{Name: "prototype", AllowWrites: true}, nil, nil)
	default:
		return logsmcp.NewServer(&logsmcp.ServerConfig{Name: "prototype"}, nil, nil)
	}
}

// connectClient connects an in-process MCP client to server.
func connectClient(ctx context.Context, server *mcp.Server) (*mcp.ClientSession, error) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "airlock-gateway", Version: serverVersion}, nil)
	return client.Connect(ctx, clientTransport, nil)
}

// relay serves the tools of the sessions through the gateway: each call goes to the session's own
// MCP server, through an in-process client, so its guards, caps and audit log apply unchanged.
// settings are the gateway's: without agentWrites, no session takes writes from an agent.
type relay struct {
	ctx      context.Context
	manager  *sessions.Manager
	now      func() time.Time
	settings func() Settings
	clients  map[sessions.ID]*relayClient
	lastCall map[sessions.ID]time.Time
	// record writes the calls the gateway refuses itself to the audit log, as server name; nil
	// records nothing. The calls it relays are audited by the session servers.
	record func(*mcpserve.ToolEvent)
	name   string
	mu     sync.Mutex
}

// audit has the refused calls recorded by record, as the gateway server name.
func (r *relay) audit(name string, record func(*mcpserve.ToolEvent)) {
	r.name, r.record = name, record
}

// recordRefusal writes the refused call of tool to session id in the audit log, with the SQL of a
// database tool (never the arguments of a cache command, which may hold a password).
func (r *relay) recordRefusal(tool string, id sessions.ID, args map[string]any, reason string) {
	if r.record == nil {
		return
	}
	e := &mcpserve.ToolEvent{Time: r.now(), Server: r.name, Tool: tool, Decision: "refused: " + reason}
	if snapshot, err := r.manager.Snapshot(r.ctx); err == nil {
		for idx := range snapshot.Sessions {
			if s := &snapshot.Sessions[idx]; s.ID == id {
				e.Target, e.Profile, e.Region = s.Target, s.Profile, s.Region
			}
		}
	}
	if strings.HasPrefix(tool, string(kindDB)+"_") {
		e.SQL, _ = args["sql"].(string)           //nolint:errcheck // absent or not text: none.
		e.Database, _ = args["database"].(string) //nolint:errcheck // absent or not text: none.
	}
	r.record(e)
}

type relayClient struct {
	server  *mcp.Server
	session *mcp.ClientSession
}

func newRelay(ctx context.Context, manager *sessions.Manager, now func() time.Time, settings func() Settings) *relay {
	return &relay{ctx: ctx, manager: manager, now: now, settings: settings, clients: map[sessions.ID]*relayClient{}, lastCall: map[sessions.ID]time.Time{}}
}

// add declares the relayed tools on server: the tools of the prototype of each kind, prefixed by
// the kind, with a session argument.
func (r *relay) add(ctx context.Context, server *mcp.Server) error {
	for _, k := range kinds {
		cs, err := connectClient(ctx, prototype(k))
		if err != nil {
			return err
		}
		listed, err := cs.ListTools(ctx, nil)
		_ = cs.Close() //nolint:errcheck // an in-memory session.
		if err != nil {
			return fmt.Errorf("failed to list the %s tools: %w", k, err)
		}
		for _, proto := range listed.Tools {
			tool, err := relayTool(k, proto)
			if err != nil {
				return err
			}
			server.AddTool(tool, r.handler(k, proto.Name, destructive(proto)))
		}
	}
	return nil
}

func relayTool(k kind, proto *mcp.Tool) (*mcp.Tool, error) {
	schema, err := withSession(proto.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("tool %s: %w", proto.Name, err)
	}
	name := string(k) + "_" + proto.Name
	description := proto.Description
	if neutral, ok := neutralDescriptions[name]; ok {
		description = neutral
	}
	return &mcp.Tool{
		Name: name, Title: proto.Title, Annotations: proto.Annotations, InputSchema: schema,
		Description: description + " Takes the id of a session from list_sessions.",
	}, nil
}

// withSession returns schema with a required integer session property.
func withSession(schema any) (map[string]any, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if uerr := json.Unmarshal(raw, &m); uerr != nil {
		return nil, uerr
	}
	m["type"] = "object"
	properties, ok := m["properties"].(map[string]any)
	if !ok {
		properties = map[string]any{}
	}
	properties["session"] = map[string]any{"type": "integer", "description": "id of the session, from list_sessions"}
	m["properties"] = properties
	required, _ := m["required"].([]any) //nolint:errcheck // absent means none.
	m["required"] = append(required, "session")
	return m, nil
}

// splitSession takes the session id out of the arguments of a relayed call. LLMs often quote
// numbers: "3" reads as 3.
func splitSession(raw json.RawMessage) (map[string]any, sessions.ID, error) {
	args := map[string]any{}
	if len(raw) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&args); err != nil {
			return nil, 0, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	value, ok := args["session"]
	delete(args, "session")
	var text string
	switch v := value.(type) {
	case json.Number:
		text = v.String()
	case string:
		text = strings.TrimSpace(v)
	}
	id, err := strconv.Atoi(text)
	if !ok || err != nil || id <= 0 {
		return nil, 0, errors.New("session is required: the id of a session from list_sessions")
	}
	return args, sessions.ID(id), nil
}

// destructive is whether tool declares it may modify data: the tools that need agent writes.
func destructive(tool *mcp.Tool) bool {
	return tool.Annotations != nil && tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint
}

// handler relays the calls of the tool name of kind k to the session the call names.
func (r *relay) handler(k kind, name string, writes bool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, id, err := splitSession(req.Params.Arguments)
		if err != nil {
			return refusal("%v", err), nil
		}
		refuse := func(format string, a ...any) *mcp.CallToolResult {
			reason := fmt.Sprintf(format, a...)
			r.recordRefusal(string(k)+"_"+name, id, args, reason)
			return refusal("%s", reason)
		}
		access, err := r.manager.MCPServer(id)
		switch {
		case errors.Is(err, sessions.ErrUnknownSession):
			return refuse("no session %d; call list_sessions", id), nil
		case errors.Is(err, sessions.ErrSessionEnded):
			r.forget(id)
			return refuse("session %d ended%s", id, strings.TrimPrefix(err.Error(), sessions.ErrSessionEnded.Error())), nil
		case errors.Is(err, sessions.ErrNoMCPServer):
			return refuse("session %d has no MCP server (its mcp line in list_sessions tells why)", id), nil
		case err != nil:
			return refuse("session %d: %v", id, err), nil
		}
		if got := kindOf(access.ResourceType); got != k {
			return refuse("session %d is a %s session: use the %s_ tools", id, access.ResourceType, got), nil
		}
		if writes && !r.settings().AgentWrites {
			return refuse("%s", reasonWritesOff), nil
		}
		if writes && !access.Writes {
			return refuse("session %d is read-only: open it with writes, or ask the user", id), nil
		}
		cs, err := r.client(id, access.Server)
		if err != nil {
			return refuse("session %d: %v", id, err), nil
		}
		r.touch(id)
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return refuse("%v", err), nil
		}
		// The text carries the result; the structured content, decoded as float64, would round big integers.
		res.StructuredContent = nil
		return res, nil
	}
}

// client returns the in-process client of session id's server, connected at the first call and
// replaced when a restart gave the session another server.
func (r *relay) client(id sessions.ID, server *mcp.Server) (*mcp.ClientSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[id]; ok {
		if c.server == server {
			return c.session, nil
		}
		_ = c.session.Close() //nolint:errcheck // the former server of a restarted session.
		delete(r.clients, id)
	}
	cs, err := connectClient(r.ctx, server)
	if err != nil {
		return nil, err
	}
	r.clients[id] = &relayClient{server: server, session: cs}
	return cs, nil
}

// instructions are the instructions of session id's server (dialect, access, how it is reached).
func (r *relay) instructions(id sessions.ID, server *mcp.Server) string {
	cs, err := r.client(id, server)
	if err != nil {
		return ""
	}
	if res := cs.InitializeResult(); res != nil {
		return res.Instructions
	}
	return ""
}

func (r *relay) touch(id sessions.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastCall[id] = r.now()
}

// lastCallOf is when session id was last called through the gateway; false when never.
func (r *relay) lastCallOf(id sessions.ID) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.lastCall[id]
	return at, ok
}

func (r *relay) forget(id sessions.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forgetLocked(id)
}

func (r *relay) forgetLocked(id sessions.ID) {
	if c, ok := r.clients[id]; ok {
		_ = c.session.Close() //nolint:errcheck // an in-memory session.
		delete(r.clients, id)
	}
	delete(r.lastCall, id)
}

// sweep forgets the sessions not in active.
func (r *relay) sweep(active map[sessions.ID]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.clients {
		if !active[id] {
			r.forgetLocked(id)
		}
	}
	for id := range r.lastCall {
		if !active[id] {
			delete(r.lastCall, id)
		}
	}
}

func (r *relay) closeAll() { r.sweep(nil) }
