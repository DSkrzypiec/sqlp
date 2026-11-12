package parser

import (
	"strings"

	"sqlp/ast"
	"sqlp/dialect"
	"sqlp/lexer"
)

// parseExpr parses a full expression.
func (p *Parser) parseExpr() ast.Expr {
	if p.bExpr { // a bracketed expression inside a b_expr is a full one again
		p.bExpr = false
		defer func() { p.bExpr = true }()
	}
	return p.parseExprPrec(0)
}

// parseBExpr parses PostgreSQL's b_expr, the expression of a column DEFAULT
// and of POSITION(a IN b). Outside brackets it has no AND, OR, NOT, IN, LIKE,
// BETWEEN, IS NULL, AT TIME ZONE or COLLATE, but it has comparisons and
// IS [NOT] DISTINCT FROM and IS [NOT] DOCUMENT.
func (p *Parser) parseBExpr() ast.Expr {
	saved := p.bExpr
	p.bExpr = true
	defer func() { p.bExpr = saved }()
	return p.parseExprPrec(0)
}

// inBExpr reports whether the infix operator op at the current token may
// continue a b_expr.
func (p *Parser) inBExpr(op string) bool {
	switch {
	case op == "IS":
		return p.isKwAt(1, "DISTINCT", "DOCUMENT") || p.isKwAt(1, "NOT") && p.isKwAt(2, "DISTINCT", "DOCUMENT")
	case op == "OPERATOR":
		return true
	}
	return op != "" && !(op[0] >= 'A' && op[0] <= 'Z') // symbolic operators, ::, [ and .
}

// parseExprPrec is the precedence-climbing loop: parse a prefix/primary
// expression, then keep absorbing infix operators that bind tighter than
// minPrec. All operators are treated as left-associative.
func (p *Parser) parseExprPrec(minPrec int) ast.Expr {
	p.enterDepth()
	defer p.exitDepth()
	return p.continueExprPrec(p.parsePrefix(), minPrec)
}

// continueExprPrec runs the infix loop of parseExprPrec starting from an
// already-parsed left operand, so a leading expression parsed for other
// reasons (see tryParenBodyQuery) does not need to be parsed again.
func (p *Parser) continueExprPrec(left ast.Expr, minPrec int) ast.Expr {
	for {
		op, prec, ok := p.infixOp()
		if !ok || prec <= minPrec || (op == "." || op == "[") && !canIndirect(left) && !p.d.Features.Expr.FreePostfix || p.bExpr && !p.inBExpr(op) {
			return left
		}
		// `a IN (1) IN (2)` is legal because IN ends with its own parenthesis;
		// every other non-associative operator ends with an open expression.
		isIn := op == "IN" || op == "NOT" && p.isKwAt(1, "IN")
		left = p.parseInfix(left, op, prec)
		if p.d.NonAssoc[prec] && !isIn {
			if _, next, ok := p.infixOp(); ok && next == prec {
				p.unexpected("end of non-associative comparison")
			}
		}
	}
}

func (p *Parser) parseExprList() []ast.Expr {
	exprs := []ast.Expr{p.parseExpr()}
	for p.acceptOp(",") {
		exprs = append(exprs, p.parseExpr())
	}
	return exprs
}

// infixOp classifies the current token as an infix operator. For keyword
// operators the returned op is the upper-cased keyword; NOT BETWEEN / NOT IN
// / NOT LIKE are returned as "NOT" with the precedence of the inner operator.
func (p *Parser) infixOp() (op string, prec int, ok bool) {
	t := p.peek()
	switch t.Kind {
	case lexer.Op:
		switch t.Text {
		case "(", ")", ",", ";", "]", "=>":
			return "", 0, false
		case "::":
			if !p.d.Features.Expr.DoubleColonCast {
				return "", 0, false
			}
		case "[":
			if !p.d.Features.Expr.ArrayLiterals {
				return "", 0, false
			}
		}
		prec, ok = p.d.InfixPrecedence(t.Text)
		return t.Text, prec, ok
	case lexer.Ident:
		kw := t.Value
		if p.d.Features.Names.KeywordLabels && p.d.Precedence[kw] != 0 && !p.d.AsOnlyLabels[kw] && p.noOperandAt(1) {
			return "", 0, false // `SELECT a is, b`: a bare label, not an operator
		}
		switch kw {
		case "ESCAPE": // only valid inside LIKE, handled there
			return "", 0, false
		case "NOT":
			inner := p.peekN(1)
			if inner.Kind != lexer.Ident {
				return "", 0, false
			}
			if inner.Value == "NULL" && p.d.Features.Expr.NotNullPostfix {
				return "NOT NULL", p.d.Precedence["NOTNULL"], true
			}
			switch inner.Value {
			case "BETWEEN", "IN", "LIKE", "ILIKE", "SIMILAR", "GLOB", "REGEXP", "MATCH":
				prec, ok = p.d.InfixPrecedence(inner.Value)
				return "NOT", prec, ok
			}
			return "", 0, false
		case "AT":
			if !p.isKwSeq("AT", "TIME", "ZONE") {
				return "", 0, false
			}
		case "SIMILAR":
			if !p.isKwAt(1, "TO") {
				return "", 0, false
			}
		case "OPERATOR":
			if !p.isOpAt(1, "(") {
				return "", 0, false
			}
		}
		prec, ok = p.d.Precedence[kw]
		return kw, prec, ok
	}
	return "", 0, false
}

// canIndirect reports whether e may be followed by [i] or .field, which
// gram.y only allows on column references, parameters and parenthesized forms.
func canIndirect(e ast.Expr) bool {
	switch e.(type) {
	case *ast.Name, *ast.Paren, *ast.Subquery, *ast.Param, *ast.Subscript, *ast.Field:
		return true
	}
	return false
}

// noOperandAt reports whether the token at offset n can never start an operand.
func (p *Parser) noOperandAt(n int) bool {
	t := p.peekN(n)
	switch t.Kind {
	case lexer.EOF:
		return true
	case lexer.Op:
		return t.Text == "," || t.Text == ")" || t.Text == ";"
	case lexer.Ident:
		return t.Value == "FROM" || t.Value == "WHERE" || t.Value == "GROUP" || t.Value == "ORDER" ||
			t.Value == "LIMIT" || t.Value == "HAVING" || t.Value == "UNION"
	}
	return false
}

func (p *Parser) parseInfix(left ast.Expr, op string, prec int) ast.Expr {
	switch op {
	case "NOT":
		p.next()
		return p.parseNegatable(left, p.peek().Value, prec, true)
	case "NOT NULL":
		p.next()
		p.next()
		return &ast.Is{Expr: left, Not: true, Kind: ast.IsNull}
	case "OPERATOR":
		name := p.parseOperatorName()
		return &ast.Binary{Op: name, Left: left, Right: p.parseExprPrec(prec)}
	case ".":
		p.next()
		if p.acceptOp("*") {
			return &ast.Field{Expr: left, Star: true}
		}
		return &ast.Field{Expr: left, Name: p.parseAnyIdent()}
	case "BETWEEN", "IN", "LIKE", "ILIKE", "SIMILAR", "GLOB", "REGEXP", "MATCH":
		return p.parseNegatable(left, op, prec, false)
	case "IS":
		return p.parseIs(left, prec)
	case "ISNULL", "NOTNULL":
		p.next()
		return &ast.Is{Expr: left, Not: op == "NOTNULL", Kind: ast.IsNull}
	case "COLLATE":
		p.next()
		if p.d.NotAlias[p.peek().Value] {
			p.unexpected("collation name")
		}
		return &ast.Collate{Expr: left, Collation: p.parseName()}
	case "AT":
		p.expectKw("AT", "TIME", "ZONE")
		return &ast.AtTimeZone{Expr: left, Zone: p.parseExprPrec(prec)}
	case "::":
		p.next()
		return &ast.Cast{Expr: left, Type: p.parseType(), Style: ast.CastDoubleColon}
	case "[":
		return p.parseSubscript(left)
	}
	p.next()
	if q := p.tryQuantified(op); q != nil {
		return &ast.Binary{Op: op, Left: left, Right: q}
	}
	return &ast.Binary{Op: op, Left: left, Right: p.parseExprPrec(prec)}
}

// builtinInfixOnly lists the operators PostgreSQL's grammar has as dedicated
// tokens, which can never be prefix operators. Anything else is a generic Op.
var builtinInfixOnly = map[string]bool{
	"=": true, "<": true, ">": true, "<=": true, ">=": true, "<>": true, "!=": true,
	"*": true, "/": true, "%": true, "^": true, "=>": true,
}

// parseOperatorName parses OPERATOR(schema.+) and returns it as written.
func (p *Parser) parseOperatorName() string {
	p.expectKw("OPERATOR")
	p.expectOp("(")
	var sb strings.Builder
	sb.WriteString("OPERATOR(")
	for k := p.peek().Kind; (k == lexer.Ident || k == lexer.QuotedIdent) && p.isOpAt(1, "."); k = p.peek().Kind {
		sb.WriteString(p.next().Text)
		sb.WriteString(".")
		p.next()
	}
	op := p.peek()
	if op.Kind != lexer.Op {
		p.unexpected("operator")
	}
	p.next()
	sb.WriteString(op.Text)
	sb.WriteString(")")
	p.expectOp(")")
	return sb.String()
}

// tryQuantified parses ANY/ALL/SOME (...), which may follow any operator in
// dialects with custom operators and comparisons elsewhere.
func (p *Parser) tryQuantified(op string) *ast.Quantified {
	if !p.isKw("ANY", "ALL", "SOME") || !p.isOpAt(1, "(") || !p.d.Lexer.CustomOperators || p.bExpr {
		return nil
	}
	if dialect.IsOperatorText(op) || strings.HasPrefix(op, "OPERATOR(") {
		kind := p.next().Value
		p.expectOp("(")
		var inner ast.Expr
		switch q, pending := p.tryParenBodyQuery(); {
		case q != nil:
			inner = &ast.Subquery{Query: q}
		case pending != nil:
			inner = pending
		default:
			inner = p.parseExpr()
		}
		p.expectOp(")")
		return &ast.Quantified{Kind: kind, Expr: inner}
	}
	return nil
}

// tryParenBodyQuery parses a query inside parentheses already opened.
// ((SELECT 1), 2) starts like a query but is a list: a leading '(' is
// ambiguous, since the nested '(query)' might be the whole body or just its
// first element, while a leading SELECT/VALUES/WITH/TABLE is committed, which
// keeps its error messages precise. The ambiguous case is resolved by parsing
// the query once and, if it isn't the whole body, reinterpreting it as a
// Subquery and continuing from there as an expression — never by
// backtracking and reparsing, which costs time proportional to the call
// stack depth at every nesting level and made deeply nested input exponential
// (then quadratic even memoized; see AGENT_TODO.md).
func (p *Parser) tryParenBodyQuery() (query *ast.Query, pending ast.Expr) {
	if !p.isQueryStart() {
		return nil, nil
	}
	if !p.isOp("(") {
		return p.parseQuery(), nil
	}
	if p.bExpr { // inside brackets: a full expression again
		p.bExpr = false
		defer func() { p.bExpr = true }()
	}
	q := p.parseQuery()
	if p.isOp(")") {
		return q, nil
	}
	return nil, p.continueExprPrec(&ast.Subquery{Query: unwrapParenQuery(q)}, 0)
}

// unwrapParenQuery undoes a single incidental ast.ParenQuery layer that
// parseQuery added by consuming a leading '(' as its own grouping (see
// parseQueryPrimary). That reading only turns out to be wrong once the
// caller discovers q is not the whole parenthesized body: q is about to be
// wrapped in a Subquery/SubqueryRef that already supplies the paren pair, so
// a bare ParenQuery here is redundant rather than a grouping actually
// written in the input (which would come with its own decorations, or
// combine with a set operator, and must be preserved).
func unwrapParenQuery(q *ast.Query) *ast.Query {
	pq, ok := q.Body.(*ast.ParenQuery)
	if !ok || q.With != nil || q.OrderBy != nil || q.Limit != nil || q.Locking != nil {
		return q
	}
	return pq.Query
}

// parseNegatable handles BETWEEN, IN and the LIKE family, with the current
// token being the operator keyword.
// finishRow parses `row OVERLAPS row`. The grammar makes it a whole operand
// rather than an infix operator, so 1 / (a, b) OVERLAPS (c, d) is valid.
func (p *Parser) finishRow(row *ast.Tuple) ast.Expr {
	if !p.d.Features.Expr.Overlaps || !p.isKw("OVERLAPS") {
		return row
	}
	pos := p.next().Pos
	right := p.parsePrimary()
	if !isPairRow(row) || !isPairRow(right) {
		p.errorf(pos, "OVERLAPS needs a row of two values on each side")
	}
	return &ast.Binary{Op: "OVERLAPS", Left: row, Right: right}
}

func isPairRow(e ast.Expr) bool {
	t, ok := e.(*ast.Tuple)
	return ok && len(t.Exprs) == 2
}

func (p *Parser) parseNegatable(left ast.Expr, op string, prec int, not bool) ast.Expr {
	p.next()
	switch op {
	case "BETWEEN":
		sym := p.d.Features.Expr.BetweenSymmetric && p.acceptKw("SYMMETRIC")
		if !sym && p.d.Features.Expr.BetweenSymmetric {
			p.acceptKw("ASYMMETRIC")
		}
		low := p.parseExprPrec(prec)
		p.expectKw("AND")
		high := p.parseExprPrec(prec)
		return &ast.Between{Expr: left, Not: not, Symmetric: sym, Low: low, High: high}
	case "IN":
		in := &ast.In{Expr: left, Not: not}
		if p.d.Features.Query.InTable && !p.isOp("(") {
			name := p.parseName()
			if p.isOp("(") {
				in.Func = p.parseFuncCall(name)
			} else {
				in.Table = name
			}
			return in
		}
		p.expectOp("(")
		switch q, pending := p.tryParenBodyQuery(); {
		case q != nil:
			in.Subquery = q
		case pending != nil:
			list := []ast.Expr{pending}
			for p.acceptOp(",") {
				list = append(list, p.parseExpr())
			}
			in.List = list
		case p.isOp(")") && p.d.Features.Query.InTable:
			in.List = []ast.Expr{}
		default:
			in.List = p.parseExprList()
		}
		p.expectOp(")")
		return in
	case "SIMILAR":
		p.expectKw("TO")
		op = "SIMILAR TO"
	}
	var pattern ast.Expr
	if q := p.tryQuantifiedLike(); q != nil {
		pattern = q
	} else {
		pattern = p.parseExprPrec(prec)
	}
	like := &ast.Like{Expr: left, Not: not, Op: op, Pattern: pattern}
	if p.d.Features.Expr.LikeEscape && p.acceptKw("ESCAPE") {
		like.Escape = p.parseExprPrec(p.d.Precedence["ESCAPE"])
	}
	return like
}

// tryQuantifiedLike parses LIKE ANY (...) and friends.
func (p *Parser) tryQuantifiedLike() *ast.Quantified {
	if !p.d.Lexer.CustomOperators {
		return nil
	}
	return p.tryQuantified("=")
}

func (p *Parser) parseIs(left ast.Expr, prec int) ast.Expr {
	p.expectKw("IS")
	not := p.acceptKw("NOT")
	is := &ast.Is{Expr: left, Not: not}
	operand := p.isConstantOperand(prec)
	switch {
	case !operand && p.acceptKw("NULL"):
		is.Kind = ast.IsNull
	case !operand && p.acceptKw("TRUE"):
		is.Kind = ast.IsTrue
	case !operand && p.acceptKw("FALSE"):
		is.Kind = ast.IsFalse
	case p.d.Features.Expr.IsUnknown && p.acceptKw("UNKNOWN"):
		is.Kind = ast.IsUnknown
	case p.d.Features.Expr.IsUnknown && p.parseIsOther(is):
	case p.acceptKwSeq("DISTINCT", "FROM"):
		is.Kind = ast.IsDistinctFrom
		is.Other = p.parseExprPrec(prec)
		if _, next, ok := p.infixOp(); ok && next == prec && p.d.Features.Expr.DistinctFromNonAssoc {
			p.unexpected("end of IS DISTINCT FROM")
		}
	default:
		if !p.d.Features.Expr.IsAsOperator {
			p.unexpected("NULL, TRUE, FALSE, UNKNOWN or DISTINCT FROM")
		}
		// SQLite: IS / IS NOT as a general null-safe equality operator.
		op := "IS"
		if not {
			op = "IS NOT"
		}
		return &ast.Binary{Op: op, Left: left, Right: p.parseExprPrec(prec)}
	}
	return is
}

// isConstantOperand reports whether the NULL, TRUE or FALSE after IS begins a
// larger right operand: where IS is an ordinary operator, x IS NULL + 1 means
// x IS (NULL + 1).
func (p *Parser) isConstantOperand(prec int) bool {
	if !p.d.Features.Expr.IsAsOperator || !p.isKw("NULL", "TRUE", "FALSE") {
		return false
	}
	p.pos++
	defer func() { p.pos-- }()
	_, next, ok := p.infixOp()
	return ok && next > prec
}

// parseIsOther parses IS JSON [form] [WITH|WITHOUT UNIQUE [KEYS]], IS DOCUMENT
// and IS [form] NORMALIZED, keeping them as canonical words.
func (p *Parser) parseIsOther(is *ast.Is) bool {
	var words []string
	switch {
	case p.acceptKw("DOCUMENT"):
		words = []string{"DOCUMENT"}
	case p.acceptKw("NORMALIZED"):
		words = []string{"NORMALIZED"}
	case p.isKw("NFC", "NFD", "NFKC", "NFKD") && p.isKwAt(1, "NORMALIZED"):
		words = []string{p.next().Value, "NORMALIZED"}
		p.next()
	case p.acceptKw("JSON"):
		words = []string{"JSON"}
		if form := p.acceptOneOf("VALUE", "ARRAY", "OBJECT", "SCALAR"); form != "" {
			words = append(words, form)
		}
		if p.isKw("WITH", "WITHOUT") && p.isKwAt(1, "UNIQUE") {
			words = append(words, p.next().Value, "UNIQUE")
			p.next()
			if p.acceptKw("KEYS") {
				words = append(words, "KEYS")
			}
		}
	default:
		return false
	}
	is.Kind, is.Text = ast.IsOther, strings.Join(words, " ")
	return true
}

func (p *Parser) parseSubscript(left ast.Expr) ast.Expr {
	p.expectOp("[")
	sub := &ast.Subscript{Expr: left}
	if p.acceptOp(":") {
		sub.Slice = true
		if !p.isOp("]") {
			sub.Upper = p.parseExpr()
		}
	} else {
		first := p.parseExpr()
		if p.acceptOp(":") {
			sub.Slice = true
			sub.Lower = first
			if !p.isOp("]") {
				sub.Upper = p.parseExpr()
			}
		} else {
			sub.Index = first
		}
	}
	p.expectOp("]")
	return sub
}

// --- prefix and primary ------------------------------------------------------

func (p *Parser) parsePrefix() ast.Expr {
	t := p.peek()
	custom := p.d.Lexer.CustomOperators
	switch t.Kind {
	case lexer.Op:
		switch {
		case t.Text == "-", t.Text == "+", t.Text == "~" && !custom:
			p.next()
			p.negativeLiteral = t.Text == "-" && p.peek().Kind == lexer.Number
			return &ast.Unary{Op: t.Text, Expr: p.parseExprPrec(p.d.UnaryMinusPrecedence)}
		case (t.Text == "@" || t.Text == "@@") && p.d.Features.Expr.AtParams:
			p.next()
			if n := p.peek(); n.Kind == lexer.Ident || n.Kind == lexer.QuotedIdent {
				p.next()
				return &ast.Param{Text: t.Text + n.Text}
			}
			p.unexpected("parameter name")
		case custom && dialect.IsOperatorText(t.Text) && !builtinInfixOnly[t.Text]:
			// gram.y gives prefix generic operators the precedence of Op, so
			// `@ a + b` is `@ (a + b)`.
			p.next()
			return &ast.Unary{Op: t.Text, Expr: p.parseExprPrec(p.d.CustomOpPrecedence)}
		}
	case lexer.Ident:
		if t.Value == "NOT" {
			if p.bExpr {
				p.unexpected("expression")
			}
			p.next()
			return &ast.Unary{Op: "NOT", Expr: p.parseExprPrec(p.d.NotPrecedence)}
		}
		if t.Value == "OPERATOR" && custom && p.isOpAt(1, "(") {
			name := p.parseOperatorName()
			return &ast.Unary{Op: name, Expr: p.parseExprPrec(p.d.CustomOpPrecedence)}
		}
	}
	return p.parsePrimary()
}

func (p *Parser) parsePrimary() ast.Expr {
	t := p.peek()
	if t.Kind == lexer.Op && t.Text == "[" && p.d.Features.Expr.BracketArrays {
		arr := &ast.Array{}
		p.parseArrayBody(arr)
		return arr
	}
	switch t.Kind {
	case lexer.Number:
		p.next()
		negative := p.negativeLiteral
		p.negativeLiteral = false
		if p.d.Features.Expr.Int64Literals && isInteger(t) && !fitsInt64(t.Text, negative) {
			p.errorf(t.Pos, "invalid integer literal: %s", t.Text)
		}
		kind := ast.Float
		if isInteger(t) {
			kind = ast.Integer
		}
		return &ast.Literal{Kind: kind, Value: t.Text}
	case lexer.String:
		if p.d.Features.Names.StringAlias && p.isOpAt(1, ".") { // SQLite: 'tbl'.col
			return p.parseNameExpr()
		}
		return p.parseStringLit()
	case lexer.Blob:
		p.next()
		return &ast.Literal{Kind: ast.BlobLit, Value: t.Value}
	case lexer.Param:
		if len(t.Text) > 1 && t.Text[0] == '#' && isDigitByte(t.Text[1]) {
			// SQLite: #1 names a register of its virtual machine, which only
			// its own nested parses may do.
			p.unexpected("expression")
		}
		p.next()
		return &ast.Param{Text: t.Text}
	case lexer.QuotedIdent:
		return p.parseNameExpr()
	case lexer.Op:
		if t.Text == "(" {
			return p.parseParenExpr()
		}
	case lexer.Ident:
		return p.parseWordExpr()
	}
	p.unexpected("expression")
	return nil
}

func (p *Parser) parseParenExpr() ast.Expr {
	p.expectOp("(")
	q, pending := p.tryParenBodyQuery()
	if q != nil {
		p.expectOp(")")
		return &ast.Subquery{Query: q}
	}
	var exprs []ast.Expr
	if pending != nil {
		exprs = append(exprs, pending)
		for p.acceptOp(",") {
			exprs = append(exprs, p.parseExpr())
		}
	} else {
		exprs = p.parseExprList()
	}
	p.expectOp(")")
	if len(exprs) == 1 {
		return &ast.Paren{Expr: exprs[0]}
	}
	return p.finishRow(&ast.Tuple{Exprs: exprs})
}

// parseWordExpr handles an expression starting with a bare word: keyword
// constructs, function calls, typed literals and column references.
func (p *Parser) parseWordExpr() ast.Expr {
	t := p.peek()
	switch t.Value {
	case "NULL":
		p.next()
		return &ast.Literal{Kind: ast.Null}
	case "COLLATION":
		if p.isKwAt(1, "FOR") && p.isOpAt(2, "(") && p.d.Features.Expr.StandardFuncSyntax {
			p.pos += 2
			return &ast.Unary{Op: "COLLATION FOR", Expr: p.parseParenExpr()}
		}
	case "TRUE":
		if !p.isOpAt(1, "(") && !p.isOpAt(1, ".") {
			p.next()
			return &ast.Literal{Kind: ast.True}
		}
	case "FALSE":
		if !p.isOpAt(1, "(") && !p.isOpAt(1, ".") {
			p.next()
			return &ast.Literal{Kind: ast.False}
		}
	case "DEFAULT":
		if p.d.Features.Expr.DefaultExpr {
			p.next()
			return &ast.Default{}
		}
	case "CASE":
		return p.parseCase()
	case "CAST":
		if p.isOpAt(1, "(") {
			return p.parseCast()
		}
		p.unexpected(`"(" after CAST`)
	case "TRY_CAST":
		if p.d.Features.Expr.TryCast && p.isOpAt(1, "(") {
			return p.parseCast()
		}
	case "RAISE":
		if p.d.Features.Expr.FreeFormTypeNames {
			return p.parseRaise()
		}
	case "EXISTS":
		if p.isOpAt(1, "(") {
			p.next()
			p.expectOp("(")
			q := p.parseQuery()
			p.expectOp(")")
			return &ast.Exists{Query: q}
		}
	case "ARRAY":
		if p.d.Features.Expr.ArrayLiterals && (p.isOpAt(1, "[") || p.isOpAt(1, "(")) {
			return p.parseArray()
		}
	case "ROW":
		if p.d.Features.Expr.RowConstructor && p.isOpAt(1, "(") {
			p.next()
			p.expectOp("(")
			tup := &ast.Tuple{Row: true}
			if !p.isOp(")") {
				tup.Exprs = p.parseExprList()
			}
			p.expectOp(")")
			return p.finishRow(tup)
		}
	case "EXTRACT":
		if p.d.Features.Expr.StandardFuncSyntax && p.isOpAt(1, "(") {
			p.next()
			p.expectOp("(")
			e := &ast.Extract{}
			switch t := p.peek(); {
			case t.Kind == lexer.String:
				e.FieldString = p.parseStringLit()
			case t.Kind == lexer.QuotedIdent:
				e.Field = p.parseAnyIdent()
			case t.Kind == lexer.Ident && !p.d.IsReserved(t.Value) && !p.isColNameOnly():
				p.next()
				e.Field = ast.Ident{Name: t.Value}
			default:
				p.unexpected("field name")
			}
			p.expectKw("FROM")
			e.Expr = p.parseExpr()
			p.expectOp(")")
			return e
		}
	case "SUBSTRING":
		if p.isOpAt(1, "(") && p.d.Features.Expr.StandardFuncSyntax {
			return p.parseSubstring()
		}
	case "TRIM":
		if p.isOpAt(1, "(") && p.d.Features.Expr.StandardFuncSyntax {
			return p.parseTrim()
		}
	case "POSITION":
		if p.isOpAt(1, "(") && p.d.Features.Expr.StandardFuncSyntax {
			p.next()
			p.expectOp("(")
			sub := p.parseBExpr()
			p.expectKw("IN")
			pos := &ast.Position{Substr: sub, Str: p.parseBExpr()}
			p.expectOp(")")
			return pos
		}
	case "OVERLAY":
		if p.d.Features.Expr.StandardFuncSyntax && p.isOpAt(1, "(") {
			if e := p.tryOverlay(); e != nil {
				return e
			}
		}
	}
	if p.d.BareFuncs[t.Value] && !p.isBareFuncCall() {
		return p.parseBareFunc()
	}
	if p.d.Features.Expr.TypedStringLiteral && (p.d.TypedLiterals[t.Value] ||
		p.d.TypedLiterals == nil && (!p.d.IsReserved(t.Value) || p.d.FuncNameKeywords[t.Value])) {
		if ts := p.tryTypedString(); ts != nil {
			return ts
		}
	}
	return p.parseNameExpr()
}

// parseRaise parses SQLite's RAISE(IGNORE) and RAISE(action, message).
func (p *Parser) parseRaise() ast.Expr {
	p.expectKw("RAISE")
	p.expectOp("(")
	r := &ast.Raise{}
	if p.acceptKw("IGNORE") {
		r.Action = "IGNORE"
	} else {
		r.Action = p.acceptOneOf("ROLLBACK", "ABORT", "FAIL")
		if r.Action == "" {
			p.unexpected("IGNORE, ROLLBACK, ABORT or FAIL")
		}
		p.expectOp(",")
		r.Message = p.parseExpr()
	}
	p.expectOp(")")
	return r
}

// isBareFuncCall reports whether a BareFuncs word is followed by a real
// argument list and so is an ordinary call: current_schema(), or in SQLite
// current_time(...).
func (p *Parser) isBareFuncCall() bool {
	return p.isOpAt(1, "(") && p.isKw("CURRENT_SCHEMA")
}

// parseBareFunc parses CURRENT_DATE and friends, with optional (precision)
// where the grammar allows one.
func (p *Parser) parseBareFunc() ast.Expr {
	t := p.next()
	fc := &ast.FuncCall{Name: &ast.Name{Parts: []ast.Ident{{Name: t.Text}}}, NoParens: true}
	switch t.Value {
	case "CURRENT_TIME", "CURRENT_TIMESTAMP", "LOCALTIME", "LOCALTIMESTAMP":
		if p.d.Features.Expr.StandardFuncSyntax && p.acceptOp("(") { // SQLite's are keywords, never called
			if p.peek().Kind != lexer.Number {
				p.unexpected("precision")
			}
			fc.NoParens = false
			fc.Args = []ast.Expr{p.parsePrimary()}
			p.expectOp(")")
		}
	}
	return fc
}

// tryOverlay parses OVERLAY(x PLACING y FROM a [FOR b]). Plain calls such as
// overlay(a, b) return nil with the position untouched.
func (p *Parser) tryOverlay() ast.Expr {
	var o *ast.Overlay
	ok := p.try(func() {
		p.next()
		p.expectOp("(")
		o = &ast.Overlay{Expr: p.parseExpr()}
		p.expectKw("PLACING")
		o.Placing = p.parseExpr()
		p.expectKw("FROM")
		o.From = p.parseExpr()
		if p.acceptKw("FOR") {
			o.For = p.parseExpr()
		}
		p.expectOp(")")
	})
	if !ok {
		return nil
	}
	return o
}

// tryTypedString parses `typename 'literal'` (DATE '2020-01-01',
// TIMESTAMP WITH TIME ZONE '...', int4 '7'). It returns nil and leaves the
// position untouched when the input is not a typed literal.
func (p *Parser) tryTypedString() ast.Expr {
	if p.peekN(1).Kind == lexer.String && !p.isColNameOnly() {
		t := p.next()
		name := t.Text
		if !strings.EqualFold(t.Text, t.Value) { // N'x' is NCHAR 'x'
			name = t.Value
		}
		dt := &ast.DataType{Name: name}
		ts := &ast.TypedString{Type: dt, Value: p.parseStringValue()}
		if t.Value == "INTERVAL" {
			dt.Fields = p.parseIntervalFields()
		}
		return ts
	}
	if !p.isKw("TIMESTAMP", "TIME", "INTERVAL", "DOUBLE", "CHARACTER", "CHAR", "VARCHAR",
		"NUMERIC", "DECIMAL", "DEC", "BIT", "NATIONAL", "NCHAR", "FLOAT") {
		return nil
	}
	var ts *ast.TypedString
	ok := p.try(func() {
		typ := p.parseType()
		if p.peek().Kind != lexer.String || typ.Fields != "" { // INTERVAL YEAR '1' is not valid
			p.unexpected("string literal")
		}
		ts = &ast.TypedString{Type: typ, Value: p.parseStringValue()}
	})
	if !ok {
		return nil // an untyped nil: a nil *TypedString in an Expr is not nil
	}
	return ts
}

// isColNameOnly reports whether the current word can name a column but not a type.
func (p *Parser) isColNameOnly() bool {
	v := p.peek().Value
	return p.d.ColNameKeywords[v] && !p.d.TypeKeywords[v]
}

// try runs f and, if it raises a syntax error, restores the position and
// returns false.
func (p *Parser) try(f func()) (ok bool) {
	save := p.pos
	defer func() {
		if r := recover(); r != nil {
			if _, isErr := r.(*Error); !isErr {
				panic(r)
			}
			p.pos = save
			ok = false
		}
	}()
	f()
	return true
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// parseNameExpr parses a column reference, `t.*`, or a function call.
func (p *Parser) parseNameExpr() ast.Expr {
	t := p.peek()
	var first ast.Ident
	switch {
	case t.Kind == lexer.QuotedIdent:
		first = p.parseAnyIdent()
	case t.Kind == lexer.Ident && p.d.FuncNameKeywords[t.Value] && p.isOpAt(1, "("):
		first = p.parseAnyIdent()
	default:
		first = p.parseIdent()
	}
	name := &ast.Name{Parts: []ast.Ident{first}}
	for p.isOp(".") {
		if p.isOpAt(1, "*") && p.d.Features.Names.KeywordLabels {
			p.next()
			p.next()
			return &ast.Star{Qualifier: name}
		}
		if limit := p.d.Features.Names.MaxNameParts; limit > 0 && len(name.Parts) > limit {
			p.unexpected("end of column reference") // schema.table.column at most
		}
		p.next()
		name.Parts = append(name.Parts, p.parseLabel())
	}
	if p.isOp("(") {
		if len(name.Parts) > 1 && !p.d.Features.Names.QualifiedFunctions {
			p.unexpected("end of column reference")
		}
		if word := strings.ToUpper(first.Name); len(name.Parts) == 1 && !first.Quoted &&
			p.d.ColNameKeywords[word] && !p.d.SpecialFuncs[word] && !p.d.RawArgFuncs[word] {
			p.errorf(t.Pos, "%s cannot be used as a function name", t.Text)
		}
		fc := p.parseFuncCall(name)
		if p.d.Features.Expr.TypedStringLiteral && p.d.TypedLiterals == nil && p.peek().Kind == lexer.String && fc.Over == nil && fc.Filter == nil {
			// numeric(10, 2) '1.5': a modified type name, not a call
			return &ast.TypedString{Type: &ast.DataType{Name: nameText(name), Args: fc.Args}, Value: p.parseStringValue()}
		}
		return fc
	}
	if len(name.Parts) > 1 && p.d.Features.Expr.TypedStringLiteral && p.d.TypedLiterals == nil && p.peek().Kind == lexer.String {
		return &ast.TypedString{Type: &ast.DataType{Name: nameText(name)}, Value: p.parseStringValue()}
	}
	return name
}

func (p *Parser) parseFuncCall(name *ast.Name) *ast.FuncCall {
	fc := &ast.FuncCall{Name: name}
	if len(name.Parts) == 1 && !name.Parts[0].Quoted && p.d.RawArgFuncs[strings.ToUpper(name.Parts[0].Name)] {
		p.expectOp("(")
		raw := p.takeRawArgs()
		fc.RawArgs = &raw
		p.expectOp(")")
		p.parseFuncTail(fc)
		return fc
	}
	p.expectOp("(")
	if !p.acceptOp(")") {
		if p.isOp("*") && p.isOpAt(1, ")") {
			p.next()
			fc.Args = []ast.Expr{&ast.Star{}}
		} else {
			if p.acceptKw("DISTINCT") {
				fc.Distinct = true
			} else {
				p.acceptKw("ALL")
			}
			// SQLite allows COUNT(DISTINCT), and rejects it only at analysis.
			if !p.d.Features.Expr.FreeFormTypeNames || !p.isOp(")") && !p.isKwSeq("ORDER", "BY") {
				fc.Args = []ast.Expr{p.parseFuncArg()}
				for p.acceptOp(",") {
					fc.Args = append(fc.Args, p.parseFuncArg())
				}
			}
			if p.acceptKwSeq("ORDER", "BY") {
				fc.OrderBy = p.parseOrderItems()
			}
		}
		p.expectOp(")")
	}
	p.checkCallArgs(fc)
	p.parseFuncTail(fc)
	return fc
}

// specialArity gives the argument counts of PostgreSQL's functions with
// grammar of their own; -1 is no upper bound.
var specialArity = map[string][2]int{
	"COALESCE": {1, -1}, "GREATEST": {1, -1}, "LEAST": {1, -1}, "GROUPING": {1, -1}, "NULLIF": {2, 2},
	"NORMALIZE": {1, 2},
}

// checkCallArgs applies the argument rules gram.y has for some calls. The
// call has just been parsed, so the current token is the one after ')'.
func (p *Parser) checkCallArgs(fc *ast.FuncCall) {
	end := p.peekN(-1).Pos // the closing parenthesis
	for i, a := range fc.Args {
		if u, ok := a.(*ast.Unary); ok && u.Op == "VARIADIC" && i < len(fc.Args)-1 {
			p.errorf(end, "VARIADIC must be the last argument")
		}
	}
	name := fc.Name.Parts[len(fc.Name.Parts)-1]
	word := strings.ToUpper(name.Name)
	arity, special := specialArity[word]
	if !special || len(fc.Name.Parts) > 1 || name.Quoted || !p.d.SpecialFuncs[word] {
		return
	}
	n := len(fc.Args)
	if n < arity[0] || arity[1] >= 0 && n > arity[1] || fc.Distinct || len(fc.OrderBy) > 0 {
		p.errorf(end, "%s takes %s", word, map[bool]string{true: "two arguments", false: "at least one argument"}[word == "NULLIF"])
	}
	if word == "NORMALIZE" && n == 2 { // the form is a word: NFC, NFD, NFKC or NFKD
		form, ok := fc.Args[1].(*ast.Name)
		if !ok || len(form.Parts) > 1 || form.Parts[0].Quoted ||
			!map[string]bool{"NFC": true, "NFD": true, "NFKC": true, "NFKD": true}[strings.ToUpper(form.Parts[0].Name)] {
			p.errorf(end, "NORMALIZE takes NFC, NFD, NFKC or NFKD as its form")
		}
	}
}

// parseFuncTail parses WITHIN GROUP, FILTER and OVER after the argument list.
func (p *Parser) parseFuncTail(fc *ast.FuncCall) {
	if p.d.Features.Expr.WithinGroup && p.acceptKwSeq("WITHIN", "GROUP") {
		if len(fc.OrderBy) > 0 {
			p.errorf(p.peek().Pos, "cannot use multiple ORDER BY clauses with WITHIN GROUP")
		}
		if fc.Distinct {
			p.errorf(p.peek().Pos, "cannot use DISTINCT with WITHIN GROUP")
		}
		if n := len(fc.Args); n > 0 {
			if u, ok := fc.Args[n-1].(*ast.Unary); ok && u.Op == "VARIADIC" {
				p.errorf(p.peek().Pos, "cannot use VARIADIC with WITHIN GROUP")
			}
		}
		p.expectOp("(")
		p.expectKw("ORDER", "BY")
		fc.WithinGroup = p.parseOrderItems()
		p.expectOp(")")
	}
	if p.isKw("FILTER") && p.isOpAt(1, "(") {
		p.next()
		p.expectOp("(")
		p.expectKw("WHERE")
		fc.Filter = p.parseExpr()
		p.expectOp(")")
	}
	if p.isKw("OVER") && (p.isOpAt(1, "(") || p.peekN(1).Kind == lexer.QuotedIdent ||
		p.peekN(1).Kind == lexer.Ident && !p.d.IsReserved(p.peekN(1).Value)) {
		p.next()
		if p.isOp("(") {
			fc.Over = p.parseWindowSpec()
		} else {
			id := p.parseIdent()
			fc.Over = &ast.WindowSpec{Name: &id, NameOnly: true}
		}
	}
}

// takeRawArgs consumes up to the ')' closing a call, returning the source text.
func (p *Parser) takeRawArgs() string {
	start, end, depth := p.peek().Pos, p.peek().Pos, 0
	for {
		t := p.peek()
		if t.Kind == lexer.EOF {
			p.unexpected(`")"`)
		}
		if t.Kind == lexer.Op && t.Text == "(" {
			depth++
		}
		if t.Kind == lexer.Op && t.Text == ")" {
			if depth == 0 {
				return p.src[start:end]
			}
			depth--
		}
		if enc := p.peekN(3); p.isKwSeq("FORMAT", "JSON", "ENCODING") && !jsonEncodings[strings.ToUpper(enc.Value)] {
			p.errorf(enc.Pos, "unrecognized JSON encoding: %s", enc.Text)
		}
		end = t.Pos + len(t.Text)
		p.next()
	}
}

var jsonEncodings = map[string]bool{"UTF8": true, "UTF16": true, "UTF32": true}

func (p *Parser) parseFuncArg() ast.Expr {
	if p.d.Features.Expr.VariadicArgs && p.acceptKw("VARIADIC") {
		return &ast.Unary{Op: "VARIADIC", Expr: p.parseExpr()}
	}
	if p.d.Features.Expr.NamedArgs && p.canBeIdent() && !p.d.ColNameKeywords[p.peek().Value] && (p.isOpAt(1, "=>") || p.isOpAt(1, ":=")) {
		id := p.parseIdent()
		p.next()
		return &ast.NamedArg{Name: id, Value: p.parseExpr()}
	}
	return p.parseExpr()
}

func (p *Parser) parseCase() ast.Expr {
	p.expectKw("CASE")
	c := &ast.Case{}
	if !p.isKw("WHEN", "END") {
		c.Operand = p.parseExpr()
	}
	for p.acceptKw("WHEN") {
		w := &ast.When{Cond: p.parseExpr()}
		p.expectKw("THEN")
		w.Result = p.parseExpr()
		c.Whens = append(c.Whens, w)
	}
	if len(c.Whens) == 0 {
		p.unexpected("WHEN")
	}
	if p.acceptKw("ELSE") {
		c.Else = p.parseExpr()
	}
	p.expectKw("END")
	return c
}

func (p *Parser) parseCast() ast.Expr {
	style := ast.CastStandard
	if p.next().Value == "TRY_CAST" {
		style = ast.CastTry
	}
	p.expectOp("(")
	e := p.parseExpr()
	p.expectKw("AS")
	c := &ast.Cast{Expr: e, Style: style}
	if p.d.Features.Expr.FreeFormTypeNames && p.isOp(")") { // SQLite allows no type at all
		c.Type = &ast.DataType{}
	} else {
		c.Type = p.parseType()
	}
	p.expectOp(")")
	return c
}

func (p *Parser) parseArray() ast.Expr {
	p.expectKw("ARRAY")
	arr := &ast.Array{}
	if p.acceptOp("(") {
		arr.Subquery = p.parseQuery()
		p.expectOp(")")
		return arr
	}
	p.parseArrayBody(arr)
	return arr
}

// parseArrayBody parses [a, b] or [[a, b], [c, d]].
func (p *Parser) parseArrayBody(arr *ast.Array) {
	p.expectOp("[")
	arr.Elems = []ast.Expr{}
	nested := p.isOp("[") // all elements are sub-arrays, or none (PostgreSQL)
	for !p.isOp("]") {
		if p.isOp("[") != nested && p.d.Features.Expr.ArrayLiterals {
			p.unexpected("element of the same kind as the first")
		}
		if p.isOp("[") {
			inner := &ast.Array{Nested: true}
			p.parseArrayBody(inner)
			arr.Elems = append(arr.Elems, inner)
		} else {
			arr.Elems = append(arr.Elems, p.parseExpr())
		}
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp("]")
}

func (p *Parser) parseSubstring() ast.Expr {
	start := p.pos
	word := p.next().Text
	p.expectOp("(")
	plainCall := func() ast.Expr { // SUBSTRING(), SUBSTRING(x, a, b), SUBSTRING(a => x)
		p.pos = start + 1
		return p.parseFuncCall(&ast.Name{Parts: []ast.Ident{{Name: word}}})
	}
	if p.isOp(")") || p.canBeIdent() && (p.isOpAt(1, "=>") || p.isOpAt(1, ":=")) {
		return plainCall()
	}
	s := &ast.Substring{Expr: p.parseExpr()}
	switch {
	case p.isOp(","):
		return plainCall()
	case p.acceptKw("SIMILAR"):
		s.Similar = p.parseExpr()
		p.expectKw("ESCAPE")
		s.Escape = p.parseExpr()
	case p.acceptKw("FROM"):
		s.From = p.parseExpr()
		if p.acceptKw("FOR") {
			s.For = p.parseExpr()
		}
	case p.acceptKw("FOR"): // FOR b FROM a has the tree of FROM a FOR b
		s.For = p.parseExpr()
		if p.acceptKw("FROM") {
			s.From = p.parseExpr()
		}
	}
	p.expectOp(")")
	return s
}

func (p *Parser) parseTrim() ast.Expr {
	p.expectKw("TRIM")
	p.expectOp("(")
	t := &ast.Trim{Where: p.acceptOneOf("LEADING", "TRAILING", "BOTH")}
	if t.From = p.acceptKw("FROM"); !t.From {
		first := p.parseExpr()
		if t.From = p.acceptKw("FROM"); t.From {
			t.Chars = first
		} else {
			t.Exprs = []ast.Expr{first}
			if p.acceptOp(",") {
				t.Exprs = append(t.Exprs, p.parseExprList()...)
			}
			p.expectOp(")")
			return t
		}
	}
	t.Exprs = p.parseExprList()
	p.expectOp(")")
	return t
}

// --- types -------------------------------------------------------------------

// pgMultiWord lists the second words of PostgreSQL's multi-word type names.
var pgMultiWord = map[string][]string{
	"DOUBLE":    {"PRECISION"},
	"CHARACTER": {"VARYING"},
	"CHAR":      {"VARYING"},
	"BIT":       {"VARYING"},
	"NATIONAL":  {"CHARACTER", "CHAR"},
	"NCHAR":     {"VARYING"},
}

// parseType parses a data type.
func (p *Parser) parseType() *ast.DataType {
	t := p.peek()
	dt := &ast.DataType{}
	switch {
	case p.d.Features.Expr.FreeFormTypeNames && (t.Kind == lexer.QuotedIdent || t.Kind == lexer.String):
		p.next()
		dt.Name = p.parseTypeWords(t)
	case t.Kind == lexer.QuotedIdent:
		dt.Name = nameText(p.parseName())
	case t.Kind == lexer.Ident && t.Value == "SETOF" && p.d.Features.DDL.ExtendedDDL:
		p.next()
		if p.isKw("SETOF") {
			p.unexpected("type name")
		}
		dt = p.parseType()
		dt.Setof = true
		return dt
	case t.Kind == lexer.Ident:
		if p.d.IsReserved(t.Value) && !p.d.FuncNameKeywords[t.Value] || p.isColNameOnly() || p.d.NotAlias[t.Value] {
			p.unexpected("type name")
		}
		p.next()
		dt.Name = p.parseTypeWords(t)
	default:
		p.unexpected("type name")
	}
	p.parseTypeArgs(dt)
	if p.d.Features.Expr.FreeFormTypeNames {
		return dt
	}
	switch strings.ToUpper(dt.Name) {
	case "TIME", "TIMESTAMP":
		switch {
		case p.acceptKwSeq("WITH", "TIME", "ZONE"):
			dt.TimeZone = ast.TZWith
		case p.acceptKwSeq("WITHOUT", "TIME", "ZONE"):
			dt.TimeZone = ast.TZWithout
		}
	case "INTERVAL":
		dt.Fields = p.parseIntervalFields()
	}
	p.parseArrayBounds(dt)
	return dt
}

// integerOnlyTypes take a single integer argument in gram.y; other types take
// a general expression list.
var integerOnlyTypes = map[string]bool{
	"CHAR": true, "CHARACTER": true, "VARCHAR": true, "NCHAR": true, "NATIONAL": true,
	"FLOAT": true, "TIME": true, "TIMESTAMP": true, "INTERVAL": true,
}

// noArgTypes are the built-in numeric types that take no modifiers at all.
var noArgTypes = map[string]bool{
	"INT": true, "INTEGER": true, "SMALLINT": true, "BIGINT": true, "REAL": true, "BOOLEAN": true,
	"DOUBLE PRECISION": true,
}

func (p *Parser) parseTypeArgs(dt *ast.DataType) {
	if !p.d.Features.Expr.FreeFormTypeNames && noArgTypes[strings.ToUpper(dt.Name)] {
		return
	}
	if !p.acceptOp("(") {
		return
	}
	switch {
	case p.d.Features.Expr.FreeFormTypeNames:
		dt.Args = []ast.Expr{p.parseSignedNumber()}
		if p.acceptOp(",") {
			dt.Args = append(dt.Args, p.parseSignedNumber())
		}
	case integerOnlyTypes[strings.ToUpper(strings.Fields(dt.Name)[0])]:
		if p.peek().Kind != lexer.Number {
			p.unexpected("integer")
		}
		dt.Args = []ast.Expr{p.parsePrimary()}
	default:
		dt.Args = p.parseExprList()
	}
	p.expectOp(")")
}

// parseSignedNumber parses [+|-] number.
func (p *Parser) parseSignedNumber() ast.Expr {
	if p.isOp("-") || p.isOp("+") {
		op := p.next().Text
		if p.peek().Kind != lexer.Number {
			p.unexpected("number")
		}
		return &ast.Unary{Op: op, Expr: p.parsePrimary()}
	}
	if p.peek().Kind != lexer.Number {
		p.unexpected("number")
	}
	return p.parsePrimary()
}

// parseTypeWords returns the full type name given its first word (consumed).
func (p *Parser) parseTypeWords(first lexer.Token) string {
	words := []string{first.Text}
	switch {
	case p.d.Features.Expr.FreeFormTypeNames:
		// SQLite: any run of bare words is a type name (UNSIGNED BIG INT).
		for p.isFreeFormTypeWord() {
			words = append(words, p.next().Text)
		}
	case p.isOp("."):
		n := &ast.Name{Parts: []ast.Ident{{Name: first.Text}}}
		for p.acceptOp(".") {
			n.Parts = append(n.Parts, p.parseAnyIdent())
		}
		return nameText(n)
	default:
		for _, second := range pgMultiWord[first.Value] {
			if p.isKw(second) {
				words = append(words, p.next().Text)
				if (second == "CHARACTER" || second == "CHAR") && p.isKw("VARYING") {
					words = append(words, p.next().Text)
				}
				break
			}
		}
	}
	return strings.Join(words, " ")
}

// isFreeFormTypeWord reports whether the next token continues a SQLite type
// name. Quoted words count, so `a int[]` is the words `int` and `[]`.
func (p *Parser) isFreeFormTypeWord() bool {
	t := p.peek()
	switch t.Kind {
	case lexer.QuotedIdent, lexer.String:
		return true
	case lexer.Ident:
		return !p.d.IsReserved(t.Value) && !p.d.NotAlias[t.Value]
	}
	return false
}

// parseArrayBounds parses [] and [n], any number of them, or a single ARRAY [n].
func (p *Parser) parseArrayBounds(dt *ast.DataType) {
	if !p.d.Features.Expr.ArrayLiterals {
		return
	}
	sized := func() (string, bool) {
		if t := p.peekN(1); p.isOp("[") && t.Kind == lexer.Number && isInteger(t) && p.isOpAt(2, "]") {
			p.pos += 3
			return t.Text, true
		}
		return "", false
	}
	for {
		size, ok := sized()
		if !ok && p.isOp("[") && p.isOpAt(1, "]") {
			p.pos += 2
			ok = true
		}
		if !ok {
			break
		}
		dt.ArrayBounds = append(dt.ArrayBounds, size)
	}
	if len(dt.ArrayBounds) == 0 && p.acceptKw("ARRAY") {
		size, _ := sized()
		dt.ArrayBounds = append(dt.ArrayBounds, size)
	}
}

// parseIntervalFields parses an optional INTERVAL qualifier and returns it in
// canonical form: YEAR, DAY TO SECOND(3), ...
func (p *Parser) parseIntervalFields() string {
	unit := func() string {
		u := p.acceptOneOf("YEAR", "MONTH", "DAY", "HOUR", "MINUTE", "SECOND")
		if u == "SECOND" && p.isOp("(") && p.peekN(1).Kind == lexer.Number && p.isOpAt(2, ")") {
			u += "(" + p.peekN(1).Text + ")"
			p.pos += 3
		}
		return u
	}
	first := unit()
	if first == "" {
		return ""
	}
	if !p.acceptKw("TO") {
		return first
	}
	second := unit()
	if second == "" {
		p.unexpected("interval unit")
	}
	return first + " TO " + second
}

// nameText renders a Name as it would be printed, for use as a type name.
func nameText(n *ast.Name) string {
	parts := make([]string, len(n.Parts))
	for i, id := range n.Parts {
		if id.Quoted {
			parts[i] = `"` + strings.ReplaceAll(id.Name, `"`, `""`) + `"`
		} else {
			parts[i] = id.Name
		}
	}
	return strings.Join(parts, ".")
}

// --- ordering and windows ------------------------------------------------------

func (p *Parser) parseOrderItems() []*ast.OrderItem {
	items := []*ast.OrderItem{p.parseOrderItem()}
	for p.acceptOp(",") {
		items = append(items, p.parseOrderItem())
	}
	return items
}

func (p *Parser) parseOrderItem() *ast.OrderItem {
	item := &ast.OrderItem{Expr: p.parseExpr()}
	item.Direction = p.acceptOneOf("ASC", "DESC")
	if item.Direction == "" && p.d.Features.Query.OrderByUsing && p.acceptKw("USING") {
		switch t := p.peek(); {
		case p.isKw("OPERATOR"):
			item.Using = p.parseOperatorName()
		case t.Kind == lexer.Op && dialect.IsOperatorText(t.Text):
			item.Using = p.next().Text
		default:
			p.unexpected("operator")
		}
	}
	if p.acceptKw("NULLS") {
		item.Nulls = p.acceptOneOf("FIRST", "LAST")
		if item.Nulls == "" {
			p.unexpected("FIRST or LAST")
		}
	}
	return item
}

func (p *Parser) parseWindowSpec() *ast.WindowSpec {
	p.expectOp("(")
	ws := &ast.WindowSpec{}
	if p.canBeIdent() && !p.isKw("PARTITION", "ORDER", "ROWS", "RANGE", "GROUPS") {
		id := p.parseIdent()
		ws.Name = &id
	}
	if p.acceptKwSeq("PARTITION", "BY") {
		ws.PartitionBy = p.parseExprList()
	}
	if p.acceptKwSeq("ORDER", "BY") {
		ws.OrderBy = p.parseOrderItems()
	}
	if units := p.acceptOneOf("ROWS", "RANGE", "GROUPS"); units != "" {
		f := &ast.Frame{Units: units}
		if p.acceptKw("BETWEEN") {
			f.Start = p.parseFrameBound()
			p.expectKw("AND")
			f.End = p.parseFrameBound()
		} else {
			f.Start = p.parseFrameBound()
		}
		p.checkFrame(f)
		if p.acceptKw("EXCLUDE") {
			switch {
			case p.acceptKwSeq("CURRENT", "ROW"):
				f.Exclude = "CURRENT ROW"
			case p.acceptKwSeq("NO", "OTHERS"):
				f.Exclude = "NO OTHERS"
			default:
				f.Exclude = p.acceptOneOf("GROUP", "TIES")
				if f.Exclude == "" {
					p.unexpected("frame exclusion")
				}
			}
		}
		ws.Frame = f
	}
	p.expectOp(")")
	return ws
}

// checkFrame applies the rules both grammars enforce on frame bounds.
func (p *Parser) checkFrame(f *ast.Frame) {
	pos := p.peek().Pos
	if f.Start.Kind == ast.UnboundedFollowing {
		p.errorf(pos, "frame start cannot be UNBOUNDED FOLLOWING")
	}
	if f.End == nil {
		if p.d.Features.Query.StrictFrames && f.Start.Kind == ast.Following {
			p.errorf(pos, "frame starting from following row cannot end with current row")
		}
		return
	}
	if f.End.Kind == ast.UnboundedPreceding {
		p.errorf(pos, "frame end cannot be UNBOUNDED PRECEDING")
	}
	if !p.d.Features.Query.StrictFrames {
		return
	}
	if f.Start.Kind == ast.CurrentRow && f.End.Kind == ast.Preceding {
		p.errorf(pos, "frame starting from current row cannot have preceding rows")
	}
	if f.Start.Kind == ast.Following && (f.End.Kind == ast.CurrentRow || f.End.Kind == ast.Preceding) {
		p.errorf(pos, "frame starting from following row cannot have preceding rows")
	}
}

func (p *Parser) parseFrameBound() *ast.FrameBound {
	switch {
	case p.acceptKwSeq("CURRENT", "ROW"):
		return &ast.FrameBound{Kind: ast.CurrentRow}
	case p.acceptKwSeq("UNBOUNDED", "PRECEDING"):
		return &ast.FrameBound{Kind: ast.UnboundedPreceding}
	case p.acceptKwSeq("UNBOUNDED", "FOLLOWING"):
		return &ast.FrameBound{Kind: ast.UnboundedFollowing}
	case p.d.Features.Expr.FreeFormTypeNames && p.isKw("CURRENT", "UNBOUNDED"):
		p.unexpected("frame bound") // keywords here in SQLite, never column names
	}
	e := p.parseExpr()
	switch {
	case p.acceptKw("PRECEDING"):
		return &ast.FrameBound{Kind: ast.Preceding, Expr: e}
	case p.acceptKw("FOLLOWING"):
		return &ast.FrameBound{Kind: ast.Following, Expr: e}
	}
	p.unexpected("PRECEDING or FOLLOWING")
	return nil
}
