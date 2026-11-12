package parser

import (
	"fmt"
	"strings"
	"testing"

	"sqlp/ast"
	"sqlp/dialect"
	"sqlp/internal/testkit"
	"sqlp/lexer"
)

// sexpr renders an expression tree as a fully parenthesized s-expression so
// tests can assert on structure (precedence, associativity) rather than text.
func sexpr(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Name:
		parts := make([]string, len(e.Parts))
		for i, p := range e.Parts {
			parts[i] = p.Name
		}
		return strings.Join(parts, ".")
	case *ast.Literal:
		switch e.Kind {
		case ast.Null:
			return "NULL"
		case ast.True:
			return "TRUE"
		case ast.False:
			return "FALSE"
		case ast.StringLit:
			return "'" + e.Value + "'"
		}
		return e.Value
	case *ast.Param:
		return e.Text
	case *ast.Unary:
		return fmt.Sprintf("(%s %s)", e.Op, sexpr(e.Expr))
	case *ast.Binary:
		return fmt.Sprintf("(%s %s %s)", e.Op, sexpr(e.Left), sexpr(e.Right))
	case *ast.Quantified:
		return fmt.Sprintf("(%s %s)", e.Kind, sexpr(e.Expr))
	case *ast.Is:
		kind := [...]string{"NULL", "TRUE", "FALSE", "UNKNOWN", "DISTINCT"}[e.Kind]
		s := "(IS"
		if e.Not {
			s += "-NOT"
		}
		s += " " + kind + " " + sexpr(e.Expr)
		if e.Other != nil {
			s += " " + sexpr(e.Other)
		}
		return s + ")"
	case *ast.Between:
		op := "BETWEEN"
		if e.Not {
			op = "NOT-BETWEEN"
		}
		return fmt.Sprintf("(%s %s %s %s)", op, sexpr(e.Expr), sexpr(e.Low), sexpr(e.High))
	case *ast.In:
		op := "IN"
		if e.Not {
			op = "NOT-IN"
		}
		items := make([]string, len(e.List))
		for i, x := range e.List {
			items[i] = sexpr(x)
		}
		if e.Subquery != nil {
			return fmt.Sprintf("(%s %s <subquery>)", op, sexpr(e.Expr))
		}
		return fmt.Sprintf("(%s %s [%s])", op, sexpr(e.Expr), strings.Join(items, " "))
	case *ast.Like:
		op := strings.ReplaceAll(e.Op, " ", "-")
		if e.Not {
			op = "NOT-" + op
		}
		s := fmt.Sprintf("(%s %s %s", op, sexpr(e.Expr), sexpr(e.Pattern))
		if e.Escape != nil {
			s += " ESCAPE " + sexpr(e.Escape)
		}
		return s + ")"
	case *ast.Cast:
		return fmt.Sprintf("(CAST %s %s)", sexpr(e.Expr), e.Type.Name)
	case *ast.Collate:
		return fmt.Sprintf("(COLLATE %s %s)", sexpr(e.Expr), e.Collation.Last().Name)
	case *ast.AtTimeZone:
		return fmt.Sprintf("(ATZ %s %s)", sexpr(e.Expr), sexpr(e.Zone))
	case *ast.Subscript:
		if e.Slice {
			return fmt.Sprintf("(SLICE %s)", sexpr(e.Expr))
		}
		return fmt.Sprintf("(IDX %s %s)", sexpr(e.Expr), sexpr(e.Index))
	case *ast.Paren:
		return fmt.Sprintf("(PAREN %s)", sexpr(e.Expr))
	case *ast.Tuple:
		items := make([]string, len(e.Exprs))
		for i, x := range e.Exprs {
			items[i] = sexpr(x)
		}
		return "(TUPLE " + strings.Join(items, " ") + ")"
	case *ast.FuncCall:
		args := make([]string, len(e.Args))
		for i, x := range e.Args {
			args[i] = sexpr(x)
		}
		return fmt.Sprintf("(CALL %s %s)", e.Name.Last().Name, strings.Join(args, " "))
	case *ast.Star:
		return "*"
	case *ast.Subquery:
		return "<subquery>"
	case *ast.TypedString:
		return fmt.Sprintf("(TYPED %s '%s')", e.Type.Name, e.Value)
	}
	return fmt.Sprintf("<%T>", e)
}

var postgresPrecedence = map[string]string{
	"a + b * c":                     "(+ a (* b c))",
	"a * b + c":                     "(+ (* a b) c)",
	"a - b - c":                     "(- (- a b) c)",
	"a / b / c":                     "(/ (/ a b) c)",
	"a ^ b * c":                     "(* (^ a b) c)",
	"-a ^ b":                        "(^ (- a) b)", // unary minus binds tighter than ^ (-2^2 = 4)
	"-a * b":                        "(* (- a) b)",
	"a || b || c":                   "(|| (|| a b) c)",
	"a || b + c":                    "(|| a (+ b c))",
	"a = b AND c = d":               "(AND (= a b) (= c d))",
	"a AND b OR c AND d":            "(OR (AND a b) (AND c d))",
	"a OR b AND c":                  "(OR a (AND b c))",
	"NOT a AND b":                   "(AND (NOT a) b)",
	"NOT a = b":                     "(NOT (= a b))",
	"NOT a IN (1, 2)":               "(NOT (IN a [1 2]))",
	"a NOT IN (1, 2)":               "(NOT-IN a [1 2])",
	"a = b IS NULL":                 "(IS NULL (= a b))",
	"a IS NOT NULL AND b":           "(AND (IS-NOT NULL a) b)",
	"a IS DISTINCT FROM b":          "(IS DISTINCT a b)",
	"a + b IS NOT DISTINCT FROM c":  "(IS-NOT DISTINCT (+ a b) c)",
	"a BETWEEN 1 AND 2 AND b":       "(AND (BETWEEN a 1 2) b)",
	"a NOT BETWEEN 1 + 1 AND 2 * 2": "(NOT-BETWEEN a (+ 1 1) (* 2 2))",
	"a LIKE 'x' ESCAPE '!' AND b":   "(AND (LIKE a 'x' ESCAPE '!') b)",
	"a NOT LIKE 'x' || 'y'":         "(NOT-LIKE a (|| 'x' 'y'))",
	"a ILIKE b":                     "(ILIKE a b)",
	"a SIMILAR TO b":                "(SIMILAR-TO a b)",
	"@ a + b":                       "(@ (+ a b))", // prefix generic operators have Op precedence
	"~ a + b":                       "(~ (+ a b))",
	"a OPERATOR(pg_catalog.+) b":    "(OPERATOR(pg_catalog.+) a b)",
	"(a).b + 1":                     "(+ <*ast.Field> 1)",
	"a IN (1) IN (2)":               "(IN (IN a [1]) [2])",
	"interval '1' year":             "(TYPED interval '1')",
	"a::int + 1":                    "(+ (CAST a int) 1)",
	"-1::int":                       "(- (CAST 1 int))",
	"a[1] + 1":                      "(+ (IDX a 1) 1)",
	"a[1][2]":                       "(IDX (IDX a 1) 2)",
	"a COLLATE \"C\" = b":           "(= (COLLATE a C) b)",
	"ts AT TIME ZONE 'UTC' + 1":     "(+ (ATZ ts 'UTC') 1)", // AT binds tighter than +
	"a @> b AND c":                  "(AND (@> a b) c)",
	"a -> 'k' ->> 'j'":              "(->> (-> a 'k') 'j')",
	"(a + b) * c":                   "(* (PAREN (+ a b)) c)",
	"(a, b) = (1, 2)":               "(= (TUPLE a b) (TUPLE 1 2))",
	"a = ANY (ARRAY[1, 2])":         "(= a (ANY <*ast.Array>))",
	"a = ANY (SELECT 1)":            "(= a (ANY <subquery>))",
	"x ISNULL OR y NOTNULL":         "(OR (IS NULL x) (IS-NOT NULL y))",
	"DATE '2020-01-01' < now()":     "(< (TYPED DATE '2020-01-01') (CALL now ))",
	"$1 + $2":                       "(+ $1 $2)",
	"a % b":                         "(% a b)",
}

// Set operators: PostgreSQL gives INTERSECT higher precedence, SQLite does not.
func TestSetOperatorGrouping(t *testing.T) {
	var shape func(ast.QueryBody) string
	shape = func(b ast.QueryBody) string {
		so, ok := b.(*ast.SetOp)
		if !ok {
			return "s"
		}
		return "(" + shape(so.Left) + [...]string{"U", "I", "E"}[so.Op] + shape(so.Right) + ")"
	}
	const sql = "SELECT 1 UNION SELECT 2 INTERSECT SELECT 3 EXCEPT SELECT 4"
	for d, want := range map[*dialect.Dialect]string{
		dialect.Postgres: "((sU(sIs))Es)",
		dialect.SQLite:   "(((sUs)Is)Es)",
	} {
		testkit.Run(t, d.Name, func(t *testing.T) {
			stmts, err := Parse(sql, d)
			if err != nil {
				t.Fatalf("%s: %v", d.Name, err)
			}
			if got := shape(stmts[0].(*ast.Query).Body); got != want {
				t.Errorf("%s: got %s, want %s", d.Name, got, want)
			}
		})
	}
}

var sqlitePrecedence = map[string]string{
	"a + b * c":             "(+ a (* b c))",
	"a || b + c":            "(+ (|| a b) c)", // || binds tighter than + in SQLite
	"a & b | c":             "(| (& a b) c)",
	"a << 1 + 1":            "(<< a (+ 1 1))",
	"a = b < c":             "(= a (< b c))", // comparison binds tighter than = in SQLite
	"a IS b":                "(IS a b)",
	"a IS NOT b":            "(IS NOT a b)",
	"a IS NULL":             "(IS NULL a)",
	"a GLOB 'x*' AND b":     "(AND (GLOB a 'x*') b)",
	"a REGEXP b":            "(REGEXP a b)",
	"a MATCH b":             "(MATCH a b)",
	"a NOT GLOB b":          "(NOT-GLOB a b)",
	"a NOT NULL":            "(IS-NOT NULL a)",
	"a -> 'k' ->> 'j'":      "(->> (-> a 'k') 'j')",
	"a == b":                "(== a b)",
	"~a + b":                "(+ (~ a) b)",
	"? + ?1 + :x + @y + $z": "(+ (+ (+ (+ ? ?1) :x) @y) $z)",
	"a COLLATE nocase = b":  "(= (COLLATE a nocase) b)",
	"NOT a BETWEEN 1 AND 2": "(NOT (BETWEEN a 1 2))",
}

func TestPrecedence(t *testing.T) {
	testkit.Run(t, dialect.Postgres.Name, func(t *testing.T) { runSexpr(t, dialect.Postgres, postgresPrecedence) })
	testkit.Run(t, dialect.SQLite.Name, func(t *testing.T) { runSexpr(t, dialect.SQLite, sqlitePrecedence) })
}

func runSexpr(t *testing.T, d *dialect.Dialect, cases map[string]string) {
	t.Helper()
	for sql, want := range cases {
		t.Run(sql, func(t *testing.T) {
			e, err := ParseExpr(sql, d)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if got := sexpr(e); got != want {
				t.Fatalf("\n want: %s\n  got: %s", want, got)
			}
		})
	}
}

func TestParseExprRejectsTrailingInput(t *testing.T) {
	testkit.Require(t, dialect.Postgres.Name)
	_, err := ParseExpr("a + b c", dialect.Postgres)
	if err == nil || !strings.Contains(err.Error(), "expected end of expression") {
		t.Fatalf("want trailing-input error, got %v", err)
	}
}

func TestErrorPosition(t *testing.T) {
	testkit.Require(t, dialect.Postgres.Name)
	_, err := Parse("SELECT a\nFROM t\nWHERE 1 =", dialect.Postgres)
	pe, ok := err.(*Error)
	if !ok {
		t.Fatalf("want *Error, got %T %v", err, err)
	}
	if pe.Line != 3 || pe.Column != 10 {
		t.Fatalf("want line 3 col 10, got line %d col %d (%s)", pe.Line, pe.Column, pe.Msg)
	}
}

// A non-*Error panic (a parser bug, not a syntax error) must be reported as
// an error, not propagated: it would otherwise crash a long-running caller
// such as an API server (see AGENT_TODO.md).
func TestRecoverNonSyntaxPanic(t *testing.T) {
	testkit.Require(t, dialect.Postgres.Name)
	func() {
		p := &Parser{d: dialect.Postgres, src: "x", toks: []lexer.Token{{Kind: lexer.EOF, Pos: 1}}}
		var err error
		defer func() {
			if err == nil {
				t.Fatal("expected the panic to be converted to an error")
			}
			if !strings.Contains(err.Error(), "internal error") {
				t.Fatalf("want an internal error, got %v", err)
			}
		}()
		defer p.recover(&err)
		panic(fmt.Errorf("boom"))
	}()
}
