// Package catalog describes the database objects that queries can refer to.
// It is the optional schema context for analysing queries: without one, names
// are taken as written; with one, they can be matched to real objects and
// columns.
//
// A Catalog is plain data, so it can be built in code, read from JSON or
// loaded from a live database. Only names and column order are required. Kind,
// column types and view definitions are optional.
//
// Write names exactly as the database stores them. Dialect rules for matching
// names in a query, such as PostgreSQL folding unquoted names to lower case or
// SQLite ignoring case, are applied when a query is analysed, never here.
//
// # Loading from a database
//
// Both queries below return the same six columns, ordered by object, so one
// loop can load either:
//
//	var c catalog.Catalog
//	for rows.Next() {
//		var schema, object, kind, column, typ, definition string
//		if err := rows.Scan(&schema, &object, &kind, &column, &typ, &definition); err != nil {
//			return err
//		}
//		var cols []catalog.Column // none for a PostgreSQL table without columns
//		if column != "" {
//			cols = append(cols, catalog.Column{Name: column, Type: typ})
//		}
//		c.Add(catalog.Name{Schema: schema, Object: object}, catalog.Kind(kind), cols...).Definition = definition
//	}
//
// PostgreSQL. It reads pg_catalog because information_schema leaves out
// materialized views, and joins the columns with a left join so that a table
// without columns is kept. Set Catalog.Database to the result of
// SELECT current_database() so that analysis can give full names:
//
//	SELECT n.nspname, c.relname,
//	       CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized_view'
//	                      WHEN 'f' THEN 'foreign_table' ELSE 'table' END,
//	       coalesce(a.attname, ''), coalesce(format_type(a.atttypid, a.atttypmod), ''),
//	       CASE WHEN c.relkind IN ('v', 'm') THEN pg_get_viewdef(c.oid) ELSE '' END
//	FROM pg_class c
//	JOIN pg_namespace n ON n.oid = c.relnamespace
//	LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
//	WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
//	  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
//	ORDER BY 1, 2, a.attnum
//
// SQLite, for the main database. An untyped column has an empty type, and a
// view's definition is its whole CREATE VIEW statement. pragma_table_xinfo,
// unlike pragma_table_info, also lists generated columns; hidden = 1 marks the
// hidden columns of virtual tables, which SELECT * leaves out:
//
//	SELECT 'main', m.name, m.type, p.name, p.type,
//	       CASE m.type WHEN 'view' THEN m.sql ELSE '' END
//	FROM sqlite_schema AS m JOIN pragma_table_xinfo(m.name) AS p
//	WHERE m.type IN ('table', 'view') AND p.hidden <> 1
//	ORDER BY m.name, p.cid
package catalog

import (
	"errors"
	"fmt"
	"strings"
)

// Catalog lists the objects that queries can refer to.
type Catalog struct {
	// Database is the database of every object whose name gives none. Set it
	// to get full names, including the database, out of analysis.
	Database string   `json:"database,omitempty"`
	Objects  []Object `json:"objects"`
}

// Name is a qualified object name. An empty Schema means the default schema.
// Database is only for three-part names, and needs a Schema.
type Name struct {
	Database string `json:"database,omitempty"`
	Schema   string `json:"schema,omitempty"`
	Object   string `json:"name"`
}

// String joins the non-empty parts with dots, without quoting.
func (n Name) String() string {
	parts := make([]string, 0, 3)
	for _, p := range []string{n.Database, n.Schema, n.Object} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ".")
}

// Kind is what sort of object an Object is. The empty Kind means it was not
// specified.
type Kind string

// The kinds of object that a FROM clause can name.
const (
	Table            Kind = "table"
	View             Kind = "view"
	MaterializedView Kind = "materialized_view"
	ForeignTable     Kind = "foreign_table"
)

func (k Kind) known() bool {
	switch k {
	case "", Table, View, MaterializedView, ForeignTable:
		return true
	}
	return false
}

// Object is a table, view or other relation. In JSON its name parts sit next
// to its other fields:
//
//	{"schema": "public", "name": "orders", "kind": "table",
//	 "columns": [{"name": "id", "type": "integer"}, {"name": "note"}]}
type Object struct {
	Name
	Kind    Kind     `json:"kind,omitempty"`
	Columns []Column `json:"columns,omitempty"` // the full list, in table order
	// Definition is a view's query, or its whole CREATE VIEW statement, in the
	// dialect the catalog is used with. It is optional.
	Definition string `json:"definition,omitempty"`
}

// Column is one column of an Object.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"` // optional, as the database reports it
}

// Add appends columns to the object named n, first creating it with kind k
// if the catalog does not have it yet, and returns the object. It is meant for
// loading the rows of a query that returns one row per column. With rows
// ordered by object, a row of the object added last takes constant time, and
// the first row of a new object scans the objects added so far. The returned
// pointer is only valid until the next call to Add.
func (c *Catalog) Add(n Name, k Kind, cols ...Column) *Object {
	i := c.index(n)
	if i < 0 {
		c.Objects = append(c.Objects, Object{Name: n, Kind: k})
		i = len(c.Objects) - 1
	}
	c.Objects[i].Columns = append(c.Objects[i].Columns, cols...)
	return &c.Objects[i]
}

// index returns the position of the object named exactly n, or -1. It searches
// from the end, where Add's previous object is.
func (c *Catalog) index(n Name) int {
	for i := len(c.Objects) - 1; i >= 0; i-- {
		if c.Objects[i].Name == n {
			return i
		}
	}
	return -1
}

// Validate reports every problem that would make the catalog ambiguous or
// meaningless: an empty name, a Database without a Schema, an unknown Kind, a
// Definition on a table, and an object or column listed twice. Names are compared exactly, because the
// dialect's matching rules are only known when a query is analysed.
func (c *Catalog) Validate() error {
	var errs []error
	fail := func(i int, o *Object, format string, args ...any) {
		where := fmt.Sprintf("catalog: objects[%d]", i)
		if o.Object != "" {
			where += fmt.Sprintf(" (%s)", o.Name)
		}
		errs = append(errs, fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...)))
	}
	seen := make(map[Name]int, len(c.Objects))
	for i := range c.Objects {
		o := &c.Objects[i]
		if o.Object == "" {
			fail(i, o, "empty name")
		}
		if o.Database != "" && o.Schema == "" {
			fail(i, o, "database %q given without a schema", o.Database)
		}
		if !o.Kind.known() {
			fail(i, o, "unknown kind %q", o.Kind)
		}
		if o.Definition != "" && (o.Kind == Table || o.Kind == ForeignTable) {
			fail(i, o, "a %s cannot have a definition", o.Kind)
		}
		if first, ok := seen[o.Name]; ok {
			fail(i, o, "also listed as objects[%d]", first)
		} else if o.Object != "" {
			seen[o.Name] = i
		}
		cols := make(map[string]bool, len(o.Columns))
		for j, col := range o.Columns {
			switch {
			case col.Name == "":
				fail(i, o, "columns[%d]: empty name", j)
			case cols[col.Name]:
				fail(i, o, "duplicate column %q", col.Name)
			}
			cols[col.Name] = true
		}
	}
	return errors.Join(errs...)
}
