package dbmcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang-sql/sqlexp"
)

// Plan modes of the query_plan tool.
const (
	// PlanActual runs the statement, in a transaction rolled back, and returns the plan it ran with.
	PlanActual = "actual"
	// PlanEstimated compiles the statement without running it.
	PlanEstimated = "estimated"
)

// showplanColumn names the column of the result sets SQL Server adds to carry a showplan.
const showplanColumn = "Microsoft SQL Server 2005 XML Showplan"

// PlanRequest asks for the execution plan of one read-only statement.
type PlanRequest struct {
	Database string
	SQL      string
	// Parameters declares the parameters SQL uses, as sp_executesql does: "@p0 int, @p1 nvarchar(6)".
	Parameters string
	// Values holds one value per declared parameter, in order: string, number, bool or nil.
	Values     []any
	Mode       string
	IncludeXML bool
}

// PlanResult is the execution plan of a statement, summarized for reading.
type PlanResult struct {
	Mode string `json:"mode"`
	// RowCount is the number of rows the statement returned (actual plans only).
	RowCount *int64 `json:"row_count,omitempty"`
	// Messages are the STATISTICS IO and TIME lines (actual plans only).
	Messages   []string        `json:"messages,omitempty"`
	Statements []StatementPlan `json:"statements"`
	// PlanXML holds the raw showplans, one per statement, to save as .sqlplan files.
	PlanXML []string `json:"plan_xml,omitempty"`
}

// planSettings returns the statements turning plan capture on, then off, for mode.
// SHOWPLAN_XML must be the only statement of its batch, hence one statement each.
func planSettings(mode string) (on, off []string) {
	if mode == PlanEstimated {
		return []string{"SET SHOWPLAN_XML ON"}, []string{"SET SHOWPLAN_XML OFF"}
	}
	return []string{"SET STATISTICS XML ON", "SET STATISTICS IO ON", "SET STATISTICS TIME ON"},
		[]string{"SET STATISTICS TIME OFF", "SET STATISTICS IO OFF", "SET STATISTICS XML OFF"}
}

// Plan returns the execution plan of a read-only statement (SQL Server only).
func (q *sqlQuerier) Plan(ctx context.Context, req PlanRequest) (PlanResult, error) {
	if q.dialect.engine != EngineSQLServer {
		return PlanResult{}, errors.New("query plans are only available on SQL Server; on PostgreSQL run EXPLAIN with the query tool")
	}
	mode := req.Mode
	if mode == "" {
		mode = PlanActual
	}
	if mode != PlanActual && mode != PlanEstimated {
		return PlanResult{}, fmt.Errorf("mode must be %s or %s (got %s)", PlanActual, PlanEstimated, mode)
	}
	if err := CheckReadOnly(q.dialect.engine, req.SQL); err != nil {
		return PlanResult{}, err
	}
	batch, args, err := planBatch(req.SQL, req.Parameters, req.Values, mode)
	if err != nil {
		return PlanResult{}, err
	}

	var result PlanResult
	var plans []string
	err = q.withPool(ctx, req.Database, func(db *sql.DB) (err error) {
		result, plans, err = q.planFrom(ctx, db, mode, batch, args)
		return err
	})
	if err != nil {
		return PlanResult{}, err
	}
	result.Statements = []StatementPlan{}
	for _, plan := range plans {
		statements, err := summarizeShowplan(plan)
		if err != nil {
			return PlanResult{}, fmt.Errorf("reading the plan: %w", err)
		}
		result.Statements = append(result.Statements, statements...)
	}
	if req.IncludeXML {
		result.PlanXML = plans
	}
	return result, nil
}

// planBatch returns the batch to run and its arguments. A parameterized statement runs through
// sp_executesql, as applications send it, so the plan is compiled for the declared parameter types
// and sniffed values. The statement, declaration and values are all passed as bound arguments.
func planBatch(statement, parameters string, values []any, mode string) (string, []any, error) {
	names, err := parameterNames(parameters)
	if err != nil {
		return "", nil, err
	}
	if len(names) != len(values) {
		return "", nil, fmt.Errorf("parameters declares %d parameter(s) but %d value(s) were given", len(names), len(values))
	}
	if len(names) == 0 {
		return statement, nil, nil
	}
	if mode == PlanEstimated {
		// sp_executesql is not run under SHOWPLAN_XML, so its statement is never compiled.
		return "", nil, errors.New("estimated plans cannot take parameters: use the actual mode, or inline the values")
	}

	var b strings.Builder
	b.WriteString("EXEC sp_executesql @airlock_stmt, @airlock_params")
	args := []any{sql.Named("airlock_stmt", statement), sql.Named("airlock_params", parameters)}
	for i, name := range names {
		value, err := parameterValue(values[i])
		if err != nil {
			return "", nil, fmt.Errorf("value of %s: %w", name, err)
		}
		fmt.Fprintf(&b, ", %s = @airlock_v%d", name, i)
		args = append(args, sql.Named(fmt.Sprintf("airlock_v%d", i), value))
	}
	return b.String(), args, nil
}

// parameterPattern matches one declared parameter: a name and a type, with an optional length,
// precision and scale. Nothing else (OUTPUT, defaults) is allowed, as the names end up in the batch.
var parameterPattern = regexp.MustCompile(`(?i)^\s*(@[a-z_][a-z0-9_]*)\s+[a-z_][a-z0-9_]*(\s*\(\s*(\d+|max)\s*(,\s*\d+\s*)?\))?\s*$`)

// parameterNames returns the names declared by parameters, e.g. "@p0 int, @p1 decimal(10, 2)".
func parameterNames(parameters string) ([]string, error) {
	if strings.TrimSpace(parameters) == "" {
		return nil, nil
	}
	// Split on the commas outside parentheses: decimal(10, 2) holds one.
	var items []string
	depth, start := 0, 0
	for i, r := range parameters {
		switch {
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == ',' && depth == 0:
			items = append(items, parameters[start:i])
			start = i + 1
		}
		if depth < 0 {
			break
		}
	}
	items = append(items, parameters[start:])

	names := make([]string, 0, len(items))
	for _, item := range items {
		match := parameterPattern.FindStringSubmatch(item)
		if match == nil || depth != 0 {
			return nil, fmt.Errorf("invalid parameter declaration %q: expected \"@name type\"", strings.TrimSpace(item))
		}
		names = append(names, match[1])
	}
	return names, nil
}

// parameterValue converts a JSON value to text: SQL Server converts it to the declared type.
func parameterValue(value any) (any, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case string:
		return v, nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil
	default:
		return nil, fmt.Errorf("unsupported value %v: use a string, number, boolean or null", value)
	}
}

// planFrom runs batch with plan capture on, in a transaction always rolled back, and returns the
// statistics messages and the showplans.
func (q *sqlQuerier) planFrom(ctx context.Context, db *sql.DB, mode, batch string, args []any) (PlanResult, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PlanResult{}, nil, q.connectionError(err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // the transaction only reads; ending it is all that matters.

	on, off := planSettings(mode)
	for _, statement := range append(append([]string(nil), q.dialect.txSetup...), on...) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return PlanResult{}, nil, err
		}
	}

	result, plans, err := readPlan(ctx, tx, mode, batch, args)
	if err != nil {
		return PlanResult{}, nil, err
	}
	// The settings are per connection: turn them off before it goes back to the pool.
	for _, statement := range off {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return PlanResult{}, nil, err
		}
	}
	return result, plans, nil
}

// readPlan runs batch and reads every result set: showplans are kept, the statement's rows are
// only counted. Info messages carry the STATISTICS IO and TIME output.
func readPlan(ctx context.Context, tx *sql.Tx, mode, batch string, args []any) (PlanResult, []string, error) {
	messages := &sqlexp.ReturnMessage{}
	rows, err := tx.QueryContext(ctx, batch, append(args, messages)...)
	if err != nil {
		return PlanResult{}, nil, err
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // errors are reported through the messages.

	result := PlanResult{Mode: mode}
	var plans []string
	var rowCount int64
	for active := true; active; {
		switch m := messages.Message(ctx).(type) {
		case sqlexp.MsgNotice:
			if text := strings.Join(strings.Fields(m.Message.String()), " "); text != "" {
				result.Messages = append(result.Messages, text)
			}
		case sqlexp.MsgNext:
			plan, counted, err := readResultSet(rows)
			if err != nil {
				return PlanResult{}, nil, err
			}
			if plan != "" {
				plans = append(plans, plan)
			}
			rowCount += counted
		case sqlexp.MsgNextResultSet:
			active = rows.NextResultSet()
		case sqlexp.MsgError:
			return PlanResult{}, nil, m.Error
		}
	}
	if err := ctx.Err(); err != nil {
		return PlanResult{}, nil, err
	}
	if err := rows.Err(); err != nil {
		return PlanResult{}, nil, err
	}
	if mode == PlanActual {
		result.RowCount = &rowCount
	}
	return result, plans, nil
}

// readResultSet returns the showplan of a plan result set, or the number of rows of any other.
func readResultSet(rows *sql.Rows) (plan string, count int64, err error) {
	columns, err := rows.Columns()
	if err != nil {
		return "", 0, err
	}
	if len(columns) == 1 && columns[0] == showplanColumn {
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				return "", 0, err
			}
			plan += text
		}
		return plan, 0, rows.Err()
	}
	values := make([]any, len(columns))
	for i := range values {
		values[i] = new(sql.RawBytes)
	}
	for rows.Next() {
		if err := rows.Scan(values...); err != nil {
			return "", 0, err
		}
		count++
	}
	return "", count, rows.Err()
}
