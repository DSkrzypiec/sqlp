package analyze_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"sqlp/analyze"
	"sqlp/ast"
	"sqlp/catalog"
	"sqlp/dialect"
	"sqlp/internal/testkit"
	"sqlp/parser"
)

// tbl is a table with the given columns.
func tbl(schema, name string, cols ...string) catalog.Object {
	o := catalog.Object{Name: catalog.Name{Schema: schema, Object: name}, Kind: catalog.Table}
	for _, c := range cols {
		o.Columns = append(o.Columns, catalog.Column{Name: c})
	}
	return o
}

// view is a view with the given columns and definition.
func view(schema, name, definition string, cols ...string) catalog.Object {
	o := tbl(schema, name, cols...)
	o.Kind, o.Definition = catalog.View, definition
	return o
}

// abt is a small catalog that many cases share.
var abt = cat("", tbl("", "a", "x", "id"), tbl("", "b", "id", "y"), tbl("", "t", "a", "arr", "c"))

// Every query here is valid in its dialects, checked with pg_query and the
// sqlite3 3.51 binary (with the case's catalog created as tables), except
// where a case says the engines reject it.
var outColsCases = []qcase{
	{
		name: "example without catalog", dialects: both, sql: userExample,
		want: `
s1.temp.* unchecked: select
s1.t1.a unchecked: select, join
s1.t1.b unchecked: select
s1.t1.c unchecked: select, join
s1.t2.c unchecked: select, join
z ambiguous: select, join
s1.temp.id unchecked: join
s2.t2.k unchecked: join
s2.t3.k unchecked: join
s1.t1.d unchecked: join
s1.t2.d unchecked: join`,
		diags: []string{"column z.z could come from s2.t2 or s2.t3"},
	},
	{
		name: "example with catalog", dialects: pg, sql: userExample,
		opts: analyze.Options{Catalog: cat("shop",
			tbl("s1", "temp", "id", "name"), tbl("s1", "t1", "a", "b", "c", "d"), tbl("s1", "t2", "c", "d"),
			tbl("s2", "t2", "k", "x"), tbl("s2", "t3", "k", "z"))},
		want: `
shop.s1.temp.id: select, join
shop.s1.temp.name: select
shop.s1.t1.a: select, join
shop.s1.t1.b: select
shop.s1.t1.c: select, join
shop.s1.t2.c: select, join
shop.s2.t3.z: select, join
shop.s2.t2.k: join
shop.s2.t3.k: join
shop.s1.t1.d: join
shop.s1.t2.d: join`,
	},

	// Columns, stars and the catalog.
	{
		name: "columns and stars", dialects: both, sql: "SELECT a, t.b, t.* FROM t",
		want: `
t.a unchecked: select
t.b unchecked: select
t.* unchecked: select`,
	},
	{
		name: "star from the catalog", dialects: pg, sql: "SELECT * FROM t", opts: analyze.Options{Catalog: abt},
		want: `
public.t.a: select
public.t.arr: select
public.t.c: select`,
	},
	{
		name: "star from the catalog", dialects: lite, sql: "SELECT * FROM t", opts: analyze.Options{Catalog: abt},
		want: `
main.t.a: select
main.t.arr: select
main.t.c: select`,
	},
	{
		name: "each column once", dialects: both, sql: "SELECT a, a + 1, t.a, T.A FROM t",
		want: `
t.a unchecked: select`,
	},
	{
		name: "expressions give the columns they use", dialects: both,
		sql: "SELECT price * qty AS total, upper(name), 1, 'x', count(*) FROM t",
		want: `
t.price unchecked: select
t.qty unchecked: select
t.name unchecked: select`,
	},
	{
		name: "constants only", dialects: both, sql: "SELECT 1, 'x', NULL", want: "",
	},
	{
		name: "only output and join columns", dialects: both,
		sql: "SELECT a FROM t WHERE b > 1 GROUP BY a HAVING count(c) > 0 ORDER BY max(d)",
		want: `
t.a unchecked: select`,
	},
	{
		name: "quoted names keep their case", dialects: pg, sql: `SELECT "A", a, "a" FROM t`,
		opts: analyze.Options{Catalog: cat("", tbl("", "t", "A", "a"))},
		want: `
public.t.A: select
public.t.a: select`,
	},
	{
		name: "names match regardless of case", dialects: lite, sql: `SELECT A, "a", T.a FROM T`,
		opts: analyze.Options{Catalog: cat("", tbl("", "t", "a"))},
		want: `
main.t.a: select`,
	},
	{
		name: "objects the catalog lacks", dialects: pg, sql: "SELECT a, * FROM missing", opts: analyze.Options{Catalog: abt},
		want: `
missing.a not_found: select
missing.* not_found: select`,
	},
	{
		name: "an object listed without columns", dialects: pg, sql: "SELECT a, * FROM t",
		opts: analyze.Options{Catalog: cat("", obj("", "t", catalog.Table))},
		want: `
public.t.a unchecked: select
public.t.* unchecked: select`,
	},
	{
		// PostgreSQL rejects both columns.
		name: "columns the catalog lacks", dialects: pg, sql: "SELECT t.nosuch, nosuch2 FROM t", opts: analyze.Options{Catalog: abt},
		want: `
public.t.nosuch not_found: select`,
		diags: []string{"column nosuch2 is not a column of any relation in scope"},
	},
	{
		name: "system columns", dialects: pg, sql: "SELECT ctid, t.xmin FROM t", opts: analyze.Options{Catalog: abt},
		want: `
public.t.ctid: select
public.t.xmin: select`,
	},
	{
		name: "system columns", dialects: lite, sql: "SELECT rowid, t.oid, _ROWID_ FROM t", opts: analyze.Options{Catalog: abt},
		want: `
main.t.rowid: select
main.t.oid: select
main.t._ROWID_: select`,
	},
	{
		name: "search path", dialects: pg, sql: "SELECT id FROM orders",
		opts: analyze.Options{
			Catalog:    cat("shop", tbl("public", "orders", "id"), tbl("sales", "orders", "id")),
			SearchPath: []string{"sales", "public"},
		},
		want: `
shop.sales.orders.id: select`,
	},
	{
		name: "qualified by schema", dialects: pg, sql: "SELECT public.t.a, shop.public.t.c FROM t", opts: analyze.Options{Catalog: cat("shop", abt.Objects...)},
		want: `
shop.public.t.a: select
shop.public.t.c: select`,
	},
	{
		// The engines reject the second column.
		name: "a qualifier that names nothing", dialects: both, sql: "SELECT t.a, q.a FROM t",
		want: `
t.a unchecked: select`,
		diags: []string{"q names no relation in scope"},
	},

	// Columns that more than one relation could have.
	{
		name: "ambiguous without a catalog", dialects: both, sql: "SELECT x, a.id FROM a, b",
		want: `
x ambiguous: select
a.id unchecked: select`,
		diags: []string{"column x could come from a or b"},
	},
	{
		name: "the catalog settles it", dialects: pg, sql: "SELECT x, a.id FROM a, b", opts: analyze.Options{Catalog: abt},
		want: `
public.a.x: select
public.a.id: select`,
	},
	{
		name: "a listed column wins over unknown ones", dialects: pg, sql: "SELECT x FROM a, missing", opts: analyze.Options{Catalog: abt},
		want: `
public.a.x: select`,
	},
	{
		// The engines reject this.
		name: "both have it", dialects: pg, sql: "SELECT id FROM a, b", opts: analyze.Options{Catalog: abt},
		want: `
id ambiguous: select`,
		diags: []string{"column id could come from public.a or public.b"},
	},

	// Joins.
	{
		name: "join conditions", dialects: both,
		sql: "SELECT a.x FROM a JOIN b ON a.id = b.id LEFT JOIN c ON c.k = b.k AND c.flag",
		want: `
a.x unchecked: select
a.id unchecked: join
b.id unchecked: join
c.k unchecked: join
b.k unchecked: join
c.flag unchecked: join`,
	},
	{
		name: "a column used in both", dialects: both, sql: "SELECT a.id FROM a JOIN b ON a.id = b.id",
		want: `
a.id unchecked: select, join
b.id unchecked: join`,
	},
	{
		name: "USING", dialects: pg, sql: "SELECT * FROM a JOIN b USING (id)", opts: analyze.Options{Catalog: abt},
		want: `
public.a.id: select, join
public.b.id: select, join
public.a.x: select
public.b.y: select`,
	},
	{
		name: "USING", dialects: lite, sql: "SELECT * FROM a JOIN b USING (id)", opts: analyze.Options{Catalog: abt},
		want: `
main.a.x: select
main.a.id: select, join
main.b.id: select, join
main.b.y: select`,
	},
	{
		name: "USING without a catalog", dialects: both, sql: "SELECT id, x FROM a JOIN b USING (id)",
		want: `
a.id unchecked: select, join
b.id unchecked: select, join
x ambiguous: select`,
		diags: []string{"column x could come from a or b"},
	},
	{
		name: "NATURAL", dialects: pg, sql: "SELECT * FROM a NATURAL JOIN b", opts: analyze.Options{Catalog: abt},
		want: `
public.a.id: select, join
public.b.id: select, join
public.a.x: select
public.b.y: select`,
	},
	{
		name: "NATURAL without a catalog", dialects: both, sql: "SELECT 1 FROM a NATURAL JOIN b", want: "",
		diags: []string{"the columns a NATURAL JOIN matches on are not known, because the columns of a are not known"},
	},
	{
		// UsingColumnsFirst decides which column c1 is.
		name: "the place of a USING column", dialects: pg, opts: analyze.Options{Catalog: abt},
		sql: "WITH s(c1, c2, c3) AS (SELECT * FROM a JOIN b USING (id)) SELECT c1 FROM s",
		want: `
public.a.id: select, join
public.b.id: select, join`,
	},
	{
		name: "the place of a USING column", dialects: lite, opts: analyze.Options{Catalog: abt},
		sql: "WITH s(c1, c2, c3) AS (SELECT * FROM a JOIN b USING (id)) SELECT c1 FROM s",
		want: `
main.a.x: select
main.a.id: join
main.b.id: join`,
	},
	{
		name: "USING with an alias", dialects: pg, sql: "SELECT j.id FROM a JOIN b USING (id) AS j",
		want: `
a.id unchecked: select, join
b.id unchecked: select, join`,
	},
	{
		name: "a join under an alias", dialects: pg, sql: "SELECT j.x FROM (a JOIN b ON a.id = b.id) AS j", opts: analyze.Options{Catalog: abt},
		want: `
public.a.x: select
public.a.id: join
public.b.id: join`,
	},
	{
		name: "joins in the CTEs the query reads", dialects: both,
		sql: `WITH u AS (SELECT a.x FROM a JOIN b ON a.id = b.id),
unused AS (SELECT c.z FROM c JOIN d ON c.k = d.k)
SELECT x FROM u`,
		want: `
a.x unchecked: select
a.id unchecked: join
b.id unchecked: join`,
	},
	{
		name: "a subquery in a join condition", dialects: both, sql: "SELECT a.x FROM a JOIN b ON b.id IN (SELECT k FROM c)",
		want: `
a.x unchecked: select
b.id unchecked: join
c.k unchecked: join`,
	},
	{
		name: "a comma join with ON", dialects: lite, sql: "SELECT a.x FROM a, b ON a.id = b.id",
		want: `
a.x unchecked: select
a.id unchecked: join
b.id unchecked: join`,
	},

	// CTEs, subqueries and set operations.
	{
		name: "a CTE's column list", dialects: both, sql: "WITH c(p, q) AS (SELECT a, b + 1 FROM t) SELECT q FROM c",
		want: `
t.b unchecked: select`,
	},
	{
		name: "a subquery's column list", dialects: pg, sql: "SELECT q FROM (SELECT a, b FROM t) AS s(p, q)",
		want: `
t.b unchecked: select`,
	},
	{
		name: "a table's column list", dialects: pg, sql: "SELECT k2 FROM t AS z(k1, k2)", opts: analyze.Options{Catalog: abt},
		want: `
public.t.arr: select`,
	},
	{
		name: "a table's column list without a catalog", dialects: pg, sql: "SELECT k2 FROM t AS z(k1, k2)",
		want: `
t.* unchecked: select`,
		diags: []string{"the column names given to z are matched to its columns only up to column 0, because the columns of t are not known; " +
			"each later name counts all the remaining columns"},
	},
	{
		name: "names through a star", dialects: both, sql: "WITH c AS (SELECT * FROM t) SELECT a, c.b FROM c",
		want: `
t.a unchecked: select
t.b unchecked: select`,
	},
	{
		name: "UNION", dialects: both, sql: "WITH u AS (SELECT a, b FROM t1 UNION ALL SELECT c, d FROM t2) SELECT b FROM u",
		want: `
t1.b unchecked: select
t2.d unchecked: select`,
	},
	{
		name: "UNION of stars", dialects: both, sql: "SELECT x FROM (SELECT * FROM t1 UNION SELECT * FROM t2) AS s",
		want: `
t1.x unchecked: select
t2.x unchecked: select`,
	},
	{
		name: "UNION of a star and a list", dialects: both, sql: "SELECT * FROM (SELECT a, b FROM t1 EXCEPT SELECT * FROM t2) AS s",
		want: `
t1.a unchecked: select
t2.* unchecked: select
t1.b unchecked: select`,
		diags: []string{"the branches of EXCEPT are matched column by column only up to column 0, because the columns of t2 are not known; " +
			"each later column counts all the remaining columns of the other branches"},
	},
	{
		name: "VALUES", dialects: both, sql: "SELECT * FROM (VALUES (1, 'x')) AS v", want: "",
	},
	{
		name: "TABLE", dialects: pg, sql: "TABLE t",
		want: `
t.* unchecked: select`,
	},
	{
		name: "CREATE VIEW", dialects: both, sql: "CREATE VIEW w AS SELECT a FROM t",
		want: `
t.a unchecked: select`,
	},
	{
		name: "a recursive CTE", dialects: lite, opts: analyze.Options{Catalog: cat("", tbl("", "nodes", "id", "parent_id", "name"))},
		sql: `WITH RECURSIVE tree AS (
  SELECT id, parent_id, name FROM nodes WHERE parent_id IS NULL
  UNION ALL
  SELECT n.id, n.parent_id, n.name FROM nodes n JOIN tree t ON n.parent_id = t.id)
SELECT name FROM tree`,
		want: `
main.nodes.name: select
main.nodes.parent_id: join
main.nodes.id: join`,
	},
	{
		name: "a recursive CTE that swaps its columns", dialects: both,
		sql: "WITH RECURSIVE r(p, q) AS (SELECT x, 1 FROM a UNION ALL SELECT q, p FROM r WHERE p < 5) SELECT q FROM r",
		want: `
a.x unchecked: select`,
	},
	{
		name: "SEARCH and CYCLE", dialects: pg,
		sql: `WITH RECURSIVE r AS (SELECT id, parent FROM g UNION ALL SELECT g.id, g.parent FROM g JOIN r ON g.parent = r.id)
SEARCH DEPTH FIRST BY id SET ord CYCLE id SET looped USING path
SELECT ord, looped FROM r`,
		want: `
g.id unchecked: select, join
g.parent unchecked: join`,
	},
	{
		name: "a data-modifying CTE", dialects: pg, sql: "WITH d AS (DELETE FROM t RETURNING a) SELECT a FROM d", want: "",
		diags: []string{"data-modifying CTE d is not analysed"},
	},

	// Expressions.
	{
		name: "a scalar subquery", dialects: both, sql: "SELECT (SELECT max(b.y) FROM b WHERE b.id = a.id) FROM a",
		want: `
b.y unchecked: select`,
	},
	{
		name: "EXISTS adds nothing, IN does", dialects: both, sql: "SELECT EXISTS (SELECT y FROM b), x IN (SELECT y FROM b) FROM a",
		want: `
a.x unchecked: select
b.y unchecked: select`,
	},
	{
		name: "x IN table", dialects: lite, sql: "SELECT x IN b1 FROM a",
		opts: analyze.Options{Catalog: cat("", tbl("", "a", "x"), tbl("", "b1", "k"))},
		want: `
main.a.x: select
main.b1.k: select`,
	},
	{
		name: "LATERAL", dialects: pg, sql: "SELECT l.m FROM t, LATERAL (SELECT max(c) AS m FROM b WHERE b.id = t.a) AS l",
		want: `
b.c unchecked: select`,
	},
	{
		name: "LATERAL sees the left side of its join", dialects: pg, sql: "SELECT l.v FROM a JOIN LATERAL (SELECT a.x AS v) AS l ON true",
		want: `
a.x unchecked: select`,
	},
	{
		name: "a table function", dialects: pg, sql: "SELECT u, ordinality FROM t, unnest(t.arr) WITH ORDINALITY AS u",
		want: `
t.arr unchecked: select`,
	},
	{
		name: "a table-valued function", dialects: lite, sql: "SELECT j.value FROM t, json_each(t.doc) AS j",
		want: `
t.doc unchecked: select`,
	},
	{
		name: "whole rows", dialects: pg, sql: "SELECT row_to_json(t), count(*), to_json(t.*) FROM t", opts: analyze.Options{Catalog: abt},
		want: `
public.t.a: select
public.t.arr: select
public.t.c: select`,
	},
	{
		name: "a field of a composite column", dialects: pg, sql: "SELECT (t.c).f, (c).g FROM t",
		want: `
t.c unchecked: select`,
	},
	{
		name: "windows", dialects: both, sql: "SELECT sum(c) OVER w, rank() OVER (w ORDER BY d) FROM t WINDOW w AS (PARTITION BY a)",
		want: `
t.c unchecked: select
t.a unchecked: select
t.d unchecked: select`,
	},
	{
		// From SQLite's test suite; the engines reject a window that names
		// itself.
		name: "a window that names itself", dialects: lite,
		sql: "SELECT min(x) OVER w1 FROM t1 WINDOW w1 AS (PARTITION BY x_count(x) OVER w1)",
		want: `
t1.x unchecked: select`,
	},
	{
		name: "FILTER and ORDER BY in an aggregate", dialects: pg, sql: "SELECT string_agg(name, ',' ORDER BY id) FILTER (WHERE ok) FROM t",
		want: `
t.name unchecked: select
t.id unchecked: select
t.ok unchecked: select`,
	},
	{
		name: "arguments kept as text", dialects: pg, sql: "SELECT xmlelement(name x, t.a) FROM t", want: "",
		diags: []string{"the arguments of xmlelement are kept as text, so the columns in them are not listed"},
	},
	{
		// NamedExprs: PostgreSQL names these columns after what they compute.
		name: "the names PostgreSQL gives expressions", dialects: pg,
		sql: `SELECT s.max, s.c, s.case, s.int4, s.coalesce, s.btrim, s.exists, s.y
FROM (SELECT max(a), c::int, CASE WHEN d THEN e END, 1::int, coalesce(f, g), trim(h), EXISTS (SELECT 1), (SELECT y FROM b) FROM t) AS s`,
		want: `
t.a unchecked: select
t.c unchecked: select
t.d unchecked: select
t.e unchecked: select
t.f unchecked: select
t.g unchecked: select
t.h unchecked: select
b.y unchecked: select`,
	},
	{
		// SQLite names the column max(a), after its text, which analysis
		// does not try to match.
		name: "SQLite names expressions by their text", dialects: lite, sql: `SELECT s."max(a)", s.b FROM (SELECT max(a), b FROM t) AS s`,
		want: `
t.b unchecked: select`,
		diags: []string{"s has no column max(a)"},
	},

	// Views.
	{
		name: "a view", dialects: pg, sql: "SELECT p FROM v",
		opts: analyze.Options{Catalog: cat("", tbl("", "a", "x", "id"), tbl("", "b", "id", "y"),
			view("", "v", "SELECT a.x, b.y FROM a JOIN b USING (id)", "p", "q"))},
		want: `
public.v.p: select`,
	},
	{
		name: "a view expanded", dialects: pg, sql: "SELECT p FROM v",
		opts: analyze.Options{ExpandViews: true, Catalog: cat("", tbl("", "a", "x", "id"), tbl("", "b", "id", "y"),
			view("", "v", "SELECT a.x, b.y FROM a JOIN b USING (id)", "p", "q"))},
		want: `
public.a.x: select
public.a.id: join
public.b.id: join`,
	},
	{
		name: "a view expanded", dialects: lite, sql: "SELECT q FROM v",
		opts: analyze.Options{ExpandViews: true, Catalog: cat("", tbl("", "a", "x", "id"), tbl("", "b", "id", "y"),
			view("", "v", "CREATE VIEW v(p, q) AS SELECT a.x, b.y FROM a JOIN b USING (id)", "p", "q"))},
		want: `
main.b.y: select
main.a.id: join
main.b.id: join`,
	},
	{
		name: "views that cannot be expanded", dialects: pg, sql: "SELECT v1.p, v2.p, v3.p, v4.a FROM v1, v2, v3, v4",
		opts: analyze.Options{ExpandViews: true, Catalog: cat("", tbl("", "t", "a"),
			view("", "v1", "SELEC 1", "p"), view("", "v2", "DELETE FROM t", "p"), view("", "v3", "SELECT * FROM v3", "p"),
			view("", "v4", "SELECT a, a AS b FROM t", "a"))},
		want: `
public.v1.p: select
public.v2.p: select
public.v3.p: select
public.t.a: select`,
		diags: []string{
			`the definition of view public.v1 does not parse: syntax error at line 1, column 1: unexpected "SELEC" at start of statement`,
			"the definition of view public.v2 is not one query or CREATE VIEW statement",
			"view public.v3 depends on itself",
			"the definition of view public.v4 has 2 columns, but the catalog lists 1",
		},
	},
}

func TestListOutCols(t *testing.T) {
	for _, tc := range outColsCases {
		for _, name := range tc.dialects {
			if !testkit.Enabled(name) {
				continue
			}
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				d := byName[name]
				stmts, err := parser.Parse(tc.sql, d)
				if err != nil {
					t.Fatal(err)
				}
				c, err := analyze.ListOutCols(stmts[0], d, tc.opts)
				if err != nil {
					t.Fatal(err)
				}
				want := strings.TrimPrefix(tc.want, "\n")
				if want != "" {
					want += "\n"
				}
				if got := c.String(); got != want {
					t.Errorf("columns:\n%s\nwant:\n%s", got, want)
				}
				var diags []string
				for _, d := range c.Diagnostics {
					diags = append(diags, d.Message)
				}
				if !slices.Equal(diags, tc.diags) {
					t.Errorf("diagnostics:\n%s\nwant:\n%s", strings.Join(diags, "\n"), strings.Join(tc.diags, "\n"))
				}
			})
		}
	}
}

func TestListOutColsErrors(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		cat  *catalog.Catalog
		want string
	}{
		{"INSERT INTO t VALUES (1)", nil, "analyze: ListOutCols needs a SELECT or CREATE VIEW statement, not *ast.Insert"},
		{"SELECT 1", cat("", obj("", "t", "View")), `catalog: objects[0] (t): unknown kind "View"`},
	} {
		testkit.Run(t, "postgres", func(t *testing.T) {
			stmts, err := parser.Parse(tc.sql, dialect.Postgres)
			if err != nil {
				t.Fatal(err)
			}
			_, err = analyze.ListOutCols(stmts[0], dialect.Postgres, analyze.Options{Catalog: tc.cat})
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Errorf("%s: got error %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}

func TestListOutColsJSON(t *testing.T) {
	testkit.Require(t, "postgres")
	stmts, err := parser.Parse("SELECT t.a, u.*, x FROM t JOIN u ON u.id = t.a, w", dialect.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	c, err := analyze.ListOutCols(stmts[0], dialect.Postgres, analyze.Options{
		Catalog: cat("shop", tbl("public", "t", "a")),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	const want = `{
  "columns": [
    {
      "database": "shop",
      "schema": "public",
      "object": "t",
      "column": "a",
      "clauses": [
        "select",
        "join"
      ]
    },
    {
      "object": "u",
      "star": true,
      "status": "not_found",
      "clauses": [
        "select"
      ]
    },
    {
      "column": "x",
      "status": "ambiguous",
      "clauses": [
        "select"
      ]
    },
    {
      "object": "u",
      "column": "id",
      "status": "not_found",
      "clauses": [
        "join"
      ]
    }
  ],
  "diagnostics": [
    {
      "message": "column x could come from u or w"
    }
  ]
}`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if name := c.Columns[1].Name(); name != "u.*" {
		t.Errorf("Name() = %q, want u.*", name)
	}

	stmts, _ = parser.Parse("SELECT 1", dialect.Postgres)
	c, _ = analyze.ListOutCols(stmts[0], dialect.Postgres, analyze.Options{})
	if got, _ := json.Marshal(c); string(got) != `{"columns":[]}` {
		t.Errorf("no columns: got %s", got)
	}
}

// TestPerfListOutCols guards against work that grows with the square of the
// input, or worse. Each input is at most about 200 KB, the most the web UI
// accepts.
func TestPerfListOutCols(t *testing.T) {
	testkit.Require(t, "postgres")
	rep := func(n int, format string) string {
		var sb strings.Builder
		for i := range n {
			sb.WriteString(strings.NewReplacer("#", strconv.Itoa(i), "$", strconv.Itoa(i+1)).Replace(format))
		}
		return sb.String()
	}
	wide := cat("", tbl("", "t", strings.Fields(rep(1000, "c# "))...))
	for _, tc := range []struct {
		name, sql string
		opts      analyze.Options
		columns   int
	}{
		// Each CTE joins the one before it to itself, which doubles its
		// columns until maxCols stops it.
		{"stars that double", "WITH c0 AS (SELECT * FROM t)" + rep(4300, ",c$ AS (SELECT * FROM c# AS x, c# AS y)") +
			" SELECT * FROM c4300", analyze.Options{Catalog: wide}, 1000},
		{"stars that double, without a catalog", "WITH c0 AS (SELECT * FROM t)" + rep(4300, ",c$ AS (SELECT * FROM c# AS x, c# AS y)") +
			" SELECT * FROM c4300", analyze.Options{}, 1},
		// Every column could belong to any of the tables.
		{"many columns of many tables", "SELECT a" + rep(1990, ",a#") + " FROM t" + rep(9000, ",t#"), analyze.Options{}, 1991},
		{"many qualified columns", "SELECT t0.a" + rep(1990, ",t#.a") + " FROM t0" + rep(15000, ",t#"), analyze.Options{}, 1990},
		{"a wide UNION", "SELECT * FROM (SELECT * FROM t" + rep(9000, " UNION SELECT * FROM t") + ") AS s",
			analyze.Options{Catalog: wide}, 1000},
		{"a UNION of many tables", "SELECT a FROM (SELECT a, b FROM t0" + rep(7000, " UNION SELECT a, b FROM t#") + ") AS s",
			analyze.Options{}, 7000},
		{"a UNION of stars of many tables", "SELECT a FROM (SELECT * FROM t0" + rep(7500, " UNION SELECT * FROM t#") + ") AS s",
			analyze.Options{}, 7500},
		{"a deep chain of CTEs", "WITH c0 AS (SELECT a FROM t)" + rep(6000, ",c$ AS (SELECT a FROM c#)") + " SELECT a FROM c6000",
			analyze.Options{}, 1},
		// Each column of the recursive term is the next one of the CTE, so every
		// column turns out to come from all of t's.
		{"a recursive CTE with many rotating columns", "WITH RECURSIVE r(" + strings.TrimPrefix(rep(1000, ",c#"), ",") +
			") AS (SELECT " + strings.TrimPrefix(rep(1000, ",t.c#"), ",") + " FROM t UNION ALL SELECT " +
			strings.TrimPrefix(rep(999, ",c$"), ",") + ",c0 FROM r) SELECT c0 FROM r", analyze.Options{Catalog: wide}, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.sql) > 200<<10 {
				t.Fatalf("the input is %d bytes, over 200 KB", len(tc.sql))
			}
			stmts, err := parser.Parse(tc.sql, dialect.Postgres)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			c, err := analyze.ListOutCols(stmts[0], dialect.Postgres, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			took := time.Since(start)
			if got := len(c.Columns); got != tc.columns {
				t.Errorf("%d columns, want %d", got, tc.columns)
			}
			t.Logf("%d bytes of SQL, %d columns: %v", len(tc.sql), len(c.Columns), took)
			if took > time.Second {
				t.Errorf("took %v, want well under 1s", took)
			}
		})
	}
}

// TestListOutColsSeesEveryField fails when an AST node that ListOutCols
// reads by hand gains or loses a field. Check that ListOutCols handles the
// change, then update the list. Other expressions need no entry: they are
// walked by reflection.
func TestListOutColsSeesEveryField(t *testing.T) {
	testkit.Require(t, "postgres", "sqlite")
	for typ, want := range map[reflect.Type]string{
		reflect.TypeFor[ast.Query]():       "With Body OrderBy Limit Locking",
		reflect.TypeFor[ast.With]():        "Recursive CTEs",
		reflect.TypeFor[ast.CTE]():         "Name Columns Materialized Query DML Search Cycle",
		reflect.TypeFor[ast.CTESearch]():   "Breadth By Set",
		reflect.TypeFor[ast.CTECycle]():    "Columns Set To Default Using",
		reflect.TypeFor[ast.Select]():      "Distinct DistinctOn Items Into From Where GroupBy Having Windows Qualify As",
		reflect.TypeFor[ast.SelectItem]():  "Expr Alias",
		reflect.TypeFor[ast.SetOp]():       "Op All Left Right",
		reflect.TypeFor[ast.Values]():      "Rows",
		reflect.TypeFor[ast.ParenQuery]():  "Query",
		reflect.TypeFor[ast.TableBody]():   "Name",
		reflect.TypeFor[ast.TableRef]():    "Name Only Alias Indexed Sample",
		reflect.TypeFor[ast.Alias]():       "Name Columns",
		reflect.TypeFor[ast.SubqueryRef](): "Lateral Query Alias",
		reflect.TypeFor[ast.FuncRef]():     "Expr Lateral Func RowsFrom RowsFromColumns Ordinality Alias Offset OffsetAlias",
		reflect.TypeFor[ast.Join]():        "Kind Natural Left Right On Using Comma UsingAlias",
		reflect.TypeFor[ast.ParenTable]():  "Expr Alias",
		reflect.TypeFor[ast.Star]():        "Qualifier Except Replace",
		reflect.TypeFor[ast.In]():          "Expr Not List Subquery Table Func",
		reflect.TypeFor[ast.Collate]():     "Expr Collation",
		reflect.TypeFor[ast.FuncCall]():    "Name Args Distinct OrderBy WithinGroup Filter Over NoParens RawArgs Nulls Having Limit",
		reflect.TypeFor[ast.WindowSpec]():  "Name NameOnly PartitionBy OrderBy Frame",
		reflect.TypeFor[ast.NamedWindow](): "Name Spec",
		reflect.TypeFor[ast.Exists]():      "Query",
		reflect.TypeFor[ast.CreateView](): "OrReplace Temp Unlogged Materialized IfNotExists Name Columns Using With " +
			"Tablespace Query CheckOption WithData",
	} {
		var fields []string
		for f := range typ.Fields() {
			fields = append(fields, f.Name)
		}
		if got := strings.Join(fields, " "); got != want {
			t.Errorf("ast.%s has fields\n  %s\nListOutCols was written for\n  %s", typ.Name(), got, want)
		}
	}
}
