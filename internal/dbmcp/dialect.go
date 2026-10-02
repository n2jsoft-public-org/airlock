package dbmcp

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	mssql "github.com/microsoft/go-mssqldb"
)

// applicationName identifies airlock MCP connections in database activity views.
const applicationName = "airlock-mcp"

// dialect holds the engine-specific SQL used by sqlQuerier.
type dialect struct {
	engine         Engine
	defaultSchema  string
	databasesSQL   string
	tablesSQL      string
	columnsSQL     string
	primaryKeySQL  string
	foreignKeysSQL string
	indexesSQL     string
	// txSetup runs at the start of every read transaction.
	txSetup    []string
	readOnlyTx bool
}

var postgresDialect = dialect{
	engine:        EnginePostgres,
	defaultSchema: "public",
	readOnlyTx:    true,
	txSetup:       []string{"SET LOCAL standard_conforming_strings = on", "SET LOCAL statement_timeout = '30s'"},
	databasesSQL:  "SELECT datname FROM pg_database WHERE NOT datistemplate AND datallowconn ORDER BY datname",
	tablesSQL: `SELECT table_schema, table_name, table_type FROM information_schema.tables
WHERE table_schema NOT IN ('pg_catalog', 'information_schema') AND ($1 = '' OR table_schema = $1)
ORDER BY table_schema, table_name`,
	columnsSQL: `SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position`,
	primaryKeySQL: `SELECT kcu.column_name FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON kcu.constraint_name = tc.constraint_name AND kcu.table_schema = tc.table_schema AND kcu.table_name = tc.table_name
WHERE tc.constraint_type = 'PRIMARY KEY' AND tc.table_schema = $1 AND tc.table_name = $2
ORDER BY kcu.ordinal_position`,
	foreignKeysSQL: `SELECT con.conname, a.attname, nf.nspname, cf.relname, af.attname
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_class cf ON cf.oid = con.confrelid
JOIN pg_namespace nf ON nf.oid = cf.relnamespace
CROSS JOIN LATERAL unnest(con.conkey, con.confkey) AS k(attnum, fattnum)
JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
JOIN pg_attribute af ON af.attrelid = con.confrelid AND af.attnum = k.fattnum
WHERE con.contype = 'f' AND n.nspname = $1 AND c.relname = $2
ORDER BY con.conname`,
	indexesSQL: "SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = $1 AND tablename = $2 ORDER BY indexname",
}

var sqlServerDialect = dialect{
	engine:        EngineSQLServer,
	defaultSchema: "dbo",
	// go-mssqldb has no read-only transactions: rely on rollback and grants.
	readOnlyTx:   false,
	txSetup:      []string{"SET LOCK_TIMEOUT 5000"},
	databasesSQL: "SELECT name FROM sys.databases WHERE database_id > 4 AND name <> 'rdsadmin' ORDER BY name",
	tablesSQL: `SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE FROM INFORMATION_SCHEMA.TABLES
WHERE (@p1 = '' OR TABLE_SCHEMA = @p1) ORDER BY TABLE_SCHEMA, TABLE_NAME`,
	columnsSQL: `SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = @p1 AND TABLE_NAME = @p2 ORDER BY ORDINAL_POSITION`,
	primaryKeySQL: `SELECT kcu.COLUMN_NAME FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu
  ON kcu.CONSTRAINT_NAME = tc.CONSTRAINT_NAME AND kcu.TABLE_SCHEMA = tc.TABLE_SCHEMA AND kcu.TABLE_NAME = tc.TABLE_NAME
WHERE tc.CONSTRAINT_TYPE = 'PRIMARY KEY' AND tc.TABLE_SCHEMA = @p1 AND tc.TABLE_NAME = @p2
ORDER BY kcu.ORDINAL_POSITION`,
	foreignKeysSQL: `SELECT fk.name, pc.name, SCHEMA_NAME(rt.schema_id), rt.name, rc.name
FROM sys.foreign_keys fk
JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
JOIN sys.tables pt ON pt.object_id = fk.parent_object_id
JOIN sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
JOIN sys.tables rt ON rt.object_id = fk.referenced_object_id
JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
WHERE SCHEMA_NAME(pt.schema_id) = @p1 AND pt.name = @p2
ORDER BY fk.name, fkc.constraint_column_id`,
	indexesSQL: `SELECT i.name, CONCAT(CASE WHEN i.is_unique = 1 THEN 'UNIQUE ' ELSE '' END, i.type_desc, ' (',
  STRING_AGG(c.name, ', ') WITHIN GROUP (ORDER BY ic.key_ordinal), ')')
FROM sys.indexes i
JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.is_included_column = 0
JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
WHERE i.object_id = OBJECT_ID(QUOTENAME(@p1) + '.' + QUOTENAME(@p2)) AND i.name IS NOT NULL
GROUP BY i.name, i.is_unique, i.type_desc
ORDER BY i.name`,
}

// newQuerier returns the Querier for engine, connecting to addr: the tunnel, or a database reached directly.
func newQuerier(engine Engine, creds CredentialProvider, addr Address) *sqlQuerier {
	if engine == EngineSQLServer {
		return newSQLQuerier(&sqlServerDialect, creds, addr, sqlServerOpener(creds, addr))
	}
	return newSQLQuerier(&postgresDialect, creds, addr, postgresOpener(creds, addr))
}

// postgresOpener fetches credentials for every new connection, so rotating IAM tokens keep working.
func postgresOpener(creds CredentialProvider, addr Address) opener {
	return func(_ context.Context, database string) (*sql.DB, error) {
		config, err := pgx.ParseConfig(postgresConnString(addr))
		if err != nil {
			return nil, err
		}
		config.Database = database
		return stdlib.OpenDB(*config, stdlib.OptionBeforeConnect(func(ctx context.Context, cc *pgx.ConnConfig) error {
			c, err := creds.Credentials(ctx, database)
			if err != nil {
				return err
			}
			cc.User, cc.Password = c.User, c.Password
			return nil
		})), nil
	}
}

func sqlServerOpener(creds CredentialProvider, addr Address) opener {
	return func(ctx context.Context, database string) (*sql.DB, error) {
		c, err := creds.Credentials(ctx, database)
		if err != nil {
			return nil, err
		}
		connector, err := mssql.NewConnector(sqlServerDSN(addr, c))
		if err != nil {
			return nil, fmt.Errorf("invalid SQL Server connection settings: %w", err)
		}
		return sql.OpenDB(connector), nil
	}
}
