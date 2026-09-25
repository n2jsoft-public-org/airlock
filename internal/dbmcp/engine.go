// Package dbmcp serves MCP servers giving AI assistants access to databases reached through jumphost tunnels.
package dbmcp

import "strings"

// Engine is a database engine supported by the MCP server.
type Engine string

const (
	EnginePostgres  Engine = "postgres"
	EngineSQLServer Engine = "sqlserver"
)

// EngineFor maps an AWS RDS engine name to a supported engine.
func EngineFor(awsEngine string) (Engine, bool) {
	engine := strings.ToLower(strings.TrimSpace(awsEngine))
	switch {
	case engine == "postgres" || engine == "aurora-postgresql":
		return EnginePostgres, true
	case strings.HasPrefix(engine, "sqlserver"):
		return EngineSQLServer, true
	default:
		return "", false
	}
}

// Dialect names the SQL dialect of the engine for the AI assistant.
func (e Engine) Dialect() string {
	if e == EngineSQLServer {
		return "T-SQL (SQL Server)"
	}
	return "PostgreSQL"
}
