package sqlp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sqlp/ast"
	"sqlp/dialect"
	"sqlp/internal/corpus"
)

// wantStatement is one expected Statement. err is a fragment of the error, or
// empty when the statement must parse.
type wantStatement struct {
	text, err string
	opaque    bool
}

func ok(text string) wantStatement { return wantStatement{text: text} }

var scriptCases = []struct {
	name, sql, only string
	want            []wantStatement
}{
	{name: "empty", sql: ""},
	{name: "only separators", sql: " ; ; "},
	{name: "only a comment", sql: "-- nothing"},
	{name: "two", sql: "SELECT 1; SELECT 2", want: []wantStatement{ok("SELECT 1"), ok("SELECT 2")}},
	{name: "extra separators", sql: ";;SELECT 1;; ;", want: []wantStatement{ok("SELECT 1")}},
	{name: "separator in string", sql: "SELECT ';'; SELECT 2", want: []wantStatement{ok("SELECT ';'"), ok("SELECT 2")}},
	{name: "edge comments excluded", sql: "-- a\nSELECT 1 /* b */ + 2 -- c\n; SELECT 2",
		want: []wantStatement{ok("SELECT 1 /* b */ + 2"), ok("SELECT 2")}},
	{name: "bad in the middle", sql: "SELECT 1;\nSELEC 2;\nSELECT 3",
		want: []wantStatement{ok("SELECT 1"), {text: "SELEC 2", err: `"SELEC"`}, ok("SELECT 3")}},
	{name: "several bad", sql: "SELEC 1; SELEC 2; SELECT 3",
		want: []wantStatement{{text: "SELEC 1", err: "SELEC"}, {text: "SELEC 2", err: "SELEC"}, ok("SELECT 3")}},
	{name: "unclosed bracket stops at separator", sql: "SELECT (1; SELECT 2",
		want: []wantStatement{{text: "SELECT (1", err: "unexpected"}, ok("SELECT 2")}},
	{name: "missing separator", sql: "SELECT 1 SELECT 2; SELECT 3",
		want: []wantStatement{{text: "SELECT 1 SELECT 2", err: `expected ";"`}, ok("SELECT 3")}},
	{name: "unterminated string", sql: "SELECT 1; SELECT 'abc; SELECT 2  ",
		want: []wantStatement{ok("SELECT 1"), {text: "SELECT 'abc; SELECT 2", err: "unterminated"}}},
	{name: "transaction BEGIN is not a body", sql: "BEGIN; SELEC 1; COMMIT",
		want: []wantStatement{ok("BEGIN"), {text: "SELEC 1", err: "SELEC"}, ok("COMMIT")}},
	{name: "trigger body", only: "sqlite", sql: "CREATE TRIGGER g AFTER INSERT ON t BEGIN DELETE FROM u; SELECT 1; END; SELECT 2",
		want: []wantStatement{ok("CREATE TRIGGER g AFTER INSERT ON t BEGIN DELETE FROM u; SELECT 1; END"), ok("SELECT 2")}},
	{name: "bad trigger body", only: "sqlite", sql: "CREATE TRIGGER g AFTER INSERT ON t BEGIN SELECT CASE WHEN 1 THEN 2 END; SELEC 1; END; SELECT 2",
		want: []wantStatement{{text: "CREATE TRIGGER g AFTER INSERT ON t BEGIN SELECT CASE WHEN 1 THEN 2 END; SELEC 1; END", err: "SELEC"}, ok("SELECT 2")}},
	{name: "atomic body", only: "postgres", sql: "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; SELECT 2; END; SELECT 3",
		want: []wantStatement{ok("CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; SELECT 2; END"), ok("SELECT 3")}},
	{name: "bad atomic body", only: "postgres", sql: "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1 +; SELECT 2; END; SELECT 3",
		want: []wantStatement{{text: "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1 +; SELECT 2; END", err: "unexpected"}, ok("SELECT 3")}},
	{name: "dollar quoted body", only: "postgres", sql: "DO $$ BEGIN PERFORM 1; END $$; SELEC",
		want: []wantStatement{ok("DO $$ BEGIN PERFORM 1; END $$"), {text: "SELEC", err: "SELEC"}}},
	{name: "opaque", only: "postgres", sql: "DROP FUNCTION f; SELECT 1",
		want: []wantStatement{{text: "DROP FUNCTION f", opaque: true}, ok("SELECT 1")}},
	{name: "too deep is per statement", sql: "SELECT " + parens(5000) + "; SELECT 2",
		want: []wantStatement{{text: "SELECT " + parens(5000), err: "max nesting depth"}, ok("SELECT 2")}},
}

func TestParseScript(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		for _, c := range scriptCases {
			if c.only != "" && c.only != d.Name {
				continue
			}
			t.Run(c.name, func(t *testing.T) {
				script, err := ParseScript(context.Background(), c.sql, d, Options{})
				if err != nil {
					t.Fatal(err)
				}
				if len(script.Statements) != len(c.want) {
					t.Fatalf("got %d statements, want %d: %+v", len(script.Statements), len(c.want), script.Statements)
				}
				for i, want := range c.want {
					got := script.Statements[i]
					if got.Text != want.text || got.Opaque != want.opaque {
						t.Errorf("statement %d: got %q opaque=%v, want %q opaque=%v", i, got.Text, got.Opaque, want.text, want.opaque)
					}
					switch {
					case want.err == "" && got.Err != nil:
						t.Errorf("statement %d: unexpected error: %v", i, got.Err)
					case want.err != "" && (got.Err == nil || !strings.Contains(got.Err.Error(), want.err)):
						t.Errorf("statement %d: error %v does not contain %q", i, got.Err, want.err)
					case (got.Node == nil) == (got.Err == nil):
						t.Errorf("statement %d: want exactly one of Node and Err", i)
					}
				}
			})
		}
	})
}

func TestParseScriptSpans(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		sql := "SELECT 1;\n  SELECT\n 2;\n"
		if d == dialect.SQLite {
			sql = "SELECT 'ééé';\n  SELECT\n 2;\n" // columns count characters, offsets bytes
		}
		script, err := ParseScript(context.Background(), sql, d, Options{})
		if err != nil {
			t.Fatal(err)
		}
		want := []Span{
			{Start: 0, End: 8, StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 9},
			{Start: 12, End: 21, StartLine: 2, StartColumn: 3, EndLine: 3, EndColumn: 3},
		}
		if d == dialect.SQLite {
			want = []Span{
				{Start: 0, End: 15, StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 13},
				{Start: 19, End: 28, StartLine: 2, StartColumn: 3, EndLine: 3, EndColumn: 3},
			}
		}
		for i, s := range script.Statements {
			if s.Span != want[i] {
				t.Errorf("statement %d: got %+v, want %+v", i, s.Span, want[i])
			}
		}
	})
}

// Line lookups and panics must not make a script of bad statements quadratic.
func TestPerfManyBadStatements(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		sql := strings.Repeat("SELEC 1;\n", 200000)
		start := time.Now()
		script, err := ParseScript(context.Background(), sql, d, Options{Limits{MaxBytes: -1}})
		if err != nil || len(script.Statements) != 200000 {
			t.Fatalf("got %v", err)
		}
		if took := time.Since(start); took > 5*time.Second {
			t.Fatalf("200,000 bad statements took %v", took)
		}
		t.Logf("took %v", time.Since(start))
	})
}

func TestParseScriptWholeScriptErrors(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		if s, err := ParseScript(context.Background(), "SELECT 1", d, Options{Limits{MaxBytes: 2}}); s != nil || err == nil {
			t.Errorf("size limit: got %v, %v", s, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if s, err := ParseScript(ctx, "SELECT 1", d, Options{}); s != nil || err == nil || !strings.Contains(err.Error(), "parse aborted") {
			t.Errorf("cancelled: got %v, %v", s, err)
		}
	})
}

// A script of the frozen statements must give the same results as parsing each
// one alone, valid or not. Hand-written lists and their mutations hold no bodies
// for recovery to misread, so the statement count must match exactly.
func TestParseScriptMatchesSingleStatements(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		files, _ := filepath.Glob(filepath.Join("testdata", "corpus", d.Name, "curated_*.jsonl"))
		for _, f := range files {
			rows, err := corpus.Read[corpus.Record](f)
			if err != nil {
				t.Fatal(err)
			}
			sqls := make([]string, len(rows))
			for i, r := range rows {
				sqls[i] = r.SQL
			}
			script, err := ParseScript(context.Background(), strings.Join(sqls, ";\n"), d, Options{Limits{MaxBytes: -1}})
			if err != nil {
				t.Fatal(err)
			}
			if len(script.Statements) != len(sqls) {
				t.Fatalf("%s: got %d statements, want %d", f, len(script.Statements), len(sqls))
			}
			for i, s := range script.Statements {
				alone := corpus.Parse(sqls[i], d)
				if (alone.Err == nil) != (s.Err == nil) {
					t.Errorf("%q: alone %v, in script %v", sqls[i], alone.Err, s.Err)
				} else if s.Err == nil && Format(alone.Stmts, d) != Format([]ast.Statement{s.Node}, d) {
					t.Errorf("%q: different result in a script", sqls[i])
				}
			}
		}
	})
}

func TestParseIsAllOrNothing(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		if stmts, err := Parse("SELECT 1; SELEC 2", d); stmts != nil || err == nil {
			t.Fatalf("got %v, %v", stmts, err)
		}
	})
}

func TestParseOne(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		for _, sql := range []string{"SELECT 1", "SELECT 1;", ";SELECT 1;;"} {
			if _, err := ParseOne(sql, d); err != nil {
				t.Errorf("%q: %v", sql, err)
			}
		}
		for _, c := range []struct{ sql, msg string }{
			{"", "line 1, column 1: expected one statement, found none"},
			{";", "found none"},
			{"SELECT 1;\nSELECT 2", "line 2, column 1: expected one statement, found 2"},
			{"SELEC 1", `"SELEC"`},
		} {
			if _, err := ParseOne(c.sql, d); err == nil || !strings.Contains(err.Error(), c.msg) {
				t.Errorf("%q: error %v does not contain %q", c.sql, err, c.msg)
			}
		}
	})
}

func TestTranspileScript(t *testing.T) {
	sql := "SELECT a::int FROM t; SELEC 1; SELECT a FROM t FOR UPDATE"
	got, err := TranspileScript(context.Background(), sql, dialect.Postgres, dialect.SQLite, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d statements: %+v", len(got), got)
	}
	if got[0].Output != "SELECT CAST(a AS INTEGER) FROM t;" || len(got[0].Warnings) != 0 {
		t.Errorf("first: %+v", got[0])
	}
	if got[1].Err == nil || got[1].Output != "" {
		t.Errorf("second: %+v", got[1])
	}
	if got[2].Output != "SELECT a FROM t;" || len(got[2].Warnings) != 1 || !strings.Contains(got[2].Warnings[0], "FOR UPDATE") {
		t.Errorf("third: %+v", got[2])
	}
}
