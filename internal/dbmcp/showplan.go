package dbmcp

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// StatementPlan summarizes the plan of one statement: what an assistant needs to analyze it,
// without the bulk of the showplan XML.
type StatementPlan struct {
	Statement     string  `json:"statement"`
	EstimatedCost float64 `json:"estimated_cost"`
	EstimatedRows float64 `json:"estimated_rows"`
	// Optimization is the optimization level and why the optimizer stopped, e.g. "FULL, GoodEnoughPlanFound".
	Optimization        string `json:"optimization,omitempty"`
	DegreeOfParallelism int    `json:"degree_of_parallelism,omitempty"`
	CompileTimeMs       int    `json:"compile_time_ms,omitempty"`
	// CPUTimeMs and ElapsedTimeMs are measured (actual plans only).
	CPUTimeMs     *int `json:"cpu_time_ms,omitempty"`
	ElapsedTimeMs *int `json:"elapsed_time_ms,omitempty"`
	// MemoryGrant is granted and, for actual plans, used memory.
	MemoryGrant string `json:"memory_grant,omitempty"`
	// Parameters lists the parameters with the values the plan was compiled for and ran with.
	Parameters     []string `json:"parameters,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
	MissingIndexes []string `json:"missing_indexes,omitempty"`
	Waits          []string `json:"waits,omitempty"`
	// Operators is the operator tree, one indented line per operator.
	Operators []string `json:"operators"`
}

// planNode is one operator (RelOp) of a showplan.
type planNode struct {
	physicalOp, logicalOp, object, predicate string
	seeks, warnings                          []string
	children                                 []*planNode
	estRows, estExecs, subtreeCost           float64
	actualRows, actualExecs, logicalReads    int64
	hasActual                                bool
	// depth is the number of open elements once the RelOp element is open.
	depth int
}

// showplanReader walks a showplan document element by element.
type showplanReader struct {
	statement  *StatementPlan
	elements   []string
	nodes      []*planNode
	roots      []*planNode
	statements []StatementPlan
	// Seek predicate being read: comparison, columns and values.
	seekOp      string
	seekColumns []string
	seekValues  []string
	// Missing index group being read.
	missing       strings.Builder
	missingImpact string
	// warnings receives the children of the Warnings element being read.
	warnings      *[]string
	warningsDepth int
}

// summarizeShowplan reads a showplan XML document.
func summarizeShowplan(document string) ([]StatementPlan, error) {
	r := &showplanReader{}
	decoder := xml.NewDecoder(strings.NewReader(document))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return r.statements, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			r.start(t.Name.Local, attributes(t.Attr))
			r.elements = append(r.elements, t.Name.Local)
		case xml.EndElement:
			r.elements = r.elements[:len(r.elements)-1]
			r.end(t.Name.Local)
		}
	}
}

func attributes(attrs []xml.Attr) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		m[a.Name.Local] = a.Value
	}
	return m
}

// parent returns the name of the element containing the one starting.
func (r *showplanReader) parent() string {
	if len(r.elements) == 0 {
		return ""
	}
	return r.elements[len(r.elements)-1]
}

// node returns the operator being read, if any.
func (r *showplanReader) node() *planNode {
	if len(r.nodes) == 0 {
		return nil
	}
	return r.nodes[len(r.nodes)-1]
}

func (r *showplanReader) start(name string, a map[string]string) {
	if r.warnings != nil && len(r.elements) == r.warningsDepth+1 {
		*r.warnings = append(*r.warnings, describeWarning(name, a))
	}
	if name == "StmtSimple" {
		r.statement = &StatementPlan{
			Statement:     strings.Join(strings.Fields(a["StatementText"]), " "),
			EstimatedCost: number(a["StatementSubTreeCost"]),
			EstimatedRows: number(a["StatementEstRows"]),
			Optimization:  strings.Trim(a["StatementOptmLevel"]+", "+a["StatementOptmEarlyAbortReason"], ", "),
		}
		r.roots = nil
		return
	}
	s, n := r.statement, r.node()
	if s == nil {
		return
	}
	switch name {
	case "QueryPlan":
		s.DegreeOfParallelism, s.CompileTimeMs = integer(a["DegreeOfParallelism"]), integer(a["CompileTime"])
	case "MemoryGrantInfo":
		if integer(a["GrantedMemory"]) == 0 {
			break
		}
		s.MemoryGrant = a["GrantedMemory"] + " KB granted"
		if used, ok := a["MaxUsedMemory"]; ok {
			s.MemoryGrant += ", " + used + " KB used"
		}
	case "QueryTimeStats":
		if n == nil {
			cpu, elapsed := integer(a["CpuTime"]), integer(a["ElapsedTime"])
			s.CPUTimeMs, s.ElapsedTimeMs = &cpu, &elapsed
		}
	case "Wait":
		if r.parent() == "WaitStats" {
			s.Waits = append(s.Waits, fmt.Sprintf("%s %s ms (%s waits)", a["WaitType"], a["WaitTimeMs"], a["WaitCount"]))
		}
	case "ColumnReference":
		r.columnReference(a)
	case "MissingIndexGroup":
		r.missing.Reset()
		r.missingImpact = a["Impact"]
	case "MissingIndex":
		fmt.Fprintf(&r.missing, "%s.%s", a["Schema"], a["Table"])
	case "ColumnGroup":
		fmt.Fprintf(&r.missing, " %s:", a["Usage"])
	case "Column":
		if r.parent() == "ColumnGroup" {
			fmt.Fprintf(&r.missing, " %s", a["Name"])
		}
	case "Warnings":
		r.warningsDepth = len(r.elements)
		if n != nil && len(r.elements) == n.depth {
			r.warnings = &n.warnings
		} else if n == nil {
			r.warnings = &s.Warnings
		}
	case "RelOp":
		r.startOperator(a)
	case "RunTimeCountersPerThread":
		if n != nil {
			n.hasActual = true
			n.actualRows += int64(integer(a["ActualRows"]))
			n.actualExecs += int64(integer(a["ActualExecutions"]))
			n.logicalReads += int64(integer(a["ActualLogicalReads"]))
		}
	case "Object":
		// The object of the operator itself, not of an operator nested in it.
		if n != nil && n.object == "" && len(r.elements) == n.depth+1 {
			n.object = strings.Trim(a["Schema"]+"."+a["Table"]+"."+a["Index"], ".")
		}
	case "Prefix", "StartRange", "EndRange":
		r.seekOp, r.seekColumns, r.seekValues = a["ScanType"], nil, nil
	case "ScalarOperator":
		r.scalarOperator(n, a["ScalarString"])
	}
}

func (r *showplanReader) columnReference(a map[string]string) {
	switch r.parent() {
	case "ParameterList":
		parameter := a["Column"] + " " + a["ParameterDataType"]
		if compiled, ok := a["ParameterCompiledValue"]; ok {
			parameter += ", compiled for " + compiled
		}
		if runtime, ok := a["ParameterRuntimeValue"]; ok {
			parameter += ", ran with " + runtime
		}
		r.statement.Parameters = append(r.statement.Parameters, parameter)
	case "RangeColumns":
		r.seekColumns = append(r.seekColumns, a["Column"])
	case "ColumnsWithNoStatistics":
		if r.warnings != nil && len(*r.warnings) > 0 {
			(*r.warnings)[len(*r.warnings)-1] += " " + strings.Trim(a["Table"]+"."+a["Column"], ".")
		}
	}
}

func (r *showplanReader) scalarOperator(n *planNode, scalar string) {
	switch {
	case n == nil:
	case r.parent() == "RangeExpressions":
		r.seekValues = append(r.seekValues, scalar)
	case (r.parent() == "Predicate" || r.parent() == "ProbeResidual" || r.parent() == "Residual") &&
		len(r.elements) == n.depth+2 && n.predicate == "":
		n.predicate = scalar
	}
}

func (r *showplanReader) startOperator(a map[string]string) {
	n := &planNode{
		physicalOp:  a["PhysicalOp"],
		logicalOp:   a["LogicalOp"],
		estRows:     number(a["EstimateRows"]),
		estExecs:    number(a["EstimateRebinds"]) + number(a["EstimateRewinds"]) + 1,
		subtreeCost: number(a["EstimatedTotalSubtreeCost"]),
		depth:       len(r.elements) + 1,
	}
	if parent := r.node(); parent != nil {
		parent.children = append(parent.children, n)
	} else {
		r.roots = append(r.roots, n)
	}
	r.nodes = append(r.nodes, n)
}

func (r *showplanReader) end(name string) {
	switch name {
	case "RelOp":
		r.nodes = r.nodes[:len(r.nodes)-1]
	case "Warnings":
		if len(r.elements) == r.warningsDepth {
			r.warnings = nil
		}
	case "Prefix", "StartRange", "EndRange":
		if n := r.node(); n != nil {
			n.seeks = append(n.seeks, seekPredicate(r.seekOp, r.seekColumns, r.seekValues))
		}
	case "MissingIndexGroup":
		if r.statement != nil {
			r.statement.MissingIndexes = append(r.statement.MissingIndexes,
				fmt.Sprintf("impact %s%%: %s", r.missingImpact, r.missing.String()))
		}
	case "StmtSimple":
		if r.statement != nil {
			r.statement.Operators = []string{}
			for _, root := range r.roots {
				r.statement.Operators = appendOperator(r.statement.Operators, root, r.statement.EstimatedCost, 0)
			}
			r.statements = append(r.statements, *r.statement)
			r.statement = nil
		}
	}
}

var seekComparisons = map[string]string{"EQ": "=", "GT": ">", "GE": ">=", "LT": "<", "LE": "<=", "IS": "IS", "ISNOT": "IS NOT"}

func seekPredicate(scanType string, columns, values []string) string {
	op := seekComparisons[scanType]
	if op == "" {
		op = scanType
	}
	parts := make([]string, 0, len(columns))
	for i, column := range columns {
		value := "?"
		if i < len(values) {
			value = values[i]
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", column, op, value))
	}
	return strings.Join(parts, " AND ")
}

// describeWarning renders a warning element and its attributes, e.g.
// "PlanAffectingConvert ConvertIssue=Seek Plan Expression=CONVERT_IMPLICIT(...)".
func describeWarning(name string, a map[string]string) string {
	if len(a) == 0 {
		return name
	}
	keys := make([]string, 0, len(a))
	for key := range a {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	parts := []string{name}
	for _, key := range keys {
		parts = append(parts, key+"="+a[key])
	}
	return strings.Join(parts, " ")
}

// appendOperator renders n and its children, indented by level.
func appendOperator(lines []string, n *planNode, statementCost float64, level int) []string {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", level))
	b.WriteString(n.physicalOp)
	if n.logicalOp != "" && n.logicalOp != n.physicalOp {
		fmt.Fprintf(&b, " (%s)", n.logicalOp)
	}
	if n.object != "" {
		fmt.Fprintf(&b, " %s", n.object)
	}
	own := n.subtreeCost
	for _, child := range n.children {
		own -= child.subtreeCost
	}
	if statementCost > 0 {
		fmt.Fprintf(&b, " | cost %.0f%%", max(own, 0)/statementCost*100)
	}
	fmt.Fprintf(&b, " | est rows %s", formatNumber(n.estRows))
	if n.estExecs > 1 {
		fmt.Fprintf(&b, " x %s execs", formatNumber(n.estExecs))
	}
	if n.hasActual {
		fmt.Fprintf(&b, " | actual rows %d in %d execs", n.actualRows, n.actualExecs)
		if n.logicalReads > 0 {
			fmt.Fprintf(&b, " | %d logical reads", n.logicalReads)
		}
	}
	if len(n.seeks) > 0 {
		fmt.Fprintf(&b, " | seek %s", strings.Join(n.seeks, "; "))
	}
	if n.predicate != "" {
		fmt.Fprintf(&b, " | predicate %s", n.predicate)
	}
	if len(n.warnings) > 0 {
		fmt.Fprintf(&b, " | WARNINGS %s", strings.Join(n.warnings, "; "))
	}
	lines = append(lines, b.String())
	for _, child := range n.children {
		lines = appendOperator(lines, child, statementCost, level+1)
	}
	return lines
}

func number(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64) //nolint:errcheck // absent attributes read as 0.
	return f
}

func integer(s string) int {
	i, _ := strconv.Atoi(s) //nolint:errcheck // absent attributes read as 0.
	return i
}

func formatNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
