package sqlp

import (
	"strings"
	"testing"
	"time"

	"sqlp/dialect"
	"sqlp/parser"
)

// These guard against the pathological nesting documented in AGENT_TODO.md:
// a backtracking parse of an ambiguous parenthesized construct that redoes
// work at every nesting level. 200 levels must finish in well under a
// second; the old backtracking parser took over a second at 20 levels.

func nestedParenExpr(n int) string {
	s := "1"
	for i := 0; i < n; i++ {
		s = "((SELECT " + s + ") + 1)"
	}
	return s
}

func nestedParenJoin(n int, d *dialect.Dialect) string {
	alias := " AS c"
	if d.Features.Query.ParenJoinOnly { // BigQuery: no alias after a parenthesized join
		alias = ""
	}
	s := "SELECT 1"
	for i := 0; i < n; i++ {
		s = "SELECT * FROM ((" + s + ") AS a JOIN b ON true)" + alias
	}
	return s
}

func TestPerfNestedParenExpr(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		start := time.Now()
		if _, err := parser.ParseExpr(nestedParenExpr(200), d); err != nil {
			t.Fatalf("parse error: %v", err)
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("200 levels took %v, want well under 1s", took)
		}
	})
}

func TestPerfNestedParenJoin(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		start := time.Now()
		if _, err := parser.Parse(nestedParenJoin(200, d), d); err != nil {
			t.Fatalf("parse error: %v", err)
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("200 levels took %v, want well under 1s", took)
		}
	})
}

func TestPerfLongUnclosedParenRun(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		start := time.Now()
		if _, err := parser.ParseExpr("SELECT "+strings.Repeat("(", 100000), d); err == nil {
			t.Fatal("expected error for unclosed parens")
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("100,000 unclosed '(' took %v, want well under 1s", took)
		}
	})
}
