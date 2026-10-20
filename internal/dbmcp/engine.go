// Package dbmcp serves MCP servers giving AI assistants access to databases reached through jumphost tunnels.
package dbmcp

import "strings"

// Engine is a database engine supported by the MCP server.
type Engine string

const (
	EnginePostgres  Engine = "postgres"
	EngineSQLServer Engine = "sqlserver"
	// EngineMySQL is RDS MySQL and Aurora MySQL; EngineMariaDB differs in timeouts and plans.
	EngineMySQL   Engine = "mysql"
	EngineMariaDB Engine = "mariadb"
)

// EngineFor maps an AWS RDS engine name to a supported engine.
func EngineFor(awsEngine string) (Engine, bool) {
	engine := strings.ToLower(strings.TrimSpace(awsEngine))
	switch {
	case engine == "postgres" || engine == "aurora-postgresql":
		return EnginePostgres, true
	case strings.HasPrefix(engine, "sqlserver"):
		return EngineSQLServer, true
	case engine == "mysql" || engine == "aurora-mysql" || engine == "aurora":
		return EngineMySQL, true
	case engine == "mariadb":
		return EngineMariaDB, true
	default:
		return "", false
	}
}

// Dialect names the SQL dialect of the engine for the AI assistant.
func (e Engine) Dialect() string {
	switch e {
	case EngineSQLServer:
		return "T-SQL (SQL Server)"
	case EngineMySQL:
		return "MySQL"
	case EngineMariaDB:
		return "MariaDB"
	default:
		return "PostgreSQL"
	}
}

// mysqlFamily reports whether e speaks the MySQL protocol and SQL dialect.
func (e Engine) mysqlFamily() bool {
	return e == EngineMySQL || e == EngineMariaDB
}

// direct reports whether e is reached directly, without tunnel: every supported engine is.
func (e Engine) direct() bool {
	return e == EnginePostgres || e == EngineSQLServer || e.mysqlFamily()
}
