package dbmcp

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

const (
	// serverVersion is reported to MCP clients.
	serverVersion  = "1.0.0"
	defaultMaxRows = 200
	maxRowsLimit   = 1000
	// defaultScriptTimeout and maxScriptTimeout bound a run_script call, in seconds.
	defaultScriptTimeout = 60
	maxScriptTimeout     = 600
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
	Schema   string `json:"schema,omitempty" jsonschema:"schema of the table; defaults to public (PostgreSQL), dbo (SQL Server) or the current database (MySQL, where schemas are databases)"`
	Table    string `json:"table" jsonschema:"table or view name"`
}

type queryInput struct {
	SQL      string `json:"sql" jsonschema:"a single read-only SELECT or WITH statement (on MySQL also SHOW, DESCRIBE or EXPLAIN)"`
	Database string `json:"database,omitempty" jsonschema:"database name; defaults to the session's default database"`
	MaxRows  int    `json:"max_rows,omitempty" jsonschema:"maximum number of rows to return (default 200, max 1000)"`
}

type exportInput struct {
	SQL      string `json:"sql" jsonschema:"a single read-only SELECT or WITH statement (on MySQL also SHOW, DESCRIBE or EXPLAIN)"`
	Database string `json:"database,omitempty" jsonschema:"database name; defaults to the session's default database"`
	Format   string `json:"format,omitempty" jsonschema:"csv (default: a header row, NULL as an empty field) or jsonl (one JSON object per row)"`
}

type queryPlanInput struct {
	SQL         string `json:"sql" jsonschema:"a single read-only SELECT or WITH statement, which may use parameters: $1, $2… on PostgreSQL, ? on MySQL, the declared ones on SQL Server"`
	Database    string `json:"database,omitempty" jsonschema:"database name; defaults to the session's default database"`
	Parameters  string `json:"parameters,omitempty" jsonschema:"SQL Server only: parameter declaration, as sp_executesql takes it and applications send it, e.g. @p0 int, @p2 nvarchar(6); PostgreSQL infers the types of $1, $2… (cast in the statement where it cannot), MySQL converts the values of ?"`
	Mode        string `json:"mode,omitempty" jsonschema:"actual (default) runs the statement in a transaction rolled back (EXPLAIN ANALYZE on MySQL 8.0.18 or later, as JSON from 8.3); estimated only plans it (on SQL Server, without parameters)"`
	Values      []any  `json:"values,omitempty" jsonschema:"one value per parameter, in order: string, number, boolean or null"`
	IncludeXML  bool   `json:"include_xml,omitempty" jsonschema:"SQL Server only: also return the raw showplan XML, to save as a .sqlplan file for SSMS"`
	IncludeJSON bool   `json:"include_json,omitempty" jsonschema:"PostgreSQL and MySQL: also return the raw EXPLAIN JSON output, for plan visualizers (MySQL's actual plans only from 8.3: before, they are a text tree)"`
}

type runScriptInput struct {
	FilePath       string `json:"file_path,omitempty" jsonschema:"absolute path of a local .sql file (UTF-8) holding the script; or give sql"`
	SQL            string `json:"sql,omitempty" jsonschema:"the script itself, when it is not in a file"`
	Database       string `json:"database,omitempty" jsonschema:"database name; defaults to the session's default database"`
	MaxRows        int    `json:"max_rows,omitempty" jsonschema:"maximum number of rows to return, over every result set (default 200, max 1000)"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"time the whole script may take, in seconds (default 60, max 600)"`
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
		result, err := q.Query(ctx, in.Database, in.SQL, mcpserve.Limit(in.MaxRows, defaultMaxRows, maxRowsLimit))
		return mcpserve.ToolResult(result, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "export",
		Description: "Run a single read-only SELECT or WITH statement and write all its rows to a local file, " +
			"without returning them: the reply only gives the file path, row count, size and columns. For results " +
			"too large for query or used as input elsewhere (e.g. ids for a migration): no row cap, no text cut, " +
			"5 min timeout, stops at 1 GiB (truncated). The file (mode 0600) is kept after the session closes: " +
			"delete it when done.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in exportInput) (*mcp.CallToolResult, any, error) {
		result, err := q.Export(ctx, in.Database, in.SQL, in.Format)
		return mcpserve.ToolResult(result, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "query_plan",
		Description: "Get the execution plan of a single read-only SELECT or WITH statement: operators with estimated " +
			"and actual rows, costs, predicates and warnings; on SQL Server also seeks, missing indexes, parameters, " +
			"and STATISTICS IO and TIME; on PostgreSQL EXPLAIN ANALYZE timings, buffers and settings; on MySQL and " +
			"MariaDB access types, keys, filtered rows and timings.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryPlanInput) (*mcp.CallToolResult, any, error) {
		result, err := q.Plan(ctx, &PlanRequest{
			Database: in.Database, SQL: in.SQL, Parameters: in.Parameters, Values: in.Values,
			Mode: in.Mode, IncludeXML: in.IncludeXML, IncludeJSON: in.IncludeJSON,
		})
		return mcpserve.ToolResult(result, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "run_script",
		Description: scriptDescription(cfg.Engine),
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in runScriptInput) (*mcp.CallToolResult, any, error) {
		script, file, err := readScript(in.FilePath, in.SQL)
		if err != nil {
			return mcpserve.ToolResult(nil, err)
		}
		result, err := q.Script(ctx, &ScriptRequest{
			Database: in.Database, SQL: script, File: file,
			MaxRows: mcpserve.Limit(in.MaxRows, defaultMaxRows, maxRowsLimit),
			Timeout: time.Duration(mcpserve.Limit(in.TimeoutSeconds, defaultScriptTimeout, maxScriptTimeout)) * time.Second,
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
			"The query tool runs one SELECT/WITH statement and returns at most %d rows; export writes all of them "+
			"to a local file instead, without returning them; run_script runs a read-only script, such as a "+
			"local .sql file too large to pass inline.%s",
		cfg.Target, cfg.Engine.Dialect(), via, access, maxRowsLimit, planInstructions(cfg.Engine),
	)
}

// scriptDescription describes run_script on engine.
func scriptDescription(engine Engine) string {
	const source = "from a local .sql file or inline, in a transaction always rolled back, and return its result sets with the errors of the statements that failed, which do not stop the next ones."
	if engine == EngineSQLServer {
		return "Run a read-only T-SQL script, " + source + " GO lines separate its batches. Besides SELECT and WITH, " +
			"a batch may DECLARE and SET @variables, use IF, WHILE, BEGIN … END and TRY … CATCH, and create, fill " +
			"and drop temporary tables (#name) and table variables (@name); EXEC and transaction statements are refused."
	}
	return fmt.Sprintf("Run a read-only %s script of %s statements separated by ';', %s The transaction is read-only.",
		engine.Dialect(), readStatements(engine), source)
}

func planInstructions(engine Engine) string {
	switch {
	case engine == EnginePostgres:
		return " To analyze a slow query, get its plan with query_plan (EXPLAIN ANALYZE, rolled back), passing " +
			"the values of $1, $2… the application sends; pg_stat_statements tells which queries cost the most, when installed."
	case engine.mysqlFamily():
		return " MySQL schemas are databases: list_tables lists the current database, or the schema given. " +
			"To analyze a slow query, get its plan with query_plan (EXPLAIN ANALYZE, rolled back; ANALYZE on MariaDB), " +
			"passing the values of ? the application sends; performance_schema.events_statements_summary_by_digest " +
			"tells which queries cost the most, when readable."
	}
	return " To analyze a slow query, get its plan with query_plan, passing the parameters the application sends " +
		"(e.g. (@p0 int,@p1 nvarchar(6))select ... becomes parameters \"@p0 int,@p1 nvarchar(6)\"); " +
		"Query Store (sys.query_store_*) tells which queries cost the most, when the login may read it."
}
