// Package sqlp is a multi-dialect SQL parser and generator.
//
//	stmts, err := sqlp.Parse("SELECT a::int FROM t", dialect.Postgres)
//	fmt.Println(sqlp.Format(stmts, dialect.Postgres)) // SELECT a::int FROM t;
//	out, warnings, err := sqlp.Transpile(sql, dialect.Postgres, dialect.SQLite)
//
// Parse is all or nothing. ParseScript reports each statement of a script
// separately, keeps going past syntax errors, and locates every statement in
// the source.
package sqlp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"sqlp/ast"
	"sqlp/dialect"
	"sqlp/gen"
	"sqlp/parser"
)

// Limits bounds the work a single parse will do; see parser.Limits.
type Limits = parser.Limits

// Parse parses a script of ';'-separated statements in the given dialect, with
// default resource limits (see ParseWithLimits for untrusted input). It returns
// every statement or an error, never both.
func Parse(sql string, d *dialect.Dialect) ([]ast.Statement, error) {
	return parser.Parse(sql, d)
}

// ParseWithLimits is Parse with caller-specified resource limits and a
// context whose cancellation or deadline aborts the parse. Use this, not
// Parse, for input from an untrusted source.
func ParseWithLimits(ctx context.Context, sql string, d *dialect.Dialect, limits Limits) ([]ast.Statement, error) {
	return parser.ParseWithLimits(ctx, sql, d, limits)
}

// Script, Statement and Span are the results of ParseScript.
type (
	Script    = parser.Script
	Statement = parser.Statement
	Span      = parser.Span
)

// Options configures ParseScript and TranspileScript.
type Options struct {
	Limits Limits
}

// ParseScript parses a script and reports each statement separately, keeping
// going past syntax errors. The returned error is only for failures that affect
// the whole script: the size limit and cancellation of ctx.
func ParseScript(ctx context.Context, sql string, d *dialect.Dialect, o Options) (*Script, error) {
	return parser.ParseScript(ctx, sql, d, o.Limits)
}

// ParseOne parses exactly one statement.
func ParseOne(sql string, d *dialect.Dialect) (ast.Statement, error) {
	script, err := ParseScript(context.Background(), sql, d, Options{})
	if err != nil {
		return nil, err
	}
	for _, s := range script.Statements {
		if s.Err != nil {
			return nil, s.Err
		}
	}
	switch n := len(script.Statements); n {
	case 0:
		return nil, &parser.Error{Line: 1, Column: 1, Msg: "expected one statement, found none"}
	case 1:
		return script.Statements[0].Node, nil
	default:
		second := script.Statements[1].Span
		return nil, &parser.Error{Pos: second.Start, Line: second.StartLine, Column: second.StartColumn,
			Msg: fmt.Sprintf("expected one statement, found %d", n)}
	}
}

// ParseExpr parses a standalone expression, with default resource limits.
func ParseExpr(sql string, d *dialect.Dialect) (ast.Expr, error) {
	return parser.ParseExpr(sql, d)
}

// ParseExprWithLimits is ParseExpr with caller-specified resource limits and
// a context whose cancellation or deadline aborts the parse.
func ParseExprWithLimits(ctx context.Context, sql string, d *dialect.Dialect, limits Limits) (ast.Expr, error) {
	return parser.ParseExprWithLimits(ctx, sql, d, limits)
}

// Format renders statements as canonical SQL for dialect d, one per line.
func Format(stmts []ast.Statement, d *dialect.Dialect) string {
	return gen.Statements(stmts, d)
}

// Transpile parses sql in dialect from and renders it for dialect to. The
// warnings name constructs the target cannot express: for those statements the
// output is not equivalent to the input, and callers should not ignore them.
func Transpile(
	sql string, from, to *dialect.Dialect,
) (out string, warnings []string, err error) {
	stmts, err := parser.Parse(sql, from)
	if err != nil {
		return "", nil, err
	}
	parts := make([]string, len(stmts))
	for i, s := range stmts {
		var ws []string
		parts[i], ws = translate(s, from, to)
		for _, w := range ws {
			if !slices.Contains(warnings, w) {
				warnings = append(warnings, w)
			}
		}
	}
	return strings.Join(parts, "\n"), warnings, nil
}

// translate renders one statement for dialect to. When the generator has no
// warning, it parses the output as the target would and warns if the target
// rejects it, so that what the generator does not know about is not silent.
func translate(s ast.Statement, from, to *dialect.Dialect) (string, []string) {
	out, warnings := gen.Translate([]ast.Statement{s}, from, to)
	if from != to && len(warnings) == 0 {
		if _, err := parser.Parse(out, to); err != nil {
			warnings = append(warnings, fmt.Sprintf("the output is not valid in dialect %s: %v", to.Name, err))
		}
	}
	return out, warnings
}

// TranspiledStatement is one statement of TranspileScript. Output is empty when
// Err is set. Warnings name constructs the target cannot express.
type TranspiledStatement struct {
	Statement
	Output   string
	Warnings []string
}

// TranspileScript is ParseScript followed by a per-statement Transpile.
func TranspileScript(ctx context.Context, sql string, from, to *dialect.Dialect, o Options) ([]TranspiledStatement, error) {
	script, err := ParseScript(ctx, sql, from, o)
	if err != nil {
		return nil, err
	}
	out := make([]TranspiledStatement, len(script.Statements))
	for i, s := range script.Statements {
		out[i].Statement = s
		if s.Err == nil {
			out[i].Output, out[i].Warnings = translate(s.Node, from, to)
		}
	}
	return out, nil
}
