package dbmcp

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// forbiddenKeywords may not appear anywhere in a read-only query, outside literals and comments.
var forbiddenKeywords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "UPSERT": true,
	"DROP": true, "ALTER": true, "CREATE": true, "TRUNCATE": true, "RENAME": true,
	"EXEC": true, "EXECUTE": true, "CALL": true, "DO": true,
	"GRANT": true, "REVOKE": true, "DENY": true,
	"INTO": true, "COPY": true, "SET": true, "DECLARE": true,
	"LOCK": true, "VACUUM": true, "ANALYZE": true, "REINDEX": true, "CLUSTER": true,
	"BACKUP": true, "RESTORE": true, "SHUTDOWN": true, "KILL": true, "DBCC": true,
	"OPENROWSET": true, "OPENQUERY": true, "OPENDATASOURCE": true, "BULK": true,
	// T-SQL runs several statements in one batch without ";", so transaction control and other
	// statement keywords must be forbidden explicitly rather than relying on statement splitting.
	"COMMIT": true, "ROLLBACK": true, "SAVE": true, "BEGIN": true, "TRAN": true, "TRANSACTION": true,
	"DISABLE": true, "ENABLE": true, "WRITETEXT": true, "UPDATETEXT": true, "READTEXT": true,
	"RECEIVE": true, "SEND": true, "RECONFIGURE": true, "CHECKPOINT": true, "SETUSER": true,
	"REVERT": true, "WAITFOR": true,
}

// CheckReadOnly rejects anything but a single SELECT/WITH statement (plus EXPLAIN on PostgreSQL, and
// SHOW, DESCRIBE and EXPLAIN on MySQL). It is a guardrail against mistakes; the database grants
// remain the security boundary.
func CheckReadOnly(engine Engine, query string) error {
	code, err := stripLiterals(engine, query)
	if err != nil {
		return err
	}

	statements := 0
	for _, part := range strings.Split(code, ";") {
		if strings.TrimSpace(part) != "" {
			statements++
		}
	}
	if statements == 0 {
		return errors.New("query is empty")
	}
	if statements > 1 {
		return errors.New("only a single statement is allowed")
	}

	words := keywords(code)
	first := words[0]
	allowed := first == "SELECT" || first == "WITH" || (engine == EnginePostgres && first == "EXPLAIN") ||
		(engine.mysqlFamily() && (first == "SHOW" || first == "DESCRIBE" || first == "DESC" || first == "EXPLAIN"))
	if !allowed {
		return fmt.Errorf("only %s queries are allowed (got %s); use the execute tool for other statements when writes are enabled", readStatements(engine), first)
	}
	if first == "SHOW" {
		// SHOW only reads, and names statements: SHOW CREATE TABLE, SHOW GRANTS.
		return nil
	}
	for _, word := range words {
		if forbiddenKeywords[word] {
			return fmt.Errorf("keyword %s is not allowed in a read-only query", word)
		}
	}
	return nil
}

// readStatements names the statements CheckReadOnly allows on engine.
func readStatements(engine Engine) string {
	if engine.mysqlFamily() {
		return "SELECT, WITH, SHOW, DESCRIBE or EXPLAIN"
	}
	return "SELECT or WITH"
}

// stripLiterals replaces comments, string literals and quoted identifiers with spaces,
// so that keywords and semicolons inside them are ignored.
func stripLiterals(engine Engine, query string) (string, error) {
	mysqlFamily := engine.mysqlFamily()
	var out strings.Builder
	runes := []rune(query)
	for i := 0; i < len(runes); {
		c := runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		switch {
		case (c == '-' && next == '-' && (!mysqlFamily || mysqlDashComment(runes, i))) || (c == '#' && mysqlFamily):
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			out.WriteRune(' ')
		case c == '/' && next == '*' && mysqlFamily:
			// MySQL runs the content of /*! */ (and MariaDB of /*M! */) comments, and does not nest them.
			if i+2 < len(runes) && (runes[i+2] == '!' || (runes[i+2] == 'M' && i+3 < len(runes) && runes[i+3] == '!')) {
				return "", errors.New("executable comments (/*! ... */) are not allowed")
			}
			end := indexRunes(runes, i+2, []rune("*/"))
			if end < 0 {
				return "", errors.New("unterminated comment")
			}
			i = end + 2
			out.WriteRune(' ')
		case c == '/' && next == '*':
			depth := 0
			for i < len(runes) {
				if runes[i] == '/' && i+1 < len(runes) && runes[i+1] == '*' {
					depth++
					i += 2
					continue
				}
				if runes[i] == '*' && i+1 < len(runes) && runes[i+1] == '/' {
					depth--
					i += 2
					if depth == 0 {
						break
					}
					continue
				}
				i++
			}
			if depth != 0 {
				return "", errors.New("unterminated comment")
			}
			out.WriteRune(' ')
		case c == '\'' || c == '"' || (c == '[' && engine == EngineSQLServer) || (c == '`' && mysqlFamily):
			closing := c
			if c == '[' {
				closing = ']'
			}
			var end int
			switch {
			// ponytail: MySQL strings take backslash escapes unless sql_mode has NO_BACKSLASH_ESCAPES (not
			// the RDS default); the read-only transaction and one-statement protocol back the guard there.
			case (c == '\'' || c == '"') && mysqlFamily:
				end = escapeStringEnd(runes, i+1, c)
			case c == '\'' && engine == EnginePostgres && i > 0 && (runes[i-1] == 'E' || runes[i-1] == 'e') && (i == 1 || !isIdentContinuation(runes[i-2])):
				end = escapeStringEnd(runes, i+1, '\'')
			default:
				end = closingIndex(runes, i+1, closing)
			}
			if end < 0 {
				return "", errors.New("unterminated quoted text")
			}
			i = end + 1
			out.WriteRune(' ')
		case c == '$' && engine == EnginePostgres:
			// "$" is legal inside a PostgreSQL identifier (e.g. price$usd), so it only opens a
			// dollar-quoted string when it is not itself continuing an identifier or a previous quote.
			if i > 0 && isIdentContinuation(runes[i-1]) {
				out.WriteRune(c)
				i++
				continue
			}
			tag, ok := dollarTag(runes, i)
			if !ok {
				out.WriteRune(c)
				i++
				continue
			}
			end := indexRunes(runes, i+len(tag), tag)
			if end < 0 {
				return "", errors.New("unterminated dollar-quoted text")
			}
			i = end + len(tag)
			out.WriteRune(' ')
		default:
			out.WriteRune(c)
			i++
		}
	}
	return out.String(), nil
}

// closingIndex finds the closing quote starting at from; a doubled quote is an escaped quote.
func closingIndex(runes []rune, from int, closing rune) int {
	for i := from; i < len(runes); i++ {
		if runes[i] != closing {
			continue
		}
		if i+1 < len(runes) && runes[i+1] == closing {
			i++
			continue
		}
		return i
	}
	return -1
}

// escapeStringEnd finds the closing quote of a string taking backslash escapes (a PostgreSQL E'...'
// escape string, a MySQL string) starting at from: \' and a doubled quote are escaped quotes.
func escapeStringEnd(runes []rune, from int, quote rune) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == '\\' && i+1 < len(runes) {
			i++
			continue
		}
		if runes[i] != quote {
			continue
		}
		if i+1 < len(runes) && runes[i+1] == quote {
			i++
			continue
		}
		return i
	}
	return -1
}

// mysqlDashComment reports whether the "--" at i starts a MySQL comment: it must be followed by
// whitespace, a control character or the end (1--1 is a subtraction).
func mysqlDashComment(runes []rune, i int) bool {
	return i+2 >= len(runes) || unicode.IsSpace(runes[i+2]) || unicode.IsControl(runes[i+2])
}

// isIdentContinuation reports whether r may appear after the first character of a PostgreSQL
// identifier: a letter, digit, underscore or dollar sign.
func isIdentContinuation(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// dollarTag returns the PostgreSQL dollar-quote tag ($$ or $name$) starting at i.
func dollarTag(runes []rune, i int) ([]rune, bool) {
	for j := i + 1; j < len(runes); j++ {
		switch r := runes[j]; {
		case r == '$':
			return runes[i : j+1], true
		case r == '_' || unicode.IsLetter(r) || (j > i+1 && unicode.IsDigit(r)):
			continue
		default:
			return nil, false
		}
	}
	return nil, false
}

// indexRunes returns the index of the first occurrence of needle in runes at or after from, or -1.
func indexRunes(runes []rune, from int, needle []rune) int {
	for i := from; i+len(needle) <= len(runes); i++ {
		if string(runes[i:i+len(needle)]) == string(needle) {
			return i
		}
	}
	return -1
}

// keywords returns the upper-cased words of code, in order.
func keywords(code string) []string {
	return strings.FieldsFunc(strings.ToUpper(code), func(r rune) bool {
		return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
