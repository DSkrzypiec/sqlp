package sqlp

import (
	"context"
	"strings"
	"testing"
	"time"

	"sqlp/ast"
	"sqlp/dialect"
	"sqlp/gen"
	"sqlp/internal/corpus"
	"sqlp/internal/testkit"
)

// FuzzParse asserts that Parse never panics or runs away, and that whatever it
// accepts survives format, parse, format with the same text and tree. Whether
// the input should be accepted at all is the corpus and oracle tests' job.
//
//	go test -fuzz=FuzzParse
func FuzzParse(f *testing.F) {
	for _, sql := range commonIdentity {
		f.Add(sql)
	}
	for _, sql := range postgresIdentity {
		f.Add(sql)
	}
	for _, sql := range sqliteIdentity {
		f.Add(sql)
	}
	for _, c := range commonFailures {
		f.Add(c.sql)
	}
	for _, c := range postgresFailures {
		f.Add(c.sql)
	}
	for _, c := range sqliteFailures {
		f.Add(c.sql)
	}
	for _, c := range corpus.ANSI {
		f.Add(c.SQL)
	}
	f.Add(nestedParenExpr(200))
	f.Add(nestedParenJoin(200, dialect.Postgres))

	f.Fuzz(func(t *testing.T, sql string) {
		for _, d := range dialect.All {
			if !testkit.Enabled(d.Name) {
				continue
			}
			checkFormatStable(t, sql, d)
			checkScript(t, sql, d)
			if stmts, err := Parse(sql, d); err == nil {
				checkQStruct(t, sql, stmts, d)
				checkOutCols(t, sql, stmts, d)
			}
		}
	})
}

// checkScript requires ParseScript to agree with Parse and to locate every
// statement exactly.
func checkScript(t *testing.T, sql string, d *dialect.Dialect) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	script, err := ParseScript(ctx, sql, d, Options{Limits: Limits{MaxBytes: -1}})
	if err != nil {
		return
	}
	last := 0
	for _, s := range script.Statements {
		if (s.Node == nil) == (s.Err == nil) {
			t.Fatalf("%s: want exactly one of Node and Err: %q", d.Name, sql)
		}
		if s.Span.Start < last || s.Span.End <= s.Span.Start || s.Span.End > len(sql) || sql[s.Span.Start:s.Span.End] != s.Text {
			t.Fatalf("%s: bad span %+v for %q in %q", d.Name, s.Span, s.Text, sql)
		}
		last = s.Span.End
	}
	stmts, err := Parse(sql, d)
	if err != nil {
		return
	}
	if len(stmts) != len(script.Statements) {
		t.Fatalf("%s: Parse found %d statements, ParseScript %d: %q", d.Name, len(stmts), len(script.Statements), sql)
	}
	for i, s := range script.Statements {
		if s.Err != nil {
			t.Fatalf("%s: Parse accepts what ParseScript rejects: %v: %q", d.Name, s.Err, sql)
		}
		if a, b := Format(stmts[i:i+1], d), Format([]ast.Statement{s.Node}, d); a != b {
			t.Fatalf("%s: different result\n  Parse:       %q\n  ParseScript: %q", d.Name, a, b)
		}
	}
}

func checkFormatStable(t *testing.T, sql string, d *dialect.Dialect) {
	// The context bounds worst-case hang time; go test -fuzz's own per-case
	// timeout is the backstop if that bound is ever wrong.
	parse := func(s string) ([]ast.Statement, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return ParseWithLimits(ctx, s, d, Limits{MaxBytes: -1})
	}
	stmts, err := parse(sql)
	if err != nil {
		return
	}
	out := Format(stmts, d)
	again, err := parse(out)
	if err != nil {
		if msg := err.Error(); strings.Contains(msg, "max nesting depth") || strings.Contains(msg, "parse aborted") {
			return // the output is legitimately at or past a resource limit
		}
		t.Fatalf("%s: output does not re-parse: %v\n  in:  %q\n  out: %q", d.Name, err, sql, out)
	}
	if second := Format(again, d); second != out {
		t.Fatalf("%s: unstable round trip\n  in:  %q\n  1st: %q\n  2nd: %q", d.Name, sql, out, second)
	}
	if a, b := gen.Shape(stmts, d), gen.Shape(again, d); a != b {
		t.Fatalf("%s: tree changed\n  in:  %q\n  1st: %q\n  2nd: %q", d.Name, sql, a, b)
	}
}
