package ideadatasource

import (
	"fmt"
	"strings"
)

// Driver describes how an IntelliJ data source connects to a given engine.
type Driver struct {
	urlFormat func(host string, port int, database string) string
	Ref       string
	ClassName string
}

// URL builds the JDBC URL for the driver.
func (d Driver) URL(host string, port int, database string) string {
	return d.urlFormat(host, port, database)
}

var (
	postgresDriver = Driver{
		Ref:       "postgresql",
		ClassName: "org.postgresql.Driver",
		urlFormat: func(host string, port int, database string) string {
			return fmt.Sprintf("jdbc:postgresql://%s:%d/%s", host, port, database)
		},
	}
	mysqlDriver = Driver{
		Ref:       "mysql.8",
		ClassName: "com.mysql.cj.jdbc.Driver",
		urlFormat: func(host string, port int, database string) string {
			return fmt.Sprintf("jdbc:mysql://%s:%d/%s", host, port, database)
		},
	}
	mariadbDriver = Driver{
		Ref:       "mariadb",
		ClassName: "org.mariadb.jdbc.Driver",
		urlFormat: func(host string, port int, database string) string {
			return fmt.Sprintf("jdbc:mariadb://%s:%d/%s", host, port, database)
		},
	}
	sqlServerDriver = Driver{
		Ref:       "sqlserver.ms",
		ClassName: "com.microsoft.sqlserver.jdbc.SQLServerDriver",
		urlFormat: func(host string, port int, database string) string {
			url := fmt.Sprintf("jdbc:sqlserver://%s:%d", host, port)
			if database != "" {
				url += ";databaseName=" + database
			}
			return url
		},
	}
	oracleDriver = Driver{
		Ref:       "oracle",
		ClassName: "oracle.jdbc.OracleDriver",
		urlFormat: func(host string, port int, database string) string {
			if database == "" {
				database = "ORCL"
			}
			return fmt.Sprintf("jdbc:oracle:thin:@%s:%d:%s", host, port, database)
		},
	}
	redisDriver = Driver{
		Ref:       "redis",
		ClassName: "jdbc.RedisDriver",
		urlFormat: func(host string, port int, _ string) string {
			return fmt.Sprintf("jdbc:redis://%s:%d/0", host, port)
		},
	}
)

// DriverForEngine maps an AWS engine name (RDS or ElastiCache) to an IntelliJ driver.
func DriverForEngine(engine string) (Driver, bool) {
	engine = strings.ToLower(strings.TrimSpace(engine))
	switch {
	case engine == "postgres" || engine == "aurora-postgresql":
		return postgresDriver, true
	case engine == "mysql" || engine == "aurora-mysql" || engine == "aurora":
		return mysqlDriver, true
	case engine == "mariadb":
		return mariadbDriver, true
	case strings.HasPrefix(engine, "sqlserver"):
		return sqlServerDriver, true
	case strings.HasPrefix(engine, "oracle"):
		return oracleDriver, true
	case engine == "redis" || engine == "valkey":
		return redisDriver, true
	default:
		return Driver{}, false
	}
}
