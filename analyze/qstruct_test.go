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

var (
	pg     = []string{"postgres"}
	lite   = []string{"sqlite"}
	both   = []string{"postgres", "sqlite"}
	byName = map[string]*dialect.Dialect{"postgres": dialect.Postgres, "sqlite": dialect.SQLite}
)

func obj(schema, name string, kind catalog.Kind, definition ...string) catalog.Object {
	o := catalog.Object{Name: catalog.Name{Schema: schema, Object: name}, Kind: kind}
	if len(definition) > 0 {
		o.Definition = definition[0]
	}
	return o
}

func cat(database string, objects ...catalog.Object) *catalog.Catalog {
	return &catalog.Catalog{Database: database, Objects: objects}
}

const userExample = `WITH x AS (SELECT a, b FROM s1.t1 WHERE c = 1),
y AS (SELECT c, d FROM s1.t1 UNION ALL SELECT c, d FROM s1.t2)
SELECT a.*, x.a, x.b, y.c, z.z
FROM s1.temp a
LEFT JOIN x ON x.a = a.id
INNER JOIN y ON y.c = a.id
LEFT JOIN (SELECT * FROM s2.t2 INNER JOIN s2.t3 ON t2.k = t3.k) z ON z.z = y.d`

type qcase struct {
	name     string
	dialects []string
	sql      string
	opts     analyze.Options
	want     string   // QStructGraph.String()
	diags    []string // Diagnostic messages, in order
}

var qstructCases = []qcase{
	{
		name: "example without catalog", dialects: both, sql: userExample,
		want: `
query: query -> s1.temp, x, y, z
s1.temp: object unchecked
x: cte -> s1.t1
s1.t1: object unchecked
y: cte -> s1.t1, s1.t2
s1.t2: object unchecked
z: subquery -> s2.t2, s2.t3
s2.t2: object unchecked
s2.t3: object unchecked`,
	},
	{
		name: "example with catalog", dialects: pg, sql: userExample,
		opts: analyze.Options{Catalog: cat("shop",
			obj("s1", "t1", catalog.Table), obj("s1", "t2", catalog.Table), obj("s1", "temp", catalog.Table),
			obj("s2", "t2", catalog.View), obj("s2", "t3", catalog.MaterializedView))},
		want: `
query: query -> shop.s1.temp, x, y, z
shop.s1.temp: table
x: cte -> shop.s1.t1
shop.s1.t1: table
y: cte -> shop.s1.t1, shop.s1.t2
shop.s1.t2: table
z: subquery -> shop.s2.t2, shop.s2.t3
shop.s2.t2: view
shop.s2.t3: materialized_view`,
	},
	{
		name: "example with catalog", dialects: lite, sql: userExample,
		opts: analyze.Options{Catalog: cat("",
			obj("s1", "t1", catalog.Table), obj("s1", "t2", catalog.Table), obj("s1", "temp", catalog.Table),
			obj("s2", "t2", catalog.Table), obj("s2", "t3", ""))},
		want: `
query: query -> s1.temp, x, y, z
s1.temp: table
x: cte -> s1.t1
s1.t1: table
y: cte -> s1.t1, s1.t2
s1.t2: table
z: subquery -> s2.t2, s2.t3
s2.t2: table
s2.t3: object`,
	},

	// Finding objects in the catalog.
	{
		name: "default search path", dialects: pg, sql: "SELECT * FROM orders",
		opts: analyze.Options{Catalog: cat("shop", obj("sales", "orders", catalog.Table), obj("public", "orders", catalog.Table))},
		want: `
query: query -> shop.public.orders
shop.public.orders: table`,
	},
	{
		name: "own search path", dialects: pg, sql: "SELECT * FROM orders",
		opts: analyze.Options{
			Catalog:    cat("shop", obj("public", "orders", catalog.Table), obj("sales", "orders", catalog.Table)),
			SearchPath: []string{"sales", "public"},
		},
		want: `
query: query -> shop.sales.orders
shop.sales.orders: table`,
	},
	{
		name: "object without schema is in the default schema", dialects: pg,
		sql:  "SELECT * FROM items, public.items, shop.public.items",
		opts: analyze.Options{Catalog: cat("shop", obj("", "items", catalog.Table))},
		want: `
query: query -> shop.public.items
shop.public.items: table`,
	},
	{
		name: "object without schema is in the default schema", dialects: lite,
		sql:  "SELECT * FROM items, main.items, ITEMS",
		opts: analyze.Options{Catalog: cat("", obj("", "items", catalog.Table))},
		want: `
query: query -> main.items
main.items: table`,
	},
	{
		name: "temp before main", dialects: lite, sql: "SELECT * FROM t, main.t",
		opts: analyze.Options{Catalog: cat("", obj("main", "t", catalog.Table), obj("temp", "t", catalog.Table))},
		want: `
query: query -> temp.t, main.t
temp.t: table
main.t: table`,
	},
	{
		name: "pg_catalog first unless the search path lists it", dialects: pg, sql: "SELECT * FROM pg_class",
		opts: analyze.Options{Catalog: cat("", obj("public", "pg_class", catalog.Table), obj("pg_catalog", "pg_class", catalog.Table))},
		want: `
query: query -> pg_catalog.pg_class
pg_catalog.pg_class: table`,
	},
	{
		name: "pg_catalog listed later", dialects: pg, sql: "SELECT * FROM pg_class",
		opts: analyze.Options{
			Catalog:    cat("", obj("public", "pg_class", catalog.Table), obj("pg_catalog", "pg_class", catalog.Table)),
			SearchPath: []string{"public", "pg_catalog"},
		},
		want: `
query: query -> public.pg_class
public.pg_class: table`,
	},
	{
		name: "unquoted names fold to lower case", dialects: pg, sql: `SELECT * FROM Orders, "Orders", ORDERS`,
		opts: analyze.Options{Catalog: cat("", obj("public", "orders", catalog.Table), obj("public", "Orders", catalog.Table))},
		want: `
query: query -> public.orders, public.Orders
public.orders: table
public.Orders: table`,
	},
	{
		name: "names ignore case", dialects: lite, sql: `SELECT * FROM orders, "ORDERS", Main.Orders`,
		opts: analyze.Options{Catalog: cat("", obj("main", "Orders", catalog.Table))},
		want: `
query: query -> main.Orders
main.Orders: table`,
	},
	{
		name: "same name in another case without catalog", dialects: lite, sql: `SELECT * FROM Orders, ORDERS`,
		want: `
query: query -> Orders
Orders: object unchecked`,
	},
	{
		name: "not in the catalog", dialects: pg, sql: "SELECT * FROM t, u, nope.t, other.public.t, shop.public.t",
		opts: analyze.Options{Catalog: cat("shop", obj("public", "t", catalog.Table))},
		want: `
query: query -> shop.public.t, u, nope.t, other.public.t
shop.public.t: table
u: object not_found
nope.t: object not_found
other.public.t: object not_found`,
	},
	{
		name: "database from the query when the catalog has none", dialects: pg, sql: "SELECT * FROM shop.public.t",
		opts: analyze.Options{Catalog: cat("", obj("public", "t", catalog.Table))},
		want: `
query: query -> shop.public.t
shop.public.t: table`,
	},
	{
		name: "unicode identifier", dialects: pg, sql: `SELECT * FROM U&"d\0061t\+000061", U&"d!0061ta" UESCAPE '!'`,
		want: `
query: query -> data
data: object unchecked`,
	},
	{
		// PostgreSQL's parser takes four parts only for a function.
		name: "too many dotted parts", dialects: pg, sql: "SELECT * FROM a.b.c.d()",
		want: `
query: query -> a.b.c.d
a.b.c.d: function unchecked`,
		diags: []string{"a.b.c.d has too many dotted parts for an object name"},
	},

	// CTE scoping.
	{
		name: "a CTE reading the table it shadows", dialects: pg, sql: "WITH t AS (SELECT * FROM t) SELECT * FROM t",
		want: `
query: query -> t
t: cte -> t#2
t#2: object unchecked`,
	},
	{
		name: "a CTE reading itself", dialects: lite, sql: "WITH t AS (SELECT * FROM t) SELECT * FROM t",
		want: `
query: query -> t
t: cte -> t`,
	},
	{
		name: "forward reference", dialects: pg, sql: "WITH a AS (SELECT * FROM b), b AS (SELECT 1) SELECT * FROM a",
		want: `
query: query -> a
a: cte -> b
b: object unchecked`,
		diags: []string{"CTE b is never used"},
	},
	{
		name: "forward reference", dialects: lite, sql: "WITH a AS (SELECT * FROM b), b AS (SELECT 1) SELECT * FROM a",
		want: `
query: query -> a
a: cte -> b
b: cte`,
	},
	{
		name: "forward reference with RECURSIVE", dialects: pg,
		sql: "WITH RECURSIVE a AS (SELECT * FROM b), b AS (SELECT 1) SELECT * FROM a",
		want: `
query: query -> a
a: cte -> b
b: cte`,
	},
	{
		name: "recursive CTE", dialects: both,
		sql: "WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r WHERE n < 3) SELECT * FROM r",
		want: `
query: query -> r
r: cte -> r`,
	},
	{
		name: "nested WITH shadows the outer one", dialects: both,
		sql: "WITH a AS (SELECT * FROM t1) SELECT * FROM (WITH a AS (SELECT * FROM t2) SELECT * FROM a) s, a",
		want: `
query: query -> s, a#2
s: subquery -> a
a: cte -> t2
t2: object unchecked
a#2: cte -> t1
t1: object unchecked`,
	},
	{
		name: "the first of two CTEs with one name is the one in scope", dialects: both,
		sql: "WITH a AS (SELECT * FROM t1), a AS (SELECT * FROM t2) SELECT * FROM a",
		want: `
query: query -> a
a: cte -> t1
t1: object unchecked`,
		diags: []string{"CTE a is never used"},
	},
	{
		name: "repeated names get distinct IDs", dialects: both,
		sql: `SELECT * FROM (SELECT 1) x, (SELECT 2) x, (SELECT 3) "x#2", (SELECT 4) x, (SELECT 5) x`,
		want: `
query: query -> x, x#2, x#2#2, x#3, x#4
x: subquery
x#2: subquery
x#2#2: subquery
x#3: subquery
x#4: subquery`,
	},
	{
		name: "CTE used from subqueries", dialects: both,
		sql: "WITH a AS (SELECT * FROM t) SELECT (SELECT count(*) FROM a), b.* FROM (SELECT * FROM a) b",
		want: `
query: query -> subquery 1, b
subquery 1: subquery -> a
a: cte -> t
t: object unchecked
b: subquery -> a`,
	},
	{
		name: "a qualified name is never a CTE", dialects: both, sql: "WITH t AS (SELECT 1) SELECT * FROM t, s.t",
		want: `
query: query -> t, s.t
t: cte
s.t: object unchecked`,
	},
	{
		name: "unquoted CTE names fold", dialects: pg, sql: `WITH X AS (SELECT 1), "Y" AS (SELECT 2) SELECT * FROM x, y`,
		want: `
query: query -> x, y
x: cte
y: object unchecked`,
		diags: []string{"CTE Y is never used"},
	},
	{
		name: "CTE names ignore case", dialects: lite, sql: `WITH X AS (SELECT 1), "Y" AS (SELECT 2) SELECT * FROM x, y`,
		want: `
query: query -> X, Y
X: cte
Y: cte`,
	},
	{
		name: "unused CTE", dialects: both, sql: "WITH u AS (SELECT * FROM t) SELECT 1",
		want: `
query: query`,
		diags: []string{"CTE u is never used"},
	},
	{
		name: "data-modifying CTE", dialects: pg, sql: "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d",
		want: `
query: query -> d
d: cte`,
		diags: []string{"data-modifying CTE d is not analysed"},
	},

	// Where dependencies can appear.
	{
		name: "subqueries in expressions", dialects: both,
		sql: `SELECT (SELECT max(x) FROM u) AS m FROM t
WHERE id IN (SELECT id FROM banned) AND EXISTS (SELECT 1 FROM v WHERE v.id = t.id)`,
		want: `
query: query -> subquery 1, t, subquery 2, subquery 3
subquery 1: subquery -> u
u: object unchecked
t: object unchecked
subquery 2: subquery -> banned
banned: object unchecked
subquery 3: subquery -> v
v: object unchecked`,
	},
	{
		name: "subqueries in other clauses", dialects: both,
		sql: `SELECT a FROM t JOIN j ON j.id IN (SELECT id FROM o) GROUP BY a
HAVING count(*) > (SELECT 1 FROM h) ORDER BY (SELECT 1 FROM ob) LIMIT (SELECT 1 FROM l)`,
		want: `
query: query -> t, j, subquery 1, subquery 2, subquery 3, subquery 4
t: object unchecked
j: object unchecked
subquery 1: subquery -> o
o: object unchecked
subquery 2: subquery -> h
h: object unchecked
subquery 3: subquery -> ob
ob: object unchecked
subquery 4: subquery -> l
l: object unchecked`,
	},
	{
		name: "joins, parentheses and LATERAL", dialects: pg,
		sql: "SELECT * FROM a JOIN (b JOIN c ON true) ON true, LATERAL (SELECT * FROM d) x",
		want: `
query: query -> a, b, c, x
a: object unchecked
b: object unchecked
c: object unchecked
x: subquery -> d
d: object unchecked`,
	},
	{
		name: "set operations", dialects: pg,
		sql: "SELECT a FROM t1 UNION SELECT a FROM t2 INTERSECT (SELECT a FROM t3 ORDER BY 1) EXCEPT SELECT a FROM t1",
		want: `
query: query -> t1, t2, t3
t1: object unchecked
t2: object unchecked
t3: object unchecked`,
	},
	{
		name: "VALUES", dialects: both, sql: "SELECT * FROM (VALUES (1), ((SELECT 2 FROM t))) AS v",
		want: `
query: query -> v
v: subquery -> subquery 1
subquery 1: subquery -> t
t: object unchecked`,
	},
	{
		name: "TABLE", dialects: pg, sql: "TABLE t",
		want: `
query: query -> t
t: object unchecked`,
	},
	{
		name: "self join", dialects: both, sql: "SELECT * FROM t a JOIN t b ON a.id = b.parent",
		want: `
query: query -> t
t: object unchecked`,
	},
	{
		name: "table functions", dialects: pg,
		sql: `SELECT * FROM generate_series(1, (SELECT max(n) FROM u)) g,
ROWS FROM (unnest(ARRAY[1]), pg_catalog.generate_series(1, 2)), Generate_Series(1, 2)`,
		want: `
query: query -> generate_series, subquery 1, unnest, pg_catalog.generate_series
generate_series: function unchecked
subquery 1: subquery -> u
u: object unchecked
unnest: function unchecked
pg_catalog.generate_series: function unchecked`,
	},
	{
		name: "IN a table or table function", dialects: lite,
		sql: "WITH c AS (SELECT 1) SELECT 1 IN t, 2 IN json_each('[2]'), 3 IN c FROM u",
		want: `
query: query -> t, json_each, c, u
t: object unchecked
json_each: function unchecked
c: cte
u: object unchecked`,
	},
	{
		name: "raw function arguments", dialects: pg, sql: "SELECT XMLELEMENT(NAME x, (SELECT 1 FROM t)), XMLELEMENT(NAME y)",
		want: `
query: query`,
		diags: []string{"the arguments of xmlelement are kept as text, so a subquery in them is not analysed"},
	},
	{
		name: "SELECT INTO", dialects: pg, sql: "SELECT * INTO new_t FROM t",
		want: `
query: query -> t
t: object unchecked`,
		diags: []string{"SELECT INTO new_t is left out: QStruct shows what a query reads"},
	},

	// CREATE VIEW.
	{
		name: "view without catalog", dialects: pg, sql: "CREATE VIEW s.v AS SELECT * FROM t",
		want: `
s.v: view -> t
t: object unchecked`,
	},
	{
		name: "view with catalog", dialects: pg, sql: "CREATE OR REPLACE VIEW v AS SELECT * FROM t",
		opts: analyze.Options{Catalog: cat("shop", obj("public", "t", catalog.Table))},
		want: `
shop.public.v: view -> shop.public.t
shop.public.t: table`,
	},
	{
		name: "temporary view", dialects: pg, sql: "CREATE TEMP VIEW v AS SELECT 1",
		opts: analyze.Options{Catalog: cat("shop")},
		want: `
shop.pg_temp.v: view`,
	},
	{
		name: "temporary view", dialects: lite, sql: "CREATE TEMP VIEW v AS SELECT * FROM t",
		opts: analyze.Options{Catalog: cat("", obj("", "t", catalog.Table))},
		want: `
temp.v: view -> main.t
main.t: table`,
	},
	{
		name: "materialized view", dialects: pg, sql: "CREATE MATERIALIZED VIEW m AS SELECT * FROM t",
		want: `
m: materialized_view -> t
t: object unchecked`,
	},
	{
		name: "a view replacing itself", dialects: pg, sql: "CREATE OR REPLACE VIEW v AS SELECT * FROM v",
		opts: analyze.Options{Catalog: cat("", obj("public", "v", catalog.View))},
		want: `
public.v: view -> public.v#2
public.v#2: view`,
	},

	// Views in the catalog.
	{
		name: "views not expanded", dialects: pg, sql: "SELECT * FROM w",
		opts: analyze.Options{Catalog: cat("", obj("public", "w", catalog.View, "SELECT * FROM t"))},
		want: `
query: query -> public.w
public.w: view`,
	},
	{
		name: "views expanded", dialects: pg, sql: "WITH t AS (SELECT 1) SELECT * FROM w, t",
		opts: analyze.Options{ExpandViews: true, Catalog: cat("",
			obj("public", "t", catalog.Table), obj("public", "u", catalog.Table),
			obj("public", "v", catalog.View, "SELECT * FROM t JOIN u ON true"),
			// pg_get_viewdef's style: leading space, trailing semicolon.
			obj("public", "w", catalog.MaterializedView, " SELECT v.a\n   FROM v;"))},
		want: `
query: query -> public.w, t
public.w: materialized_view -> public.v
public.v: view -> public.t, public.u
public.t: table
public.u: table
t: cte`,
	},
	{
		name: "views expanded", dialects: lite, sql: "SELECT * FROM v",
		opts: analyze.Options{ExpandViews: true, Catalog: cat("",
			obj("main", "t", catalog.Table),
			obj("main", "v", catalog.View, "CREATE VIEW v AS SELECT * FROM t"))},
		want: `
query: query -> main.v
main.v: view -> main.t
main.t: table`,
	},
	{
		name: "a view used twice is expanded once", dialects: pg, sql: "SELECT * FROM v a, v b, (SELECT * FROM v) c",
		opts: analyze.Options{ExpandViews: true, Catalog: cat("",
			obj("public", "t", catalog.Table), obj("public", "v", catalog.View, "SELECT * FROM t"))},
		want: `
query: query -> public.v, c
public.v: view -> public.t
public.t: table
c: subquery -> public.v`,
	},
	{
		name: "views depending on each other", dialects: pg, sql: "SELECT * FROM v1",
		opts: analyze.Options{ExpandViews: true, Catalog: cat("",
			obj("public", "v1", catalog.View, "SELECT * FROM v2"), obj("public", "v2", catalog.View, "SELECT * FROM v1"))},
		want: `
query: query -> public.v1
public.v1: view -> public.v2
public.v2: view`,
		diags: []string{"view public.v1 depends on itself"},
	},
	{
		name: "bad view definitions", dialects: pg, sql: "SELECT * FROM v1, v2",
		opts: analyze.Options{ExpandViews: true, Catalog: cat("", obj("", "v1", "", "SELEC 1"), obj("", "v2", "", "DELETE FROM t"))},
		want: `
query: query -> public.v1, public.v2
public.v1: object
public.v2: object`,
		diags: []string{
			`the definition of view public.v1 does not parse: syntax error at line 1, column 1: unexpected "SELEC" at start of statement`,
			"the definition of view public.v2 is not one query or CREATE VIEW statement",
		},
	},
}

func TestQStruct(t *testing.T) {
	for _, tc := range qstructCases {
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
				g, err := analyze.QStruct(stmts[0], d, tc.opts)
				if err != nil {
					t.Fatal(err)
				}
				want := strings.TrimPrefix(tc.want, "\n") + "\n"
				if got := g.String(); got != want {
					t.Errorf("graph:\n%s\nwant:\n%s", got, want)
				}
				var diags []string
				for _, d := range g.Diagnostics {
					diags = append(diags, d.Message)
				}
				if !slices.Equal(diags, tc.diags) {
					t.Errorf("diagnostics:\n%s\nwant:\n%s", strings.Join(diags, "\n"), strings.Join(tc.diags, "\n"))
				}
			})
		}
	}
}

func TestQStructErrors(t *testing.T) {
	for _, tc := range []struct {
		dialect string
		sql     string
		cat     *catalog.Catalog
		want    string
	}{
		{"postgres", "INSERT INTO t VALUES (1)", nil,
			"analyze: QStruct needs a SELECT or CREATE VIEW statement, not *ast.Insert"},
		{"postgres", "SELECT 1", cat("", obj("", "t", "View")),
			`catalog: objects[0] (t): unknown kind "View"`},
		{"postgres", "SELECT 1", cat("", obj("", "t", ""), obj("public", "t", "")),
			"catalog: objects[0] (t) and objects[1] (public.t) are the same object in postgres"},
		{"postgres", "SELECT 1", cat("", obj("", "t", ""), obj("", "T", "")), ""},
		{"sqlite", "SELECT 1", cat("", obj("", "T", ""), obj("main", "t", "")),
			"catalog: objects[0] (T) and objects[1] (main.t) are the same object in sqlite"},
	} {
		testkit.Run(t, tc.dialect, func(t *testing.T) {
			d := byName[tc.dialect]
			stmts, err := parser.Parse(tc.sql, d)
			if err != nil {
				t.Fatal(err)
			}
			_, err = analyze.QStruct(stmts[0], d, analyze.Options{Catalog: tc.cat})
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

func TestQStructJSON(t *testing.T) {
	testkit.Require(t, "postgres")
	stmts, err := parser.Parse(`WITH x AS (SELECT * FROM t), unused AS (SELECT 1)
SELECT * FROM x, generate_series(1, 2), missing`, dialect.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	g, err := analyze.QStruct(stmts[0], dialect.Postgres, analyze.Options{
		Catalog: cat("shop", obj("public", "t", catalog.Table)),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	const want = `{
  "root": "query",
  "nodes": [
    {
      "id": "query",
      "kind": "query",
      "children": [
        "x",
        "generate_series",
        "missing"
      ]
    },
    {
      "id": "x",
      "kind": "cte",
      "name": "x",
      "children": [
        "shop.public.t"
      ]
    },
    {
      "id": "shop.public.t",
      "kind": "table",
      "database": "shop",
      "schema": "public",
      "name": "t"
    },
    {
      "id": "generate_series",
      "kind": "function",
      "name": "generate_series",
      "status": "unchecked"
    },
    {
      "id": "missing",
      "kind": "object",
      "name": "missing",
      "status": "not_found"
    }
  ],
  "diagnostics": [
    {
      "message": "CTE unused is never used"
    }
  ]
}`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	var names []string
	for _, n := range g.Objects() {
		names = append(names, n.ID)
	}
	if want := []string{"shop.public.t", "generate_series", "missing"}; !slices.Equal(names, want) {
		t.Errorf("Objects() = %v, want %v", names, want)
	}
}

// TestPerfQStruct guards against work that grows with the square of the
// input. Each input is about 200 KB, the most the web UI accepts, and every
// one of the first three took seconds before node IDs, edges and CTE lookups
// were made constant-time.
func TestPerfQStruct(t *testing.T) {
	testkit.Require(t, "postgres")
	// rep joins n copies of format, with # replaced by the copy's number and
	// $ by the number after it.
	rep := func(n int, format string) string {
		var sb strings.Builder
		for i := range n {
			sb.WriteString(strings.NewReplacer("#", strconv.Itoa(i), "$", strconv.Itoa(i+1)).Replace(format))
		}
		return sb.String()
	}
	nest := func(n int, inner string) string {
		return strings.Repeat("WITH c AS (", n) + inner + strings.Repeat(") SELECT 1 FROM c", n)
	}
	for _, tc := range []struct {
		name, sql string
		nodes     int
	}{
		{"one alias on many subqueries", "SELECT 1 FROM (SELECT 1) x" + rep(15000, ",(SELECT 1) x"), 15002},
		{"one CTE read by many subqueries", "WITH c AS (SELECT 1) SELECT 1" + rep(10000, ",(SELECT 1 FROM c)"), 10002},
		{"many tables in one FROM", "SELECT 1 FROM t" + rep(30000, ",t#"), 30002},
		{"many CTEs, many references", "WITH c AS (SELECT 1)" + rep(5000, ",c# AS (SELECT 1)") +
			" SELECT 1 FROM c" + rep(15000, ",t#"), 15002},
		{"a chain of CTEs", "WITH c0 AS (SELECT 1)" + rep(6000, ",c$ AS (SELECT 1 FROM c#)") + " SELECT 1 FROM c6000", 6002},
		// Lookups from the innermost query pass every enclosing WITH clause.
		{"tables under nested WITH clauses", nest(900, "SELECT 1 FROM t"+rep(25000, ",t#")), 25902},
		{"one outer CTE under nested WITH clauses", "WITH z AS (SELECT 1), c AS (" +
			nest(900, "SELECT 1 FROM z"+rep(80000, ",z")) + ") SELECT 1 FROM c", 903},
		{"many outer CTEs under nested WITH clauses", "WITH z AS (SELECT 1)" + rep(6000, ",z# AS (SELECT 1)") + ", c AS (" +
			nest(900, "SELECT 1 FROM z"+rep(6000, ",z#")) + ") SELECT 1 FROM c", 6903},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := parser.Parse(tc.sql, dialect.Postgres)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			g, err := analyze.QStruct(stmts[0], dialect.Postgres, analyze.Options{})
			if err != nil {
				t.Fatal(err)
			}
			took := time.Since(start)
			if got := len(g.Nodes()); got != tc.nodes {
				t.Errorf("%d nodes, want %d", got, tc.nodes)
			}
			t.Logf("%d bytes of SQL, %d nodes: %v", len(tc.sql), tc.nodes, took)
			if took > time.Second {
				t.Errorf("took %v, want well under 1s", took)
			}
		})
	}
}

// TestQStructSeesEveryField fails when an AST node that QStruct walks by hand
// gains or loses a field. Check that QStruct handles the change, then update
// the list. Expressions need no entry: they are walked by reflection.
func TestQStructSeesEveryField(t *testing.T) {
	testkit.Require(t, "postgres", "sqlite")
	for typ, want := range map[reflect.Type]string{
		reflect.TypeFor[ast.Query]():       "With Body OrderBy Limit Locking Pipes",
		reflect.TypeFor[ast.With]():        "Recursive CTEs",
		reflect.TypeFor[ast.CTE]():         "Name Columns Materialized Query DML Search Cycle",
		reflect.TypeFor[ast.Select]():      "Distinct DistinctOn Items Into From Where GroupBy Having Windows Qualify As",
		reflect.TypeFor[ast.SetOp]():       "Op All Left Right Match",
		reflect.TypeFor[ast.Values]():      "Rows",
		reflect.TypeFor[ast.ParenQuery]():  "Query",
		reflect.TypeFor[ast.TableBody]():   "Name",
		reflect.TypeFor[ast.TableRef]():    "Name Only Alias Indexed Sample SystemTime",
		reflect.TypeFor[ast.SubqueryRef](): "Lateral Query Alias",
		reflect.TypeFor[ast.FuncRef]():     "Expr Lateral Func RowsFrom RowsFromColumns Ordinality Alias Offset OffsetAlias",
		reflect.TypeFor[ast.Join]():        "Kind Natural Left Right On Using Comma UsingAlias",
		reflect.TypeFor[ast.ParenTable]():  "Expr Alias",
		reflect.TypeFor[ast.In]():          "Expr Not List Subquery Table Func",
		reflect.TypeFor[ast.CreateView](): "OrReplace Temp Unlogged Materialized IfNotExists Name Columns Using With " +
			"Tablespace Query CheckOption WithData",
	} {
		var fields []string
		for f := range typ.Fields() {
			fields = append(fields, f.Name)
		}
		if got := strings.Join(fields, " "); got != want {
			t.Errorf("ast.%s has fields\n  %s\nQStruct was written for\n  %s", typ.Name(), got, want)
		}
	}
}
