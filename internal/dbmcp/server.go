package dbmcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

const (
	// serverVersion is reported to MCP clients.
	serverVersion  = "1.0.0"
	defaultMaxRows = 200
	maxRowsLimit   = 1000
)

// ServerConfig describes the session an MCP server gives access to.
type ServerConfig struct {
	Name    string
	Target  string
	Profile string
	Region  string
	// Via tells how the database is reached, ending with a period (e.g. "reached directly at
	// db:5432."); empty means the SSM tunnel of Profile and Region.
	Via         string
	Engine      Engine
	AllowWrites bool
}

type listTablesInput struct {
	Database string `json:"database,omitempty" jsonschema:"database name; defaults to the session's default database"`
	Schema   string `json:"schema,omitempty" jsonschema:"only list tables of this schema"`
}

type describeTableInput struct {
	Database string `json:"database,omitempty" jsonschema:"database name; defaults to the session's default database"`
	Schema   string `json:"schema,omitempty" jsonschema:"schema of the table; defaults to public (PostgreSQL) or dbo (SQL Server)"`
	Table    string `json:"table" jsonschema:"table or view name"`
}

type queryInput struct {
	SQL      string `json:"sql" jsonschema:"a single read-only SELECT or WITH statement"`
	Database string `json:"database,omitempty" jsonschema:"database name; defaults to the session's default database"`
	MaxRows  int    `json:"max_rows,omitempty" jsonschema:"maximum number of rows to return (default 200, max 1000)"`
}

type queryPlanInput struct {
	SQL         string `json:"sql" jsonschema:"a single read-only SELECT or WITH statement, which may use parameters: $1, $2… on PostgreSQL, the declared ones on SQL Server"`
	Database    string `json:"database,omitempty" jsonschema:"database name; defaults to the session's default database"`
	Parameters  string `json:"parameters,omitempty" jsonschema:"SQL Server only: parameter declaration, as sp_executesql takes it and applications send it, e.g. @p0 int, @p2 nvarchar(6); PostgreSQL infers the types of $1, $2… (cast in the statement where it cannot)"`
	Values      []any  `json:"values,omitempty" jsonschema:"one value per parameter, in order: string, number, boolean or null"`
	Mode        string `json:"mode,omitempty" jsonschema:"actual (default) runs the statement in a transaction rolled back; estimated only plans it (on SQL Server, without parameters)"`
	IncludeXML  bool   `json:"include_xml,omitempty" jsonschema:"SQL Server only: also return the raw showplan XML, to save as a .sqlplan file for SSMS"`
	IncludeJSON bool   `json:"include_json,omitempty" jsonschema:"PostgreSQL only: also return the raw EXPLAIN (FORMAT JSON) output, for plan visualizers"`
}

type executeInput struct {
	SQL      string `json:"sql" jsonschema:"a single statement that may modify data or schema"`
	Database string `json:"database,omitempty" jsonschema:"database name; defaults to the session's default database"`
}

type databasesOutput struct {
	Databases []string `json:"databases"`
}

type tablesOutput struct {
	Tables    []Table `json:"tables"`
	Truncated bool    `json:"truncated"`
}

type executeOutput struct {
	RowsAffected int64 `json:"rows_affected"`
}

// NewServer builds the MCP server exposing q for one jumphost session.
func NewServer(cfg *ServerConfig, q Querier) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: cfg.Name, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions(cfg)},
	)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_databases",
		Description: "List the databases this session can query.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		names, err := q.Databases(ctx)
		if names == nil {
			names = []string{}
		}
		return mcpserve.ToolResult(databasesOutput{Databases: names}, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_tables",
		Description: "List tables and views, optionally of one schema.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTablesInput) (*mcp.CallToolResult, any, error) {
		tables, truncated, err := q.Tables(ctx, in.Database, in.Schema)
		if tables == nil {
			tables = []Table{}
		}
		return mcpserve.ToolResult(tablesOutput{Tables: tables, Truncated: truncated}, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "describe_table",
		Description: "Describe a table: columns, primary key, foreign keys and indexes.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in describeTableInput) (*mcp.CallToolResult, any, error) {
		description, err := q.Describe(ctx, in.Database, in.Schema, in.Table)
		return mcpserve.ToolResult(description, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "query",
		Description: fmt.Sprintf("Run a single read-only %s SELECT or WITH statement and return its rows.", cfg.Engine.Dialect()),
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, any, error) {
		result, err := q.Query(ctx, in.Database, in.SQL, clampRows(in.MaxRows))
		return mcpserve.ToolResult(result, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "query_plan",
		Description: "Get the execution plan of a single read-only SELECT or WITH statement: operators with estimated " +
			"and actual rows, costs, predicates and warnings; on SQL Server also seeks, missing indexes, parameters, " +
			"and STATISTICS IO and TIME; on PostgreSQL EXPLAIN ANALYZE timings, buffers and settings.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryPlanInput) (*mcp.CallToolResult, any, error) {
		result, err := q.Plan(ctx, PlanRequest{
			Database: in.Database, SQL: in.SQL, Parameters: in.Parameters, Values: in.Values,
			Mode: in.Mode, IncludeXML: in.IncludeXML, IncludeJSON: in.IncludeJSON,
		})
		return mcpserve.ToolResult(result, err)
	})

	if cfg.AllowWrites {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "execute",
			Description: fmt.Sprintf("Run a single %s statement that may modify data or schema. Confirm with the user first.", cfg.Engine.Dialect()),
			Annotations: &mcp.ToolAnnotations{DestructiveHint: new(true)},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in executeInput) (*mcp.CallToolResult, any, error) {
			affected, err := q.Exec(ctx, in.Database, in.SQL)
			return mcpserve.ToolResult(executeOutput{RowsAffected: affected}, err)
		})
	}

	return server
}

func clampRows(requested int) int {
	switch {
	case requested <= 0:
		return defaultMaxRows
	case requested > maxRowsLimit:
		return maxRowsLimit
	default:
		return requested
	}
}

func instructions(cfg *ServerConfig) string {
	access := "Access is read-only."
	if cfg.AllowWrites {
		access = "WRITES ARE ENABLED through the execute tool: always confirm with the user before modifying data or schema."
	}
	via := cfg.Via
	if via == "" {
		via = fmt.Sprintf("reached through an airlock SSM tunnel. AWS profile %s, region %s.", cfg.Profile, cfg.Region)
	}
	return fmt.Sprintf(
		"Database %s (%s dialect), %s %s "+
			"Explore with list_databases, list_tables and describe_table before writing queries. "+
			"The query tool runs one SELECT/WITH statement and returns at most %d rows.%s",
		cfg.Target, cfg.Engine.Dialect(), via, access, maxRowsLimit, planInstructions(cfg.Engine),
	)
}

func planInstructions(engine Engine) string {
	if engine == EnginePostgres {
		return " To analyze a slow query, get its plan with query_plan (EXPLAIN ANALYZE, rolled back), passing " +
			"the values of $1, $2… the application sends; pg_stat_statements tells which queries cost the most, when installed."
	}
	return " To analyze a slow query, get its plan with query_plan, passing the parameters the application sends " +
		"(e.g. (@p0 int,@p1 nvarchar(6))select ... becomes parameters \"@p0 int,@p1 nvarchar(6)\"); " +
		"Query Store (sys.query_store_*) tells which queries cost the most, when the login may read it."
}
