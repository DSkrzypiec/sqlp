package sqlp

import (
	"reflect"
	"testing"

	"sqlp/dialect"
	"sqlp/internal/testkit"
)

// transpileCase is one input, its expected output and the expected warnings.
type transpileCase struct {
	in, out  string
	warnings []string
}

func transpiles(t *testing.T, from, to *dialect.Dialect, cases []transpileCase) {
	t.Helper()
	if !testkit.Enabled(from.Name) && !testkit.Enabled(to.Name) {
		return
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, warnings, err := Transpile(c.in, from, to)
			if err != nil {
				t.Fatalf("transpile error: %v", err)
			}
			if got != c.out+";" {
				t.Fatalf("\n want: %s\n  got: %s", c.out, got)
			}
			if !reflect.DeepEqual(warnings, c.warnings) {
				t.Fatalf("warnings\n want: %q\n  got: %q", c.warnings, warnings)
			}
		})
	}
}

func unsupported(what, d string) []string {
	return []string{what + " is not supported in dialect " + d}
}

func TestTranspilePostgresToSQLite(t *testing.T) {
	sq := func(what string) []string { return unsupported(what, "sqlite") }
	transpiles(t, dialect.Postgres, dialect.SQLite, []transpileCase{
		{in: "SELECT a::int FROM t", out: "SELECT CAST(a AS INTEGER) FROM t"},
		{in: "SELECT a::int4, b::int8, c::float8, d::bool, e::bytea FROM t",
			out: "SELECT CAST(a AS INTEGER), CAST(b AS BIGINT), CAST(c AS REAL), CAST(d AS BOOLEAN), CAST(e AS BLOB) FROM t"},
		{in: "SELECT (a + b)::text", out: "SELECT CAST((a + b) AS text)"},
		{in: "SELECT a ILIKE 'x%' FROM t", out: "SELECT a LIKE 'x%' FROM t", warnings: sq("ILIKE (LIKE ignores the case of ASCII letters only)")},
		{in: "SELECT a BETWEEN SYMMETRIC 2 AND 1 FROM t", out: "SELECT a BETWEEN 2 AND 1 FROM t", warnings: sq("BETWEEN SYMMETRIC")},
		{in: "SELECT * FROM f() WITH ORDINALITY", out: "SELECT * FROM f()", warnings: sq("WITH ORDINALITY")},
		{in: "VALUES (1) ORDER BY 1", out: "VALUES (1) ORDER BY 1", warnings: sq("ORDER BY or LIMIT after VALUES")},
		{in: "CREATE UNLOGGED TABLE t (a int4)", out: "CREATE TABLE t (a INTEGER)", warnings: sq("UNLOGGED")},
		{in: "SELECT $$raw$$", out: "SELECT 'raw'"},
		{in: "CREATE TABLE t (id serial PRIMARY KEY, created timestamptz, flag bool)",
			out:      "CREATE TABLE t (id INTEGER PRIMARY KEY, created TIMESTAMP, flag BOOLEAN)",
			warnings: []string{sq("SERIAL (its sequence default)")[0], sq("TIMESTAMPTZ (its time zone)")[0]}},
		{in: "CREATE TABLE t (a DOUBLE PRECISION, b character varying(20))", out: "CREATE TABLE t (a REAL, b VARCHAR(20))"},
		{in: "SELECT a FROM t FETCH FIRST 5 ROWS ONLY", out: "SELECT a FROM t LIMIT 5"},
		{in: "SELECT a FROM t OFFSET 2 ROWS FETCH FIRST 5 ROWS ONLY", out: "SELECT a FROM t LIMIT 5 OFFSET 2"},
		{in: "SELECT a FROM t FETCH FIRST ROWS ONLY", out: "SELECT a FROM t LIMIT 1"},
		// lossy conversions must be reported
		{in: "SELECT DISTINCT ON (a) a, b FROM t", out: "SELECT DISTINCT a, b FROM t", warnings: sq("DISTINCT ON")},
		{in: "SELECT a FROM t FOR UPDATE", out: "SELECT a FROM t", warnings: sq("FOR UPDATE")},
		{in: "TRUNCATE t", out: "TRUNCATE TABLE t", warnings: sq("TRUNCATE")},
		{in: "SELECT a @> b FROM t", out: "SELECT a @> b FROM t", warnings: sq("operator @>")},
		{in: "SELECT ARRAY[1, 2]", out: "SELECT ARRAY[1, 2]", warnings: sq("ARRAY")},
		// Output SQLite accepts but reads differently must warn or be rewritten.
		{in: "SELECT a[1] FROM t", out: "SELECT a[1] FROM t", warnings: sq("array subscript")},
		{in: "SELECT @ a FROM t", out: "SELECT @a FROM t", warnings: sq("prefix operator @")},
		{in: "SELECT X'1F', B'01'", out: "SELECT X'1F', B'01'", warnings: []string{sq("bit string X'1F'")[0], sq("bit string B'01'")[0]}},
		{in: "CREATE TABLE x (LIKE t)", out: "CREATE TABLE x (LIKE t)", warnings: sq("LIKE in CREATE TABLE")},
		{in: "SELECT POSITION('a' IN b) FROM t", out: "SELECT instr(b, 'a') FROM t"},
		{in: "SELECT now(), date_trunc('day', a), my_func(a), lower(a) FROM t", out: "SELECT now(), date_trunc('day', a), my_func(a), lower(a) FROM t",
			warnings: []string{sq("function now")[0], sq("function date_trunc")[0]}}, // a function neither has built in is the user's
		{in: `SELECT a COLLATE "C" FROM t`, out: `SELECT a COLLATE "C" FROM t`, warnings: sq("collation C")},
		{in: "SELECT a FROM t LIMIT -1", out: "SELECT a FROM t LIMIT -1"},
		{in: "CREATE TABLE t (a timestamp with time zone, b jsonb)", out: "CREATE TABLE t (a timestamp WITH TIME ZONE, b TEXT)",
			warnings: []string{sq("WITH TIME ZONE")[0], sq("JSONB (as text)")[0]}},
		{in: "VACUUM FULL t", out: "VACUUM FULL t", // SQLite reads FULL as a schema name
			warnings: []string{`the output is not valid in dialect sqlite: syntax error at line 1, column 13: unexpected "t", expected ";"`}},
		// INITIALLY DEFERRED implies DEFERRABLE; SQLite needs the word first.
		{in: "CREATE TABLE x (a int REFERENCES u INITIALLY DEFERRED, b int REFERENCES u INITIALLY IMMEDIATE DEFERRABLE)",
			out: "CREATE TABLE x (a INTEGER REFERENCES u DEFERRABLE INITIALLY DEFERRED, b INTEGER REFERENCES u DEFERRABLE INITIALLY IMMEDIATE)"},
		{in: "CREATE TABLE x (a int, FOREIGN KEY (a) REFERENCES u INITIALLY IMMEDIATE)",
			out: "CREATE TABLE x (a INTEGER, FOREIGN KEY (a) REFERENCES u NOT DEFERRABLE INITIALLY IMMEDIATE)"},
		{in: "CREATE TABLE x (a int, UNIQUE (a) DEFERRABLE)", out: "CREATE TABLE x (a INTEGER, UNIQUE (a))",
			warnings: sq("DEFERRABLE or INITIALLY on a table constraint other than FOREIGN KEY")},
	})
}

// PostgreSQL's E” escapes must be resolved, not copied, for dialects that
// have no escape strings.
func TestTranspileEscapeStrings(t *testing.T) {
	transpiles(t, dialect.Postgres, dialect.SQLite, []transpileCase{
		{in: `SELECT E'a\nb'`, out: "SELECT 'a\nb'"},
		{in: `SELECT E'it\'s'`, out: "SELECT 'it''s'"},
		{in: `SELECT E'it''s'`, out: "SELECT 'it''s'"},
		{in: `SELECT E'tab\there'`, out: "SELECT 'tab\there'"},
		{in: `SELECT E'back\\slash'`, out: `SELECT 'back\slash'`},
		{in: `SELECT E'\101\x42'`, out: "SELECT 'AB'"},
		{in: `SELECT E'\u0041\U00000042'`, out: "SELECT 'AB'"},
		{in: `SELECT E'\u017C'`, out: "SELECT 'ż'"},
		{in: `SELECT E'\303\251\xc3\xa9'`, out: "SELECT 'éé'"},
		{in: `SELECT E'\ud83d\ude00'`, out: "SELECT '\U0001F600'"},
	})
}

// A tree parsed under one dialect's precedence must keep its grouping under
// another's, by adding the parentheses the source did not need.
func TestTranspilePreservesGrouping(t *testing.T) {
	transpiles(t, dialect.Postgres, dialect.SQLite, []transpileCase{
		{in: "SELECT a || b + c", out: "SELECT a || (b + c)"},
		{in: "SELECT a + b || c", out: "SELECT (a + b) || c"}, // || is looser than + in PostgreSQL
		{in: "SELECT a = b OR c < d", out: "SELECT a = b OR c < d"},
		{in: "SELECT a < b + 1", out: "SELECT a < b + 1"},
	})
	transpiles(t, dialect.SQLite, dialect.Postgres, []transpileCase{
		{in: "SELECT a || b + c", out: "SELECT (a || b) + c"},
		{in: "SELECT a = b < c", out: "SELECT a = (b < c)"},
		{in: "SELECT a = b = c", out: "SELECT (a = b) = c"}, // = cannot be chained in PostgreSQL
	})
}

func TestTranspileSQLiteToPostgres(t *testing.T) {
	pg := func(what string) []string { return unsupported(what, "postgres") }
	transpiles(t, dialect.SQLite, dialect.Postgres, []transpileCase{
		{in: "INSERT OR REPLACE INTO t (a) VALUES (1)", out: "INSERT INTO t (a) VALUES (1)", warnings: pg("OR REPLACE")},
		{in: "REPLACE INTO t (a) VALUES (1)", out: "INSERT INTO t (a) VALUES (1)", warnings: pg("REPLACE INTO")},
		{in: "CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, b BLOB, c DATETIME)",
			out: "CREATE TABLE t (id INTEGER PRIMARY KEY, b BYTEA, c TIMESTAMP)", warnings: pg("AUTOINCREMENT")},
		{in: "CREATE TABLE t (a TEXT NOT NULL ON CONFLICT REPLACE)", out: "CREATE TABLE t (a TEXT NOT NULL)", warnings: pg("ON CONFLICT REPLACE")},
		{in: "CREATE TABLE t (a INTEGER) WITHOUT ROWID", out: "CREATE TABLE t (a INTEGER)", warnings: pg("WITHOUT ROWID")},
		{in: "SELECT `a`, [b] FROM t", out: `SELECT "a", "b" FROM t`},
		{in: "SELECT a 'x' FROM t", out: `SELECT a AS "x" FROM t`},
		{in: "SELECT a FROM t LIMIT 5, 10", out: "SELECT a FROM t LIMIT 10 OFFSET 5"},
		{in: "SELECT CAST(a AS DOUBLE) FROM t", out: "SELECT CAST(a AS DOUBLE PRECISION) FROM t"},
		{in: "SELECT * FROM (a)", out: "SELECT * FROM a"},
		{in: "CREATE TABLE t (a INTEGER, b INTEGER GENERATED ALWAYS AS (a + 1))",
			out: "CREATE TABLE t (a INTEGER, b INTEGER GENERATED ALWAYS AS (a + 1) STORED)", warnings: pg("generated column that is not STORED")},
		{in: "CREATE TABLE t (a INTEGER REFERENCES u MATCH PARTIAL)", out: "CREATE TABLE t (a INTEGER REFERENCES u)", warnings: pg("MATCH PARTIAL")},
		{in: "CREATE TABLE t (a INTEGER REFERENCES u MATCH foo ON INSERT CASCADE ON DELETE CASCADE)", out: "CREATE TABLE t (a INTEGER REFERENCES u ON DELETE CASCADE)",
			warnings: []string{pg("MATCH foo")[0], pg("ON INSERT CASCADE")[0]}},
		{in: "SELECT * FROM ((a))", out: "SELECT * FROM a"},
		// Output PostgreSQL accepts but reads differently must warn or be rewritten.
		{in: "SELECT x'ab', X''", out: `SELECT '\xab'::BYTEA, '\x'::BYTEA`}, // a blob, not a bit string
		{in: "SELECT a FROM t LIMIT -1", out: "SELECT a FROM t LIMIT ALL"},  // no limit, not an error
		{in: "SELECT a FROM t LIMIT -1 OFFSET 2", out: "SELECT a FROM t LIMIT ALL OFFSET 2"},
		{in: "SELECT a == b FROM t", out: "SELECT a = b FROM t"},
		{in: "SELECT a LIKE 'x%', b NOT LIKE 'y' FROM t", out: "SELECT a ILIKE 'x%', b NOT ILIKE 'y' FROM t"}, // SQLite's LIKE ignores case
		{in: "SELECT a COLLATE NOCASE FROM t", out: "SELECT a COLLATE NOCASE FROM t", warnings: pg("collation NOCASE")},
		{in: "CREATE TABLE t (a TEXT COLLATE NOCASE)", out: "CREATE TABLE t (a TEXT COLLATE NOCASE)", warnings: pg("collation NOCASE")},
		{in: "SELECT rowid, t._rowid_ FROM t", out: "SELECT rowid, t._rowid_ FROM t", warnings: pg("rowid")},
		{in: "SELECT iif(a, 1, 2), total(a), group_concat(a), coalesce(a, 1), my_func(a) FROM t",
			out:      "SELECT iif(a, 1, 2), total(a), group_concat(a), coalesce(a, 1), my_func(a) FROM t",
			warnings: []string{pg("function iif")[0], pg("function total")[0], pg("function group_concat")[0]}},
		{in: "SELECT * FROM a NATURAL CROSS JOIN b", out: "SELECT * FROM a NATURAL JOIN b"},
		{in: "SELECT * FROM (a) AS x", out: "SELECT * FROM (a) AS x", warnings: pg("parenthesized table")},
	})
}

// Type renames only apply across dialects: in PostgreSQL a type called BLOB is
// a user type and must be left alone.
func TestNoTypeMappingWithinDialect(t *testing.T) {
	testkit.Require(t, dialect.Postgres.Name)
	rewrites(t, dialect.Postgres, []rewrite{{"SELECT a::blob", "SELECT a::blob"}})
}

// Quoted identifiers stay quoted. A bare identifier that is reserved in the
// target gets quoted, in the case the target would have folded it to.
func TestTranspileQuoting(t *testing.T) {
	transpiles(t, dialect.Postgres, dialect.SQLite, []transpileCase{
		{in: `SELECT "user", key FROM t`, out: `SELECT "user", key FROM t`},
	})
	transpiles(t, dialect.SQLite, dialect.Postgres, []transpileCase{
		{in: `SELECT user, "offset" FROM t`, out: `SELECT "user", "offset" FROM t`},
		{in: `SELECT USER FROM t`, out: `SELECT "user" FROM t`},
	})
}

func TestDialectByName(t *testing.T) {
	for name, want := range map[string]*dialect.Dialect{
		"postgres": dialect.Postgres, "PostgreSQL": dialect.Postgres, "pg": dialect.Postgres,
		"sqlite": dialect.SQLite, "SQLite3": dialect.SQLite,
	} {
		got, ok := dialect.ByName(name)
		if !ok || got != want {
			t.Errorf("ByName(%q) = %v, %v", name, got, ok)
		}
	}
	if _, ok := dialect.ByName("oracle"); ok {
		t.Error("ByName(oracle) should fail")
	}
}

// DML that BigQuery has no syntax for comes out unchanged, with a warning.
func TestTranspileDMLToBigQuery(t *testing.T) {
	bq := func(what string) []string { return unsupported(what, "bigquery") }
	transpiles(t, dialect.Postgres, dialect.BigQuery, []transpileCase{
		{in: "TRUNCATE t", out: "TRUNCATE TABLE t"},
		{in: "TRUNCATE t, u RESTART IDENTITY", out: "TRUNCATE TABLE t, u RESTART IDENTITY",
			warnings: bq("TRUNCATE with ONLY, several tables, IDENTITY, CASCADE or RESTRICT")},
		{in: "INSERT INTO t DEFAULT VALUES", out: "INSERT INTO t DEFAULT VALUES", warnings: bq("DEFAULT VALUES")},
		{in: "INSERT INTO t AS x VALUES (1)", out: "INSERT INTO t AS x VALUES (1)", warnings: bq("INSERT INTO t AS alias")},
		{in: "UPDATE t SET (a, b) = (1, 2) WHERE true", out: "UPDATE t SET (a, b) = (1, 2) WHERE TRUE", warnings: bq("SET (a, b) = ...")},
		{in: "WITH x AS (SELECT 1) DELETE FROM t WHERE a IN (SELECT * FROM x)",
			out:      "WITH x AS (SELECT 1) DELETE FROM t WHERE a IN (SELECT * FROM x)",
			warnings: bq("WITH before INSERT, UPDATE, DELETE or MERGE")},
		{in: "UPDATE t SET a = 1 WHERE b = 2", out: "UPDATE t SET a = 1 WHERE b = 2"},
	})
}
