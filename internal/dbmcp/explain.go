package dbmcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Thresholds of the PostgreSQL plan warnings.
const (
	// estimateMissRatio is how far apart estimated and actual rows must be to warn.
	estimateMissRatio = 10
	// estimateMissRows is the row count below which a misestimate does not matter.
	estimateMissRows = 100
	// largeScanRows is the number of rows a sequential scan must read to warn.
	largeScanRows = 100_000
)

// explainStatement returns the EXPLAIN statement giving the plan of statement in mode.
func explainStatement(mode, statement string) string {
	if mode == PlanEstimated {
		return "EXPLAIN (VERBOSE, FORMAT JSON) " + statement
	}
	return "EXPLAIN (ANALYZE, BUFFERS, VERBOSE, SETTINGS, FORMAT JSON) " + statement
}

// postgresPlan returns the plan of a statement checked read-only. EXPLAIN ANALYZE runs it in a
// read-only transaction always rolled back, so even a statement slipping past the guard changes nothing.
func (q *sqlQuerier) postgresPlan(ctx context.Context, req *PlanRequest, mode string) (PlanResult, error) {
	if strings.TrimSpace(req.Parameters) != "" {
		return PlanResult{}, errors.New("PostgreSQL takes no parameter declaration: use $1, $2… in the statement " +
			"(cast where the type cannot be inferred, e.g. $1::int) and pass their values")
	}
	if req.IncludeXML {
		return PlanResult{}, errors.New("include_xml is for SQL Server: use include_json for the raw PostgreSQL plan")
	}
	args := make([]any, len(req.Values))
	for i, value := range req.Values {
		v, err := parameterValue(value)
		if err != nil {
			return PlanResult{}, fmt.Errorf("value of $%d: %w", i+1, err)
		}
		args[i] = v
	}

	var document string
	err := q.withPool(ctx, req.Database, func(db *sql.DB) (err error) {
		document, err = q.explainFrom(ctx, db, explainStatement(mode, req.SQL), args)
		return err
	})
	if err != nil {
		return PlanResult{}, err
	}
	statement, rowCount, err := summarizeExplain(document)
	if err != nil {
		return PlanResult{}, fmt.Errorf("reading the plan: %w", err)
	}
	statement.Statement = strings.Join(strings.Fields(req.SQL), " ")
	result := PlanResult{Mode: mode, Statements: []StatementPlan{statement}}
	if mode == PlanActual {
		result.RowCount = &rowCount
	}
	if req.IncludeJSON {
		result.PlanJSON = json.RawMessage(document)
	}
	return result, nil
}

// explainFrom runs an EXPLAIN statement in a read-only transaction always rolled back, and returns its JSON plan.
func (q *sqlQuerier) explainFrom(ctx context.Context, db *sql.DB, statement string, args []any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: q.dialect.readOnlyTx})
	if err != nil {
		return "", q.connectionError(err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // the transaction only reads; ending it is all that matters.

	for _, setup := range q.dialect.txSetup {
		if _, err := tx.ExecContext(ctx, setup); err != nil {
			return "", err
		}
	}
	var document string
	if err := tx.QueryRowContext(ctx, statement, args...).Scan(&document); err != nil {
		return "", err
	}
	return document, nil
}

// explainPlan is one statement of an EXPLAIN (FORMAT JSON) document.
type explainPlan struct {
	Settings      map[string]string `json:"Settings"`
	PlanningTime  *float64          `json:"Planning Time"`
	ExecutionTime *float64          `json:"Execution Time"`
	Plan          explainNode       `json:"Plan"`
}

// explainNode is one node of a PostgreSQL plan, with the fields the summary shows.
type explainNode struct {
	ActualLoops               *float64      `json:"Actual Loops"`
	NodeType                  string        `json:"Node Type"`
	Strategy                  string        `json:"Strategy"`
	JoinType                  string        `json:"Join Type"`
	SubplanName               string        `json:"Subplan Name"`
	Schema                    string        `json:"Schema"`
	RelationName              string        `json:"Relation Name"`
	Alias                     string        `json:"Alias"`
	IndexName                 string        `json:"Index Name"`
	CTEName                   string        `json:"CTE Name"`
	FunctionName              string        `json:"Function Name"`
	IndexCond                 string        `json:"Index Cond"`
	RecheckCond               string        `json:"Recheck Cond"`
	HashCond                  string        `json:"Hash Cond"`
	MergeCond                 string        `json:"Merge Cond"`
	JoinFilter                string        `json:"Join Filter"`
	Filter                    string        `json:"Filter"`
	SortMethod                string        `json:"Sort Method"`
	SortSpaceType             string        `json:"Sort Space Type"`
	SortKey                   []string      `json:"Sort Key"`
	Plans                     []explainNode `json:"Plans"`
	StartupCost               float64       `json:"Startup Cost"`
	TotalCost                 float64       `json:"Total Cost"`
	PlanRows                  float64       `json:"Plan Rows"`
	ActualStartupTime         float64       `json:"Actual Startup Time"`
	ActualTotalTime           float64       `json:"Actual Total Time"`
	ActualRows                float64       `json:"Actual Rows"`
	RowsRemovedByFilter       float64       `json:"Rows Removed by Filter"`
	RowsRemovedByJoinFilter   float64       `json:"Rows Removed by Join Filter"`
	RowsRemovedByIndexRecheck float64       `json:"Rows Removed by Index Recheck"`
	SortSpaceUsed             int64         `json:"Sort Space Used"`
	HashBatches               int64         `json:"Hash Batches"`
	SharedHitBlocks           int64         `json:"Shared Hit Blocks"`
	SharedReadBlocks          int64         `json:"Shared Read Blocks"`
	TempReadBlocks            int64         `json:"Temp Read Blocks"`
	TempWrittenBlocks         int64         `json:"Temp Written Blocks"`
	WorkersPlanned            int           `json:"Workers Planned"`
	WorkersLaunched           int           `json:"Workers Launched"`
	ParallelAware             bool          `json:"Parallel Aware"`
}

// summarizeExplain reads an EXPLAIN (FORMAT JSON) document of one statement, and returns its
// summary and the number of rows it returned (actual plans).
func summarizeExplain(document string) (StatementPlan, int64, error) {
	var plans []explainPlan
	if err := json.Unmarshal([]byte(document), &plans); err != nil {
		return StatementPlan{}, 0, err
	}
	if len(plans) != 1 {
		return StatementPlan{}, 0, fmt.Errorf("expected the plan of one statement, got %d", len(plans))
	}
	p, root := plans[0], plans[0].Plan
	s := StatementPlan{
		EstimatedCost:   root.TotalCost,
		EstimatedRows:   root.PlanRows,
		PlanningTimeMs:  p.PlanningTime,
		ExecutionTimeMs: p.ExecutionTime,
		Operators:       appendExplainNode(nil, &root, 0),
	}
	for name, value := range p.Settings {
		s.Settings = append(s.Settings, name+" = "+value)
	}
	slices.Sort(s.Settings)
	s.Warnings = appendExplainWarnings(nil, &root)

	var rowCount int64
	if root.ActualLoops != nil {
		rowCount = int64(math.Round(root.ActualRows * *root.ActualLoops))
	}
	return s, rowCount, nil
}

// title names a node as PostgreSQL's text EXPLAIN does, e.g. "Index Scan using orders_pkey on public.orders o".
func (n *explainNode) title() string {
	var b strings.Builder
	if n.SubplanName != "" {
		b.WriteString("[" + n.SubplanName + "] ")
	}
	if n.ParallelAware {
		b.WriteString("Parallel ")
	}
	b.WriteString(n.NodeType)
	if kind := n.Strategy + n.JoinType; kind != "" && kind != "Plain" && kind != "Inner" {
		b.WriteString(" (" + kind + ")")
	}
	if n.IndexName != "" {
		b.WriteString(" using " + n.IndexName)
	}
	if object := n.object(); object != "" {
		b.WriteString(" on " + object)
	}
	return b.String()
}

// object is the relation, CTE or function a node reads, with its alias.
func (n *explainNode) object() string {
	name := strings.Trim(n.Schema+"."+n.RelationName, ".")
	switch {
	case n.CTEName != "":
		name = "CTE " + n.CTEName
	case n.FunctionName != "":
		name = strings.Trim(n.Schema+"."+n.FunctionName, ".")
	}
	if n.Alias != "" && n.Alias != n.RelationName && n.Alias != n.CTEName && n.Alias != n.FunctionName {
		name = strings.TrimSpace(name + " " + n.Alias)
	}
	return name
}

// appendExplainNode renders n and its children, indented by level. Rows and times are per loop, as in EXPLAIN.
func appendExplainNode(lines []string, n *explainNode, level int) []string {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", level) + n.title())
	b.WriteString(" | cost " + formatNumber(n.StartupCost) + ".." + formatNumber(n.TotalCost))
	b.WriteString(" | est rows " + formatNumber(n.PlanRows))
	if loops := n.ActualLoops; loops != nil {
		if *loops == 0 {
			b.WriteString(" | never executed")
		} else {
			b.WriteString(" | actual rows " + formatNumber(n.ActualRows) + " x " + formatNumber(*loops) + " loops")
			b.WriteString(" | time " + formatNumber(n.ActualStartupTime) + ".." + formatNumber(n.ActualTotalTime) + " ms")
		}
	}
	switch {
	case n.WorkersPlanned > 0 && n.ActualLoops != nil:
		b.WriteString(" | workers " + strconv.Itoa(n.WorkersLaunched) + " launched of " + strconv.Itoa(n.WorkersPlanned))
	case n.WorkersPlanned > 0:
		b.WriteString(" | workers " + strconv.Itoa(n.WorkersPlanned) + " planned")
	}
	if n.SharedHitBlocks+n.SharedReadBlocks > 0 {
		b.WriteString(" | buffers hit " + strconv.FormatInt(n.SharedHitBlocks, 10) + " read " + strconv.FormatInt(n.SharedReadBlocks, 10))
	}
	if n.TempReadBlocks+n.TempWrittenBlocks > 0 {
		b.WriteString(" | temp read " + strconv.FormatInt(n.TempReadBlocks, 10) + " written " + strconv.FormatInt(n.TempWrittenBlocks, 10))
	}
	for _, c := range []struct {
		label, condition string
		removed          float64
	}{
		{"index cond", n.IndexCond, 0},
		{"recheck cond", n.RecheckCond, n.RowsRemovedByIndexRecheck},
		{"hash cond", n.HashCond, 0},
		{"merge cond", n.MergeCond, 0},
		{"join filter", n.JoinFilter, n.RowsRemovedByJoinFilter},
		{"filter", n.Filter, n.RowsRemovedByFilter},
	} {
		if c.condition != "" {
			b.WriteString(" | " + c.label + " " + c.condition)
			if c.removed > 0 {
				b.WriteString(" removed " + formatNumber(c.removed) + " rows")
			}
		}
	}
	if len(n.SortKey) > 0 {
		b.WriteString(" | sort key " + strings.Join(n.SortKey, ", "))
	}
	if n.SortMethod != "" {
		b.WriteString(" | sort " + n.SortMethod + ", " + n.SortSpaceType + " " + strconv.FormatInt(n.SortSpaceUsed, 10) + " kB")
	}
	if n.HashBatches > 1 {
		b.WriteString(" | hash batches " + strconv.FormatInt(n.HashBatches, 10))
	}
	lines = append(lines, b.String())
	for i := range n.Plans {
		lines = appendExplainNode(lines, &n.Plans[i], level+1)
	}
	return lines
}

// misestimated reports whether the planner got the rows of an executed node far wrong.
func (n *explainNode) misestimated() bool {
	if n.ActualLoops == nil || *n.ActualLoops == 0 {
		return false
	}
	low, high := min(n.PlanRows, n.ActualRows), max(n.PlanRows, n.ActualRows)
	return high >= estimateMissRows && high >= estimateMissRatio*max(low, 1)
}

// appendExplainWarnings returns the warnings of n and its children: big estimate misses,
// sequential scans reading many rows, and sorts or hashes spilling to disk.
func appendExplainWarnings(warnings []string, n *explainNode) []string {
	executed := n.ActualLoops != nil && *n.ActualLoops > 0
	// A miss is reported where it starts: the nodes above a misestimated child inherit it.
	if n.misestimated() && !slices.ContainsFunc(n.Plans, func(c explainNode) bool { return c.misestimated() }) {
		warnings = append(warnings, fmt.Sprintf("%s: estimated %s rows, got %s per loop; stale statistics (ANALYZE) "+
			"or correlated predicates mislead the planner, and the nodes above inherit the error",
			n.title(), formatNumber(n.PlanRows), formatNumber(n.ActualRows)))
	}
	if n.NodeType == "Seq Scan" {
		if executed {
			read := (n.ActualRows + n.RowsRemovedByFilter) * *n.ActualLoops
			if read >= largeScanRows {
				warning := fmt.Sprintf("%s read %s rows to return %s", n.title(), formatNumber(read), formatNumber(n.ActualRows**n.ActualLoops))
				if n.RowsRemovedByFilter > 0 {
					warning += ": an index matching the filter may help"
				}
				warnings = append(warnings, warning)
			}
		} else if n.ActualLoops == nil && n.PlanRows >= largeScanRows {
			warnings = append(warnings, fmt.Sprintf("%s is estimated to return %s rows", n.title(), formatNumber(n.PlanRows)))
		}
	}
	if n.SortSpaceType == "Disk" {
		warnings = append(warnings, fmt.Sprintf("%s spilled %d kB to disk: raise work_mem or sort fewer rows", n.title(), n.SortSpaceUsed))
	}
	if n.HashBatches > 1 {
		warnings = append(warnings, fmt.Sprintf("%s spilled to disk in %d batches: raise work_mem or hash fewer rows", n.title(), n.HashBatches))
	}
	for i := range n.Plans {
		warnings = appendExplainWarnings(warnings, &n.Plans[i])
	}
	return warnings
}
