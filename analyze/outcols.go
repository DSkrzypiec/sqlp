package analyze

import (
	"encoding/json"
	"fmt"
	"strings"

	"sqlp/ast"
	"sqlp/catalog"
	"sqlp/dialect"
)

// OutCols lists the distinct table and view columns that a query uses: the
// ones its output is computed from, and the ones its joins match on. CTEs,
// subqueries and aliases are seen through, so each column belongs to a
// database object. A column that the query only filters, groups or sorts on
// is left out.
type OutCols struct {
	// Columns come in the order the query first uses them: those of the
	// output first, then those used only in join conditions.
	Columns     []OutCol
	Diagnostics []Diagnostic
}

// OutCol is one column of a table or view, or with Star, all of them.
type OutCol struct {
	// Object is the table or view: its full name when the catalog has it,
	// otherwise as written, with the dialect's case folding. It is empty when
	// Status is Ambiguous.
	Object catalog.Name
	// Column is the column's name as the catalog stores it, or when the
	// catalog does not list it, as written with the dialect's case folding.
	// It is empty when Star is set.
	Column string
	// Star stands for every column of Object, when the catalog does not list
	// them, as for SELECT * without a catalog.
	Star bool
	// Status is Resolved when the catalog lists the column; NotFound when it
	// lacks the object or the column; Unchecked without a catalog or when it
	// lists the object without columns; Ambiguous when the column could
	// belong to more than one relation, which only a catalog can settle.
	Status NameStatus
	// Clauses says where the query uses the column, in the order of the
	// Clause constants.
	Clauses []Clause
}

// Clause is where a query uses a column.
type Clause string

const (
	// SelectClause is the output: the SELECT list, followed through the
	// CTEs, subqueries and views it reads.
	SelectClause Clause = "select"
	// JoinClause is a join condition: ON, USING or NATURAL, in any CTE or
	// subquery the query reads.
	JoinClause Clause = "join"
)

// clauses lists every Clause; a column's clauses are a bit set over it.
var clauses = []Clause{SelectClause, JoinClause}

const (
	inSelect = iota // the bit of SelectClause
	inJoin
)

// ListOutCols lists the columns that a SELECT or CREATE [MATERIALIZED] VIEW
// statement uses. The error is only for an unsupported statement or an
// invalid catalog; anything the analysis cannot see into is reported in
// Diagnostics.
func ListOutCols(stmt ast.Statement, d *dialect.Dialect, o Options) (*OutCols, error) {
	var q *ast.Query
	switch s := stmt.(type) {
	case *ast.Query:
		q = s
	case *ast.CreateView:
		q = s.Query
	default:
		return nil, fmt.Errorf("analyze: ListOutCols needs a SELECT or CREATE VIEW statement, not %T", stmt)
	}
	r, err := newResolver(d, o)
	if err != nil {
		return nil, err
	}
	b := newColBuilder(r, o)
	for _, c := range b.query(q, nil, nil, nil) {
		b.emit(b.allSrcs([]col{c}), inSelect)
	}
	return b.result(), nil
}

// result turns the sources the query used into OutCols.
func (b *colBuilder) result() *OutCols {
	out := &OutCols{Diagnostics: b.diags}
	listed := make([]bool, len(b.srcs))
	for _, ids := range b.order {
		for _, id := range ids {
			if listed[id] {
				continue
			}
			listed[id] = true
			s := b.srcs[id]
			c := OutCol{Column: s.column, Star: s.star, Status: s.status}
			if s.obj != nil {
				c.Object = s.obj.name
			}
			for i, cl := range clauses {
				if b.used[id]&(1<<i) != 0 {
					c.Clauses = append(c.Clauses, cl)
				}
			}
			out.Columns = append(out.Columns, c)
		}
	}
	return out
}

// Name is the column's full name: object.column, object.* for a star, and
// the column alone when there is no object.
func (c OutCol) Name() string {
	col := c.Column
	if c.Star {
		col = "*"
	}
	if obj := c.Object.String(); obj != "" {
		return obj + "." + col
	}
	return col
}

// String lists the columns one per line, as "name [status]: clause, ...".
func (c *OutCols) String() string {
	var sb strings.Builder
	for _, col := range c.Columns {
		sb.WriteString(col.Name())
		if col.Status != Resolved {
			sb.WriteString(" " + string(col.Status))
		}
		sb.WriteString(":")
		for i, cl := range col.Clauses {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(" " + string(cl))
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// MarshalJSON writes each column with its object's name parts next to it:
//
//	{"columns": [
//	  {"database": "shop", "schema": "public", "object": "orders", "column": "id", "clauses": ["select", "join"]},
//	  {"schema": "public", "object": "items", "star": true, "status": "unchecked", "clauses": ["select"]},
//	  {"column": "note", "status": "ambiguous", "clauses": ["select"]}]}
func (c *OutCols) MarshalJSON() ([]byte, error) {
	type jsonCol struct {
		Database string     `json:"database,omitempty"`
		Schema   string     `json:"schema,omitempty"`
		Object   string     `json:"object,omitempty"`
		Column   string     `json:"column,omitempty"`
		Star     bool       `json:"star,omitempty"`
		Status   NameStatus `json:"status,omitempty"`
		Clauses  []Clause   `json:"clauses"`
	}
	out := struct {
		Columns     []jsonCol    `json:"columns"`
		Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	}{Columns: []jsonCol{}, Diagnostics: c.Diagnostics}
	for _, col := range c.Columns {
		out.Columns = append(out.Columns, jsonCol{
			Database: col.Object.Database, Schema: col.Object.Schema, Object: col.Object.Object,
			Column: col.Column, Star: col.Star, Status: col.Status, Clauses: col.Clauses,
		})
	}
	return json.Marshal(out)
}
