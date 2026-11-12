// Package gen renders a syntax tree back to SQL text for a target dialect.
//
// Output is canonical: upper-case keywords, single spaces, explicit AS for
// aliases. The generator is the test oracle for the parser (parse → generate
// → parse must be stable) and the basis for transpiling between dialects.
package gen

import (
	"encoding/hex"
	"strings"

	"sqlp/ast"
	"sqlp/dialect"
	"sqlp/lexer"
)

// Generator renders nodes for one target dialect.
type Generator struct {
	d        *dialect.Dialect
	from     *dialect.Dialect // source dialect when transpiling, else nil
	sb       strings.Builder
	warnings *[]string
	shape    bool // parenthesize every nested operator; see Shape
}

// New returns a generator for dialect d.
func New(d *dialect.Dialect) *Generator { return &Generator{d: d, warnings: new([]string)} }

// Shape renders statements with the grouping of the tree made explicit: every
// nested operator, join and set operation is parenthesized. An engine that
// builds the same tree for this text and for the original proves the parser
// grouped the statement correctly.
func Shape(stmts []ast.Statement, d *dialect.Dialect) string {
	parts := make([]string, len(stmts))
	for i, s := range stmts {
		g := &Generator{d: d, warnings: new([]string), shape: true}
		g.stmt(s)
		parts[i] = g.sb.String() + ";"
	}
	return strings.Join(parts, "\n")
}

// Translate renders statements parsed in dialect from for dialect to. The
// returned warnings name every construct that could not be carried over: the
// output for such statements does not mean exactly what the input meant.
func Translate(stmts []ast.Statement, from, to *dialect.Dialect) (string, []string) {
	warnings := new([]string)
	parts := make([]string, len(stmts))
	for i, s := range stmts {
		g := &Generator{d: to, from: from, warnings: warnings}
		g.stmt(s)
		parts[i] = g.sb.String() + ";"
	}
	return strings.Join(parts, "\n"), *warnings
}

// transpiling reports whether source and target dialects differ.
func (g *Generator) transpiling() bool { return g.from != nil && g.from != g.d }

// warn records that a construct could not be carried over to the target.
func (g *Generator) warn(what string) {
	msg := what + " is not supported in dialect " + g.d.Name
	for _, w := range *g.warnings {
		if w == msg {
			return
		}
	}
	*g.warnings = append(*g.warnings, msg)
}

// Statement renders one statement without a trailing semicolon.
func Statement(s ast.Statement, d *dialect.Dialect) string {
	g := New(d)
	g.stmt(s)
	return g.sb.String()
}

// Statements renders a script, one statement per line, each ending in ';'.
func Statements(stmts []ast.Statement, d *dialect.Dialect) string {
	parts := make([]string, len(stmts))
	for i, s := range stmts {
		parts[i] = Statement(s, d) + ";"
	}
	return strings.Join(parts, "\n")
}

// Expr renders an expression.
func Expr(e ast.Expr, d *dialect.Dialect) string {
	g := New(d)
	g.expr(e)
	return g.sb.String()
}

// --- low-level writers --------------------------------------------------------

func (g *Generator) w(parts ...string) {
	for _, s := range parts {
		g.sb.WriteString(s)
	}
}

// kw writes a leading space followed by the words when cond is true.
func (g *Generator) kw(cond bool, words string) {
	if cond {
		g.w(" ", words)
	}
}

// quoted writes name in the dialect's first identifier quotes.
func (g *Generator) quoted(name string) {
	open := g.d.Lexer.IdentQuotes[:1]
	closing := map[string]string{"[": "]"}[open]
	if closing == "" {
		closing = open
	}
	if g.d.Lexer.BackslashEscapes {
		g.w(lexer.BackslashQuote(name, open[0], false))
		return
	} else {
		name = strings.ReplaceAll(name, closing, closing+closing)
	}
	g.w(open, name, closing)
}

func (g *Generator) ident(id ast.Ident) {
	switch {
	case id.Quoted:
		if id.Unicode {
			g.w("U&")
		}
		g.quoted(id.Name)
		if id.UEscape != "" {
			g.w(" UESCAPE ")
			g.str(id.UEscape)
		}
	case needsQuoting(id.Name, g.d):
		// A bare name that must be quoted for this target keeps its meaning
		// only if it is written the way the target would have folded it.
		if g.d.FoldsToLower {
			g.quoted(strings.ToLower(id.Name))
		} else {
			g.quoted(id.Name)
		}
	default:
		g.w(id.Name)
	}
}

// label writes a name in a position where the dialect allows any keyword.
func (g *Generator) label(id ast.Ident) {
	if !id.Quoted && g.d.Features.Names.KeywordLabels && !hasSpecialChars(id.Name) {
		g.w(id.Name)
		return
	}
	g.ident(id)
}

func needsQuoting(name string, d *dialect.Dialect) bool {
	return name == "" || d.IsReserved(strings.ToUpper(name)) || hasSpecialChars(name)
}

func hasSpecialChars(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		isLower := c >= 'a' && c <= 'z'
		isUpper := c >= 'A' && c <= 'Z'
		isDigit := c >= '0' && c <= '9'
		if !(isLower || isUpper || c == '_' || c == '$' || c >= 0x80 || isDigit && i > 0) {
			return true
		}
	}
	return false
}

func (g *Generator) name(n *ast.Name) {
	for i, part := range n.Parts {
		if i > 0 {
			g.w(".")
			if g.d.Features.Names.KeywordsAfterDot && !part.Quoted && !hasSpecialChars(part.Name) {
				g.w(part.Name)
			} else {
				g.label(part)
			}
			continue
		}
		g.ident(part)
	}
}

func (g *Generator) identList(ids []ast.Ident) {
	for i, id := range ids {
		if i > 0 {
			g.w(", ")
		}
		g.ident(id)
	}
}

func (g *Generator) nameList(ns []*ast.Name) {
	for i, n := range ns {
		if i > 0 {
			g.w(", ")
		}
		g.name(n)
	}
}

func (g *Generator) exprList(es []ast.Expr) {
	for i, e := range es {
		if i > 0 {
			g.w(", ")
		}
		g.expr(e)
	}
}

func (g *Generator) str(s string) {
	if g.d.Lexer.BackslashEscapes {
		g.w(lexer.BackslashQuote(s, '\'', false))
		return
	}
	g.w("'", strings.ReplaceAll(s, "'", "''"), "'")
}

// --- expressions ---------------------------------------------------------------

// postfixBase writes the operand of . or [ ], in parentheses unless it is an
// atom: (a + b).c is not a + b.c.
func (g *Generator) postfixBase(e ast.Expr) {
	if g.prec(e) < atomPrec {
		g.w("(")
		g.expr(e)
		g.w(")")
		return
	}
	g.expr(e)
}

// atomPrec is the precedence of anything that never needs parentheses.
const atomPrec = 1000

// prec returns the binding power of e in the target dialect.
func (g *Generator) prec(e ast.Expr) int {
	lookup := func(op string) int {
		if p, ok := g.d.InfixPrecedence(op); ok {
			return p
		}
		return atomPrec
	}
	switch e := e.(type) {
	case *ast.Binary:
		if strings.HasPrefix(e.Op, "OPERATOR(") {
			return g.d.CustomOpPrecedence
		}
		return lookup(strings.Fields(e.Op)[0]) // "IS NOT" -> "IS"
	case *ast.Unary:
		switch {
		case e.Op == "NOT":
			return g.d.NotPrecedence
		case e.Op == "-", e.Op == "+", !g.d.Lexer.CustomOperators:
			return g.d.UnaryMinusPrecedence
		}
		return g.d.CustomOpPrecedence
	case *ast.Is:
		return lookup("IS")
	case *ast.Between:
		return lookup("BETWEEN")
	case *ast.In:
		return lookup("IN")
	case *ast.Like:
		return lookup(strings.Fields(g.likeOp(e.Op))[0])
	case *ast.Collate:
		return lookup("COLLATE")
	case *ast.AtTimeZone:
		return lookup("AT")
	}
	return atomPrec
}

// parentPrec is prec for an expression about to print its operands: an
// operator unknown to the target imposes no constraint on them.
func (g *Generator) parentPrec(e ast.Expr) int {
	if p := g.prec(e); p != atomPrec {
		return p
	}
	return 0
}

// operand writes a child of an operator with binding power parent, adding
// parentheses when the target dialect would otherwise regroup it. The tree
// came from a parse, so this only ever fires when transpiling: PostgreSQL's
// `a || b + c` must become `a || (b + c)` in SQLite, where || binds tighter.
func (g *Generator) operand(e ast.Expr, parent int, rightSide bool) {
	p := g.prec(e)
	if g.shape && p != atomPrec || p < parent || p == parent && (rightSide || g.d.NonAssoc[parent]) {
		g.w("(")
		g.expr(e)
		g.w(")")
		return
	}
	g.expr(e)
}

func (g *Generator) expr(e ast.Expr) {
	switch e := e.(type) {
	case *ast.Name:
		if g.transpiling() && g.from.Features.DDL.WithoutRowid && !g.d.Features.DDL.WithoutRowid {
			switch strings.ToUpper(e.Parts[len(e.Parts)-1].Name) {
			case "ROWID", "_ROWID_", "OID": // tables have a rowid where WITHOUT ROWID exists
				g.warn("rowid")
			}
		}
		g.name(e)
	case *ast.Star:
		if e.Qualifier != nil {
			g.name(e.Qualifier)
			g.w(".")
		}
		g.w("*")
		if len(e.Except) > 0 {
			g.w(" EXCEPT (")
			for i, id := range e.Except {
				if i > 0 {
					g.w(", ")
				}
				g.ident(id)
			}
			g.w(")")
		}
		if len(e.Replace) > 0 {
			g.w(" REPLACE (")
			g.selectItems(e.Replace)
			g.w(")")
		}
	case *ast.Literal:
		g.literal(e)
	case *ast.TypedString:
		// INTERVAL qualifiers follow the literal: INTERVAL '1' YEAR.
		bare := *e.Type
		bare.Fields = ""
		g.dataType(&bare)
		g.w(" ")
		g.str(e.Value)
		g.kw(e.Type.Fields != "", e.Type.Fields)
	case *ast.Param:
		g.w(e.Text)
	case *ast.Default:
		g.w("DEFAULT")
	case *ast.Word:
		g.w(e.Text)
	case *ast.Field:
		g.postfixBase(e.Expr)
		g.w(".")
		if e.Star {
			g.w("*")
		} else {
			g.label(e.Name)
		}
	case *ast.Overlay:
		g.w("OVERLAY(")
		g.expr(e.Expr)
		g.w(" PLACING ")
		g.expr(e.Placing)
		g.w(" FROM ")
		g.expr(e.From)
		if e.For != nil {
			g.w(" FOR ")
			g.expr(e.For)
		}
		g.w(")")
	case *ast.Unary:
		if g.transpiling() && !isWord(e.Op) && !g.d.Lexer.CustomOperators && e.Op != "-" && e.Op != "+" && e.Op != "~" {
			g.warn("prefix operator " + e.Op)
		}
		operand := sub(g, func(c *Generator) { c.operand(e.Expr, c.parentPrec(e), false) })
		g.w(e.Op)
		if isWord(e.Op) || g.needsSpaceAfterPrefix(e.Op, operand) {
			g.w(" ")
		}
		g.w(operand)
	case *ast.Binary:
		op := e.Op
		if _, ok := g.d.Precedence[op]; op == "==" && !ok {
			op = "=" // PostgreSQL reads == as an operator nobody defined
		}
		if g.transpiling() {
			if _, ok := g.d.InfixPrecedence(strings.Fields(op)[0]); !ok {
				g.warn("operator " + op)
			}
		}
		p := g.parentPrec(e)
		g.operand(e.Left, p, false)
		g.w(" ", op, " ")
		g.operand(e.Right, p, true)
	case *ast.Quantified:
		if e.Unnest != nil {
			g.w(e.Kind, " ")
			g.funcCall(e.Unnest)
			break
		}
		g.w(e.Kind, " (")
		if e.List != nil {
			g.exprList(e.List)
		} else if sq, ok := e.Expr.(*ast.Subquery); ok {
			g.query(sq.Query)
		} else {
			g.expr(e.Expr)
		}
		g.w(")")
	case *ast.Is:
		g.operand(e.Expr, g.parentPrec(e), false)
		g.w(" IS")
		g.kw(e.Not, "NOT")
		switch e.Kind {
		case ast.IsNull:
			g.w(" NULL")
		case ast.IsTrue:
			g.w(" TRUE")
		case ast.IsFalse:
			g.w(" FALSE")
		case ast.IsUnknown:
			g.w(" UNKNOWN")
		case ast.IsDistinctFrom:
			g.w(" DISTINCT FROM ")
			g.operand(e.Other, g.parentPrec(e), true)
		case ast.IsOther:
			g.w(" ", e.Text)
		}
	case *ast.Between:
		p := g.parentPrec(e)
		g.operand(e.Expr, p, false)
		g.kw(e.Not, "NOT")
		g.w(" BETWEEN")
		if e.Symmetric && !g.d.Features.Expr.BetweenSymmetric {
			g.warn("BETWEEN SYMMETRIC")
		} else {
			g.kw(e.Symmetric, "SYMMETRIC")
		}
		g.w(" ")
		g.operand(e.Low, p, true)
		g.w(" AND ")
		g.operand(e.High, p, true)
	case *ast.In:
		g.operand(e.Expr, g.parentPrec(e), false)
		g.kw(e.Not, "NOT")
		g.w(" IN ")
		switch {
		case e.Table != nil:
			g.name(e.Table)
		case e.Func != nil:
			g.funcCall(e.Func)
		case e.Subquery != nil:
			g.w("(")
			g.query(e.Subquery)
			g.w(")")
		default:
			g.w("(")
			g.exprList(e.List)
			g.w(")")
		}
	case *ast.Raise:
		g.w("RAISE(", e.Action)
		if e.Message != nil {
			g.w(", ")
			g.expr(e.Message)
		}
		g.w(")")
	case *ast.Like:
		p := g.parentPrec(e)
		g.operand(e.Expr, p, false)
		g.kw(e.Not, "NOT")
		g.w(" ", g.likeOp(e.Op), " ")
		g.operand(e.Pattern, p, true)
		if e.Escape != nil {
			g.w(" ESCAPE ")
			g.expr(e.Escape)
		}
	case *ast.Collate:
		g.operand(e.Expr, g.parentPrec(e), false)
		g.w(" COLLATE ")
		g.collation(e.Collation)
	case *ast.AtTimeZone:
		if !g.d.Features.Expr.AtTimeZone {
			g.warn("AT TIME ZONE")
		}
		p := g.parentPrec(e)
		g.operand(e.Expr, p, false)
		g.w(" AT TIME ZONE ")
		g.operand(e.Zone, p, true)
	case *ast.Subscript:
		if g.transpiling() && !g.d.Features.Expr.ArrayLiterals {
			g.warn("array subscript") // SQLite reads [1] as a quoted name
		}
		g.postfixBase(e.Expr)
		g.w("[")
		if e.Slice {
			if e.Lower != nil {
				g.expr(e.Lower)
			}
			g.w(":")
			if e.Upper != nil {
				g.expr(e.Upper)
			}
		} else {
			g.expr(e.Index)
		}
		g.w("]")
	case *ast.Paren:
		g.w("(")
		g.expr(e.Expr)
		g.w(")")
	case *ast.Tuple:
		if e.Row {
			g.w("ROW")
		}
		g.w("(")
		g.exprList(e.Exprs)
		g.w(")")
	case *ast.Array:
		if !g.d.Features.Expr.ArrayLiterals {
			g.warn("ARRAY")
		}
		switch {
		case e.Subquery != nil:
			g.w("ARRAY(")
			g.query(e.Subquery)
			g.w(")")
		case e.Nested:
			g.w("[")
			g.exprList(e.Elems)
			g.w("]")
		case e.Of != nil:
			g.w("ARRAY<")
			g.dataType(e.Of)
			g.w(">[")
			g.exprList(e.Elems)
			g.w("]")
		default:
			g.w("ARRAY[")
			g.exprList(e.Elems)
			g.w("]")
		}
	case *ast.Cast:
		g.cast(e)
	case *ast.Struct:
		if e.Type != nil {
			g.dataType(e.Type)
		} else {
			g.w("STRUCT")
		}
		g.w("(")
		for i, f := range e.Fields {
			if i > 0 {
				g.w(", ")
			}
			g.expr(f.Expr)
			if f.As != nil {
				g.w(" AS ")
				g.label(*f.As)
			}
		}
		g.w(")")
	case *ast.Case:
		g.w("CASE")
		if e.Operand != nil {
			g.w(" ")
			g.expr(e.Operand)
		}
		for _, w := range e.Whens {
			g.w(" WHEN ")
			g.expr(w.Cond)
			g.w(" THEN ")
			g.expr(w.Result)
		}
		if e.Else != nil {
			g.w(" ELSE ")
			g.expr(e.Else)
		}
		g.w(" END")
	case *ast.Exists:
		g.w("EXISTS (")
		g.query(e.Query)
		g.w(")")
	case *ast.Subquery:
		g.w("(")
		g.query(e.Query)
		g.w(")")
	case *ast.FuncCall:
		g.funcCall(e)
	case *ast.NamedArg:
		g.ident(e.Name)
		g.w(" => ")
		g.expr(e.Value)
	case *ast.Extract:
		g.w("EXTRACT(")
		if e.FieldString != nil {
			g.literal(e.FieldString)
		} else {
			g.ident(e.Field)
		}
		if e.FieldArg != nil {
			g.w("(")
			g.ident(*e.FieldArg)
			g.w(")")
		}
		g.w(" FROM ")
		g.expr(e.Expr)
		if e.TimeZone != nil {
			g.w(" AT TIME ZONE ")
			g.expr(e.TimeZone)
		}
		g.w(")")
	case *ast.TableArg:
		g.w(e.Kind, " ")
		g.name(e.Name)
	case *ast.Interval:
		g.w("INTERVAL ")
		g.expr(e.Value)
		g.w(" ", e.Unit)
	case *ast.Substring:
		g.w("SUBSTRING(")
		g.expr(e.Expr)
		if e.Similar != nil {
			g.w(" SIMILAR ")
			g.expr(e.Similar)
			g.w(" ESCAPE ")
			g.expr(e.Escape)
		}
		if e.From != nil {
			g.w(" FROM ")
			g.expr(e.From)
		}
		if e.For != nil {
			g.w(" FOR ")
			g.expr(e.For)
		}
		g.w(")")
	case *ast.Trim:
		g.w("TRIM(")
		if e.Where != "" {
			g.w(e.Where, " ")
		}
		if e.Chars != nil {
			g.expr(e.Chars)
			g.w(" ")
		}
		if e.From {
			g.w("FROM ")
		}
		g.exprList(e.Exprs)
		g.w(")")
	case *ast.Position:
		if !g.d.Features.Expr.StandardFuncSyntax { // SQLite reads IN b as a table
			g.w("instr(")
			g.expr(e.Str)
			g.w(", ")
			g.expr(e.Substr)
			g.w(")")
			return
		}
		g.w("POSITION(")
		g.expr(e.Substr)
		g.w(" IN ")
		g.expr(e.Str)
		g.w(")")
	case *ast.GroupingSet:
		if e.Kind != "" {
			g.w(e.Kind, " ")
		}
		g.w("(")
		g.exprList(e.Items)
		g.w(")")
	default:
		g.w("<?expr>")
	}
}

// needsSpaceAfterPrefix keeps a prefix operator from fusing with what follows:
// `- -1` must not print as the comment `--1`, nor `@ -1` as the operator `@-`.
func (g *Generator) needsSpaceAfterPrefix(op, operand string) bool {
	if g.d.Lexer.CustomOperators && op != "-" && op != "+" && op != "~" {
		return true
	}
	return operand != "" && strings.IndexByte("+-*/<>=~!@#%^&|`?", operand[0]) >= 0
}

func isWord(op string) bool {
	return op != "" && (op[0] >= 'A' && op[0] <= 'Z' || op[0] >= 'a' && op[0] <= 'z')
}

// likeOp keeps the case rule of the source: SQLite's LIKE ignores the case
// of ASCII letters, PostgreSQL's LIKE does not and its ILIKE ignores all case.
func (g *Generator) likeOp(op string) string {
	switch {
	case !g.transpiling():
	case op == "ILIKE" && !g.d.Features.Expr.Ilike:
		g.warn("ILIKE (LIKE ignores the case of ASCII letters only)")
		return "LIKE"
	case op == "LIKE" && !g.from.Features.Expr.Ilike && g.d.Features.Expr.Ilike:
		return "ILIKE"
	}
	return op
}

// collation writes a collation name. The built-in collations of the dialects
// share no name (BINARY, NOCASE, RTRIM against "C", "POSIX", ...), so one
// cannot be carried over.
func (g *Generator) collation(n *ast.Name) {
	if g.transpiling() {
		parts := make([]string, len(n.Parts))
		for i, p := range n.Parts {
			parts[i] = p.Name
		}
		g.warn("collation " + strings.Join(parts, "."))
	}
	g.name(n)
}

func (g *Generator) literal(l *ast.Literal) {
	switch l.Kind {
	case ast.Null:
		g.w("NULL")
	case ast.True:
		g.w("TRUE")
	case ast.False:
		g.w("FALSE")
	case ast.Integer, ast.Float:
		g.w(l.Value)
	case ast.BytesLit:
		digits := strings.ToUpper(hex.EncodeToString([]byte(l.Value)))
		switch {
		case g.d.Lexer.PrefixedStrings:
			g.w("b", lexer.BackslashQuote(l.Value, '\'', true))
		case g.d.Lexer.BlobLiterals:
			g.w("X'", digits, "'")
		default: // the target's binary type, written in hex, as for a blob
			g.cast(&ast.Cast{Style: ast.CastDoubleColon, Type: &ast.DataType{Name: "BLOB"},
				Expr: &ast.Literal{Kind: ast.StringLit, Value: `\x` + digits}})
		}
	case ast.BlobLit:
		switch {
		case g.transpiling() && g.d.Lexer.PrefixedStrings && !g.from.Lexer.BitStrings: // X'0F' is b'\x0f'
			raw, _ := hex.DecodeString(l.Value[2 : len(l.Value)-1])
			g.literal(&ast.Literal{Kind: ast.BytesLit, Value: string(raw)})
		case !g.transpiling() || g.from.Lexer.BitStrings == g.d.Lexer.BitStrings:
			g.w(l.Value)
		case g.from.Lexer.BitStrings: // X'1F' is a bit string in PostgreSQL, a blob in SQLite
			g.warn("bit string " + l.Value)
			g.w(l.Value)
		default: // a blob becomes the target's binary type, written in hex
			hex := l.Value[2 : len(l.Value)-1]
			g.cast(&ast.Cast{Style: ast.CastDoubleColon, Type: &ast.DataType{Name: "BLOB"},
				Expr: &ast.Literal{Kind: ast.StringLit, Value: `\x` + hex}})
		}
	case ast.StringLit:
		if l.Unicode {
			g.w("U&")
			g.str(l.Value)
			if l.UEscape != "" {
				g.w(" UESCAPE ")
				g.str(l.UEscape)
			}
			return
		}
		if l.Escape {
			if g.d.Lexer.EscapeStrings {
				g.w("E'", l.Value, "'")
			} else {
				g.str(lexer.Unescape(l.Value))
			}
			return
		}
		g.str(l.Value)
	}
}

func (g *Generator) cast(c *ast.Cast) {
	switch {
	case c.Style == ast.CastDoubleColon && g.d.Features.Expr.DoubleColonCast:
		g.expr(c.Expr)
		g.w("::")
		g.dataType(c.Type)
	default:
		g.w(map[ast.CastStyle]string{ast.CastTry: "TRY_CAST(", ast.CastSafe: "SAFE_CAST("}[c.Style])
		if c.Style != ast.CastTry && c.Style != ast.CastSafe {
			g.w("CAST(")
		}
		g.expr(c.Expr)
		g.w(" AS")
		if c.Type.Name != "" { // SQLite allows CAST(x AS)
			g.w(" ")
			g.dataType(c.Type)
		}
		if c.Format != nil {
			g.w(" FORMAT ")
			g.expr(c.Format)
		}
		if c.TimeZone != nil {
			g.w(" AT TIME ZONE ")
			g.expr(c.TimeZone)
		}
		g.w(")")
	}
}

// typeMap renames source type names (upper-cased) for a target dialect.
var typeMap = map[string]map[string]string{
	"sqlite": {
		"INT": "INTEGER", "INT4": "INTEGER", "INT8": "BIGINT", "INT2": "SMALLINT", "SERIAL": "INTEGER",
		"BIGSERIAL": "INTEGER", "FLOAT4": "REAL", "FLOAT8": "REAL", "DOUBLE PRECISION": "REAL",
		"BOOL": "BOOLEAN", "BYTEA": "BLOB", "TIMESTAMPTZ": "TIMESTAMP", "JSONB": "TEXT",
		"UUID": "TEXT", "CHARACTER VARYING": "VARCHAR",
	},
	"postgres": {
		"BLOB": "BYTEA", "DATETIME": "TIMESTAMP", "DOUBLE": "DOUBLE PRECISION",
	},
}

// lossyTypes are the typeMap entries that lose part of the meaning.
var lossyTypes = map[string]string{
	"SERIAL": "SERIAL (its sequence default)", "BIGSERIAL": "BIGSERIAL (its sequence default)",
	"TIMESTAMPTZ": "TIMESTAMPTZ (its time zone)", "JSONB": "JSONB (as text)", "UUID": "UUID (as text)",
}

func (g *Generator) dataType(dt *ast.DataType) {
	name := dt.Name
	if g.transpiling() {
		if mapped, ok := typeMap[g.d.Name][strings.ToUpper(name)]; ok {
			name = mapped
			if what, lossy := lossyTypes[strings.ToUpper(dt.Name)]; lossy {
				g.warn(what)
			}
		}
		if dt.TimeZone == ast.TZWith && !g.d.Features.Expr.AtTimeZone {
			g.warn("WITH TIME ZONE")
		}
		if len(dt.ArrayBounds) > 0 && !g.d.Features.Expr.ArrayLiterals {
			g.warn("array type " + dt.Name)
		}
	}
	if dt.Setof {
		g.w("SETOF ")
	}
	if dt.Angle {
		g.w(name, "<")
		if dt.Of != nil {
			g.dataType(dt.Of)
		}
		for i, f := range dt.Struct {
			if i > 0 {
				g.w(", ")
			}
			if f.Name.Name != "" {
				g.label(f.Name)
				g.w(" ")
			}
			g.dataType(f.Type)
		}
		g.w(">")
		return
	}
	g.w(name)
	if len(dt.Args) > 0 {
		g.w("(")
		g.exprList(dt.Args)
		g.w(")")
	}
	switch dt.TimeZone {
	case ast.TZWith:
		g.w(" WITH TIME ZONE")
	case ast.TZWithout:
		g.w(" WITHOUT TIME ZONE")
	}
	g.kw(dt.Fields != "", dt.Fields)
	for _, bound := range dt.ArrayBounds {
		g.w("[", bound, "]")
	}
}

// funcName writes a function name. Words such as LEFT or CURRENT_USER are
// reserved as column names but are valid here without quotes.
func (g *Generator) funcName(f *ast.FuncCall) {
	if len(f.Name.Parts) == 1 && !f.Name.Parts[0].Quoted {
		word := strings.ToUpper(f.Name.Parts[0].Name)
		if f.NoParens || g.d.FuncNameKeywords[word] || g.d.BareFuncs[word] || word == "UNNEST" && g.d.Features.Query.Unnest {
			g.w(f.Name.Parts[0].Name)
			return
		}
	}
	g.name(f.Name)
}

func (g *Generator) funcCall(f *ast.FuncCall) {
	if g.transpiling() && len(f.Name.Parts) == 1 {
		name := strings.ToUpper(f.Name.Parts[0].Name)
		if g.from.HasFunction(name) && !g.d.HasFunction(name) {
			g.warn("function " + strings.ToLower(name))
		}
	}
	g.funcName(f)
	if f.NoParens {
		return
	}
	g.w("(")
	if f.RawArgs != nil {
		g.w(*f.RawArgs)
	}
	g.kwInline(f.Distinct, "DISTINCT ")
	g.exprList(f.Args)
	if f.Nulls != "" {
		g.w(" ", f.Nulls, " NULLS")
	}
	if f.Where != nil {
		g.w(" WHERE ")
		g.expr(f.Where)
	}
	if f.Having != nil {
		g.w(map[bool]string{true: " HAVING MAX ", false: " HAVING MIN "}[f.Having.Max])
		g.expr(f.Having.Expr)
	}
	if len(f.OrderBy) > 0 {
		g.kwInline(len(f.Args) > 0, " ")
		g.w("ORDER BY ")
		g.orderItems(f.OrderBy)
	}
	if f.Limit != nil {
		g.w(" LIMIT ")
		g.expr(f.Limit)
	}
	g.w(")")
	if len(f.WithinGroup) > 0 {
		g.w(" WITHIN GROUP (ORDER BY ")
		g.orderItems(f.WithinGroup)
		g.w(")")
	}
	if f.Filter != nil {
		g.w(" FILTER (WHERE ")
		g.expr(f.Filter)
		g.w(")")
	}
	if f.Over != nil {
		g.w(" OVER ")
		if f.Over.NameOnly {
			g.ident(*f.Over.Name)
		} else {
			g.windowSpec(f.Over)
		}
	}
}

func (g *Generator) kwInline(cond bool, s string) {
	if cond {
		g.w(s)
	}
}

func (g *Generator) windowSpec(ws *ast.WindowSpec) {
	g.w("(")
	var parts []string
	if ws.Name != nil {
		parts = append(parts, sub(g, func(g *Generator) { g.ident(*ws.Name) }))
	}
	if len(ws.PartitionBy) > 0 {
		parts = append(parts, "PARTITION BY "+sub(g, func(g *Generator) { g.exprList(ws.PartitionBy) }))
	}
	if len(ws.OrderBy) > 0 {
		parts = append(parts, "ORDER BY "+sub(g, func(g *Generator) { g.orderItems(ws.OrderBy) }))
	}
	if ws.Frame != nil {
		parts = append(parts, sub(g, func(g *Generator) { g.frame(ws.Frame) }))
	}
	g.w(strings.Join(parts, " "), ")")
}

// sub renders with a child generator and returns the text.
func sub(g *Generator, f func(*Generator)) string {
	c := &Generator{d: g.d, from: g.from, warnings: g.warnings, shape: g.shape}
	f(c)
	return c.sb.String()
}

func (g *Generator) frame(f *ast.Frame) {
	g.w(f.Units, " ")
	if f.End != nil {
		g.w("BETWEEN ")
		g.frameBound(f.Start)
		g.w(" AND ")
		g.frameBound(f.End)
	} else {
		g.frameBound(f.Start)
	}
	g.kw(f.Exclude != "", "EXCLUDE "+f.Exclude)
}

func (g *Generator) frameBound(b *ast.FrameBound) {
	switch b.Kind {
	case ast.CurrentRow:
		g.w("CURRENT ROW")
	case ast.UnboundedPreceding:
		g.w("UNBOUNDED PRECEDING")
	case ast.UnboundedFollowing:
		g.w("UNBOUNDED FOLLOWING")
	case ast.Preceding:
		g.expr(b.Expr)
		g.w(" PRECEDING")
	case ast.Following:
		g.expr(b.Expr)
		g.w(" FOLLOWING")
	}
}

func (g *Generator) orderItems(items []*ast.OrderItem) {
	for i, it := range items {
		if i > 0 {
			g.w(", ")
		}
		g.expr(it.Expr)
		g.kw(it.Direction != "", it.Direction)
		g.kw(it.Using != "", "USING "+it.Using)
		g.kw(it.Nulls != "", "NULLS "+it.Nulls)
	}
}

// --- queries ---------------------------------------------------------------------

func (g *Generator) query(q *ast.Query) {
	if q.With != nil {
		g.with(q.With)
		g.w(" ")
	}
	g.body(q.Body)
	if !g.d.Features.Query.ValuesTail && g.from != nil && endsWithValues(q.Body) && (len(q.OrderBy) > 0 || q.Limit != nil) {
		g.warn("ORDER BY or LIMIT after VALUES")
	}
	if len(q.OrderBy) > 0 {
		g.w(" ORDER BY ")
		g.orderItems(q.OrderBy)
	}
	g.limit(q.Limit)
	for _, lk := range q.Locking {
		if !g.d.Features.Query.Locking {
			g.warn("FOR " + lk.Strength)
			continue
		}
		g.w(" FOR ", lk.Strength)
		if len(lk.Of) > 0 {
			g.w(" OF ")
			g.nameList(lk.Of)
		}
		g.kw(lk.Wait != "", lk.Wait)
	}
}

func isNegativeInteger(e ast.Expr) bool {
	u, ok := e.(*ast.Unary)
	if !ok || u.Op != "-" {
		return false
	}
	lit, ok := u.Expr.(*ast.Literal)
	return ok && lit.Kind == ast.Integer && strings.Trim(lit.Value, "0_") != ""
}

func (g *Generator) limit(l *ast.Limit) {
	if l == nil {
		return
	}
	switch {
	case l.Fetch && g.d.Features.Query.FetchFirst:
		if l.Offset != nil {
			g.w(" OFFSET ")
			g.expr(l.Offset)
			g.w(" ROWS")
		}
		g.w(" FETCH FIRST ")
		if l.Count != nil {
			g.expr(l.Count)
			g.w(" ")
		}
		g.w("ROWS")
		if l.WithTies {
			g.w(" WITH TIES")
		} else {
			g.w(" ONLY")
		}
		return
	case l.All, g.transpiling() && isNegativeInteger(l.Count):
		// A negative limit is none in SQLite and an error in PostgreSQL.
		if g.d.Features.Query.OffsetWithoutLimit {
			g.w(" LIMIT ALL")
		} else {
			g.w(" LIMIT -1")
		}
	case l.Count != nil:
		g.w(" LIMIT ")
		g.expr(l.Count)
	case l.Fetch: // FETCH FIRST ROWS ONLY → LIMIT 1
		g.w(" LIMIT 1")
	}
	if l.Offset != nil {
		g.w(" OFFSET ")
		g.expr(l.Offset)
	}
}

func (g *Generator) with(w *ast.With) {
	g.w("WITH")
	g.kw(w.Recursive, "RECURSIVE")
	for i, cte := range w.CTEs {
		if i > 0 {
			g.w(",")
		}
		g.w(" ")
		g.ident(cte.Name)
		if len(cte.Columns) > 0 {
			g.w("(")
			g.identList(cte.Columns)
			g.w(")")
		}
		g.w(" AS ")
		if cte.Materialized != nil {
			if !*cte.Materialized {
				g.w("NOT ")
			}
			g.w("MATERIALIZED ")
		}
		g.w("(")
		if cte.DML != nil {
			g.stmt(cte.DML)
		} else {
			g.query(cte.Query)
		}
		g.w(")")
		if s := cte.Search; s != nil {
			if s.Breadth {
				g.w(" SEARCH BREADTH FIRST BY ")
			} else {
				g.w(" SEARCH DEPTH FIRST BY ")
			}
			g.identList(s.By)
			g.w(" SET ")
			g.ident(s.Set)
		}
		if c := cte.Cycle; c != nil {
			g.w(" CYCLE ")
			g.identList(c.Columns)
			g.w(" SET ")
			g.ident(c.Set)
			if c.To != nil {
				g.w(" TO ")
				g.expr(c.To)
				g.w(" DEFAULT ")
				g.expr(c.Default)
			}
			g.w(" USING ")
			g.ident(c.Using)
		}
	}
}

func (g *Generator) body(b ast.QueryBody) {
	switch b := b.(type) {
	case *ast.Select:
		g.sel(b)
	case *ast.SetOp:
		g.setOperand(b.Left)
		g.w(" ", [...]string{"UNION", "INTERSECT", "EXCEPT"}[b.Op])
		g.kw(b.All, "ALL")
		g.kw(!b.All && g.d.Features.Query.SetQuantifierRequired, "DISTINCT")
		g.w(" ")
		g.setOperand(b.Right)
	case *ast.Values:
		g.values(b)
	case *ast.ParenQuery:
		g.w("(")
		g.query(b.Query)
		g.w(")")
	case *ast.TableBody:
		g.w("TABLE ")
		g.name(b.Name)
	}
}

func endsWithValues(b ast.QueryBody) bool {
	switch b := b.(type) {
	case *ast.Values:
		return true
	case *ast.SetOp:
		return endsWithValues(b.Right)
	}
	return false
}

func (g *Generator) setOperand(b ast.QueryBody) {
	if _, nested := b.(*ast.SetOp); nested && g.shape {
		g.w("(")
		g.body(b)
		g.w(")")
		return
	}
	g.body(b)
}

func (g *Generator) values(v *ast.Values) {
	g.w("VALUES ")
	for i, row := range v.Rows {
		if i > 0 {
			g.w(", ")
		}
		g.w("(")
		g.exprList(row)
		g.w(")")
	}
}

func (g *Generator) sel(s *ast.Select) {
	g.w("SELECT")
	switch s.Distinct {
	case ast.DistinctAll:
		g.w(" ALL")
	case ast.Distinct:
		g.w(" DISTINCT")
	case ast.DistinctOn:
		g.w(" DISTINCT")
		if g.d.Features.Query.DistinctOn {
			g.w(" ON (")
			g.exprList(s.DistinctOn)
			g.w(")")
		} else {
			g.warn("DISTINCT ON")
		}
	}
	if s.As != "" {
		g.w(" AS ", s.As)
	}
	if len(s.Items) > 0 {
		g.w(" ")
		g.selectItems(s.Items)
	}
	if s.Into != nil {
		g.w(" INTO")
		g.kw(s.Into.Temp, "TEMPORARY")
		g.kw(s.Into.Unlogged, "UNLOGGED")
		g.w(" ")
		g.name(s.Into.Name)
	}
	if len(s.From) > 0 {
		g.w(" FROM ")
		g.tableExprs(s.From)
	}
	if s.Where != nil {
		g.w(" WHERE ")
		g.expr(s.Where)
	}
	if s.GroupBy != nil {
		g.w(" GROUP BY")
		g.kw(s.GroupBy.Distinct, "DISTINCT")
		g.kw(s.GroupBy.All, "ALL")
		if len(s.GroupBy.Items) > 0 {
			g.w(" ")
			g.exprList(s.GroupBy.Items)
		}
	}
	if s.Having != nil {
		g.w(" HAVING ")
		g.expr(s.Having)
	}
	if len(s.Windows) > 0 {
		g.w(" WINDOW ")
		for i, nw := range s.Windows {
			if i > 0 {
				g.w(", ")
			}
			g.ident(nw.Name)
			g.w(" AS ")
			g.windowSpec(nw.Spec)
		}
	}
	if s.Qualify != nil {
		g.w(" QUALIFY ")
		g.expr(s.Qualify)
	}
}

func (g *Generator) selectItems(items []*ast.SelectItem) {
	for i, it := range items {
		if i > 0 {
			g.w(", ")
		}
		g.expr(it.Expr)
		if it.Alias != nil {
			g.w(" AS ")
			g.label(*it.Alias)
		}
	}
}

func (g *Generator) tableExprs(ts []ast.TableExpr) {
	for i, t := range ts {
		if i > 0 {
			g.w(", ")
		}
		g.tableExpr(t)
	}
}

func (g *Generator) tableExpr(t ast.TableExpr) {
	switch t := t.(type) {
	case *ast.TableRef:
		g.kwInline(t.Only, "ONLY ")
		g.name(t.Name)
		g.alias(t.Alias)
		if t.SystemTime != nil {
			g.w(" FOR SYSTEM_TIME AS OF ")
			g.expr(t.SystemTime)
		}
		if t.Sample != nil {
			g.w(" TABLESAMPLE ")
			g.name(t.Sample.Method)
			g.w("(")
			g.exprList(t.Sample.Args)
			g.kw(t.Sample.Percent, "PERCENT")
			g.w(")")
			if t.Sample.Repeatable != nil {
				g.w(" REPEATABLE (")
				g.expr(t.Sample.Repeatable)
				g.w(")")
			}
		}
		g.indexed(t.Indexed)
	case *ast.SubqueryRef:
		g.kwInline(t.Lateral, "LATERAL ")
		g.w("(")
		g.query(t.Query)
		g.w(")")
		g.alias(t.Alias)
	case *ast.FuncRef:
		g.kwInline(t.Lateral, "LATERAL ")
		switch {
		case t.Expr != nil:
			g.expr(t.Expr)
		case t.Func != nil:
			g.funcCall(t.Func)
		default:
			g.w("ROWS FROM (")
			for i, f := range t.RowsFrom {
				if i > 0 {
					g.w(", ")
				}
				g.funcCall(f)
				if defs := t.RowsFromColumns[i]; len(defs) > 0 {
					g.w(" AS (")
					for j, c := range defs {
						if j > 0 {
							g.w(", ")
						}
						g.columnDef(c)
					}
					g.w(")")
				}
			}
			g.w(")")
		}
		if t.Ordinality && !g.d.Features.Query.WithOrdinality {
			g.warn("WITH ORDINALITY")
		} else {
			g.kw(t.Ordinality, "WITH ORDINALITY")
		}
		g.alias(t.Alias)
		if t.Offset {
			g.w(" WITH OFFSET")
			if t.OffsetAlias != nil {
				g.w(" AS ")
				g.ident(*t.OffsetAlias)
			}
		}
	case *ast.Join:
		g.joinOperand(t.Left)
		if t.Comma {
			g.w(", ")
		} else {
			kind := t.Kind
			if t.Natural && kind == ast.CrossJoin && g.from != nil && !g.d.Features.Query.FreeJoinKeywords {
				kind = ast.InnerJoin // SQLite's NATURAL CROSS JOIN is a natural inner join
			}
			g.kw(t.Natural, "NATURAL")
			g.w(" ", [...]string{"JOIN", "LEFT JOIN", "RIGHT JOIN", "FULL JOIN", "CROSS JOIN"}[kind], " ")
		}
		g.joinOperand(t.Right)
		if t.On != nil {
			g.w(" ON ")
			g.expr(t.On)
		}
		if len(t.Using) > 0 {
			g.w(" USING (")
			g.identList(t.Using)
			g.w(")")
			if t.UsingAlias != nil {
				g.w(" AS ")
				g.ident(*t.UsingAlias)
			}
		}
	case *ast.ParenTable:
		if g.from != nil && g.d.Features.Query.ParenTableIsJoin && !ast.IsJoinTree(t.Expr) {
			if t.Alias == nil && !isCommaJoin(t.Expr) {
				g.tableExpr(t.Expr)
				return
			}
			g.warn("parenthesized table")
		}
		g.w("(")
		g.tableExpr(t.Expr)
		g.w(")")
		g.alias(t.Alias)
	}
}

func (g *Generator) joinOperand(t ast.TableExpr) {
	if j, nested := t.(*ast.Join); nested && g.shape && !j.Comma {
		g.w("(")
		g.tableExpr(t)
		g.w(")")
		return
	}
	g.tableExpr(t)
}

func isCommaJoin(t ast.TableExpr) bool {
	j, ok := t.(*ast.Join)
	return ok && j.Comma
}

func (g *Generator) alias(a *ast.Alias) {
	if a == nil {
		return
	}
	g.w(" AS ")
	if a.Name.Name != "" || a.Name.Quoted {
		g.ident(a.Name)
	}
	if len(a.Columns) > 0 {
		g.w("(")
		for i, c := range a.Columns {
			if i > 0 {
				g.w(", ")
			}
			g.ident(c.Name)
			if c.Type != nil {
				g.w(" ")
				g.dataType(c.Type)
			}
		}
		g.w(")")
	}
}

func (g *Generator) indexed(ix *ast.Indexed) {
	switch {
	case ix == nil:
	case ix.Not:
		g.w(" NOT INDEXED")
	default:
		g.w(" INDEXED BY ")
		g.ident(ix.Name)
	}
}
