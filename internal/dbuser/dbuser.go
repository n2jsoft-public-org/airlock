// Package dbuser holds the default database users per engine, written as
// "user,engine=user,...".
package dbuser

import (
	"errors"
	"fmt"
	"strings"
)

// Engines of the engine=user entries.
const (
	Postgres  = "postgres"
	MySQL     = "mysql"
	SQLServer = "sqlserver"
)

// Defaults are the default database users; the zero Defaults means "not set".
type Defaults struct {
	// IAM is the bare user: the default of the engines signing in with AWS IAM (PostgreSQL and
	// MySQL) that have no user of their own. It never applies to SQL Server.
	IAM       string
	Postgres  string
	MySQL     string
	SQLServer string
}

// Parse reads comma-separated entries, each "engine=user" (engine postgres, mysql or sqlserver,
// any case) or a bare user; spaces around them are trimmed. A blank string is the zero Defaults.
// An unknown engine, an engine given twice, two bare users, an empty entry or a user with spaces
// is an error.
func Parse(s string) (Defaults, error) {
	var d Defaults
	if strings.TrimSpace(s) == "" {
		return d, nil
	}
	for entry := range strings.SplitSeq(s, ",") {
		engine, user, hasEngine := strings.Cut(entry, "=")
		if !hasEngine {
			engine, user = "", engine
		}
		engine, user = strings.ToLower(strings.TrimSpace(engine)), strings.TrimSpace(user)
		if user == "" {
			return Defaults{}, fmt.Errorf("invalid entry %q: expected user or engine=user", strings.TrimSpace(entry))
		}
		if strings.ContainsAny(user, " \t") {
			return Defaults{}, fmt.Errorf("invalid user %q: must not contain spaces", user)
		}
		field, err := fieldOf(&d, engine, hasEngine)
		if err != nil {
			return Defaults{}, err
		}
		if *field != "" {
			if hasEngine {
				return Defaults{}, fmt.Errorf("engine %s is given twice", engine)
			}
			return Defaults{}, errors.New("only one user may be given without an engine")
		}
		*field = user
	}
	return d, nil
}

// fieldOf returns the field of d for engine; the bare user when hasEngine is false. A function,
// not a method: Defaults keeps value receivers only.
func fieldOf(d *Defaults, engine string, hasEngine bool) (*string, error) {
	switch {
	case !hasEngine:
		return &d.IAM, nil
	case engine == Postgres:
		return &d.Postgres, nil
	case engine == MySQL:
		return &d.MySQL, nil
	case engine == SQLServer:
		return &d.SQLServer, nil
	default:
		return nil, fmt.Errorf("unknown engine %q (valid: %s, %s, %s)", engine, Postgres, MySQL, SQLServer)
	}
}

// For returns the default user of awsEngine, an AWS RDS engine name such as aurora-postgresql or
// sqlserver-se; "" when none is set or the engine is not supported.
func (d Defaults) For(awsEngine string) string {
	switch engine := strings.ToLower(strings.TrimSpace(awsEngine)); {
	case engine == "postgres" || engine == "aurora-postgresql":
		return firstNonEmpty(d.Postgres, d.IAM)
	case engine == "mysql" || engine == "aurora-mysql" || engine == "aurora" || engine == "mariadb":
		return firstNonEmpty(d.MySQL, d.IAM)
	case strings.HasPrefix(engine, "sqlserver"):
		return d.SQLServer
	default:
		return ""
	}
}

// String is the normalized form: the bare user first, then postgres, mysql and sqlserver; "" for
// the zero Defaults.
func (d Defaults) String() string {
	var entries []string
	if d.IAM != "" {
		entries = append(entries, d.IAM)
	}
	for _, e := range []struct{ engine, user string }{{Postgres, d.Postgres}, {MySQL, d.MySQL}, {SQLServer, d.SQLServer}} {
		if e.user != "" {
			entries = append(entries, e.engine+"="+e.user)
		}
	}
	return strings.Join(entries, ",")
}

// IsZero reports whether no user is set.
func (d Defaults) IsZero() bool {
	return d == Defaults{}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
