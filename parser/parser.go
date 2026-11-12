// Package parser is a hand-written recursive-descent SQL parser with
// precedence climbing for expressions. Dialect differences are driven by the
// tables and feature flags in package dialect.
//
// Errors are raised with panic(*Error) inside the parser and recovered at the
// statement boundary, which keeps the grammar code free of error plumbing.
package parser

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"sqlp/ast"
	"sqlp/dialect"
	"sqlp/lexer"
)

// Error is a syntax error with a position in the source. Pos is a byte
// offset; Line and Column count from 1, and Column counts characters
// (Unicode code points), as a person reading the line would.
type Error struct {
	Pos    int
	Line   int
	Column int
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("syntax error at line %d, column %d: %s", e.Line, e.Column, e.Msg)
}

// Limits bounds the work a single parse will do, so that untrusted input
// cannot hang or crash the process (see AGENT_TODO.md). A zero Limits uses
// the defaults; negative values disable the corresponding check.
type Limits struct {
	MaxBytes int // default DefaultMaxBytes
	MaxDepth int // default DefaultMaxDepth
}

const (
	// DefaultMaxBytes is the default Limits.MaxBytes.
	DefaultMaxBytes = 4 << 20 // 4 MiB
	// DefaultMaxDepth is the default Limits.MaxDepth. SQLite's default
	// SQLITE_MAX_EXPR_DEPTH is 1000.
	DefaultMaxDepth = 1000
)

func (l Limits) maxBytes() int {
	if l.MaxBytes == 0 {
		return DefaultMaxBytes
	}
	return l.MaxBytes
}

func (l Limits) maxDepth() int {
	if l.MaxDepth == 0 {
		return DefaultMaxDepth
	}
	return l.MaxDepth
}

// Parser holds the token stream and the dialect in use.
type Parser struct {
	d    *dialect.Dialect
	src  string
	toks []lexer.Token
	pos  int

	queryStartMemo []int8 // isQueryStartAtPos cache: 0 unknown, 1 true, 2 false

	ctx      context.Context
	steps    int // next() calls since the last ctx.Err check
	depth    int
	maxDepth int

	// bExpr is set while parsing PostgreSQL's b_expr (see parseBExpr).
	bExpr bool

	lineStarts []int // byte offset of each line, built on first use
	lastPos    int   // the last position lineCol counted characters to,
	lastCol    int   // and its column, so in-order lookups stay linear
}

// Parse parses a script of zero or more ';'-separated statements, with
// default resource limits.
func Parse(sql string, d *dialect.Dialect) ([]ast.Statement, error) {
	return ParseWithLimits(context.Background(), sql, d, Limits{})
}

// ParseWithLimits is Parse with caller-specified resource limits and a
// context whose cancellation or deadline aborts the parse.
func ParseWithLimits(ctx context.Context, sql string, d *dialect.Dialect, limits Limits) (stmts []ast.Statement, err error) {
	defer func() {
		if err != nil {
			stmts = nil // all or nothing; see ParseScript for partial results
		}
	}()
	defer recoverAny(&err) // backstop for a panic in newParser, before a Parser exists to attribute a position to
	p, lexErr := newParser(ctx, sql, d, limits)
	if lexErr != nil {
		return nil, lexErr
	}
	defer p.recover(&err)
	for {
		for p.acceptOp(";") {
		}
		if p.peek().Kind == lexer.EOF {
			return stmts, nil
		}
		stmts = append(stmts, p.parseStatement())
		if p.peek().Kind != lexer.EOF {
			p.expectOp(";")
		}
	}
}

// ParseExpr parses a single standalone expression, with default resource
// limits.
func ParseExpr(sql string, d *dialect.Dialect) (ast.Expr, error) {
	return ParseExprWithLimits(context.Background(), sql, d, Limits{})
}

// ParseExprWithLimits is ParseExpr with caller-specified resource limits and
// a context whose cancellation or deadline aborts the parse.
func ParseExprWithLimits(ctx context.Context, sql string, d *dialect.Dialect, limits Limits) (e ast.Expr, err error) {
	defer recoverAny(&err) // backstop for a panic in newParser, before a Parser exists to attribute a position to
	p, lexErr := newParser(ctx, sql, d, limits)
	if lexErr != nil {
		return nil, lexErr
	}
	defer p.recover(&err)
	e = p.parseExpr()
	if p.peek().Kind != lexer.EOF {
		p.unexpected("end of expression")
	}
	return e, nil
}

func checkSize(sql string, limits Limits) error {
	if max := limits.maxBytes(); max >= 0 && len(sql) > max {
		return &Error{Msg: fmt.Sprintf("input is %d bytes, over the %d byte limit", len(sql), max)}
	}
	return nil
}

func newParser(ctx context.Context, sql string, d *dialect.Dialect, limits Limits) (*Parser, error) {
	if err := checkSize(sql, limits); err != nil {
		return nil, err
	}
	toks, lexErr := lexer.Tokenize(sql, d.Lexer)
	if lexErr != nil {
		return nil, lexErr
	}
	return &Parser{d: d, src: sql, toks: toks, ctx: ctx, maxDepth: limits.maxDepth()}, nil
}

// recover turns a panic into an error instead of propagating it to the
// caller. A *Error is a syntax error raised deliberately by the parser; any
// other panic is a parser bug, which would otherwise crash a long-running
// caller such as an API server (see AGENT_TODO.md). It is reported as an
// internal error rather than re-panicked, since by this point the input is
// known to be hostile or at least unusual, so a bug here should degrade to a
// rejected parse, not an outage.
func (p *Parser) recover(err *error) {
	if r := recover(); r != nil {
		if pe, ok := r.(*Error); ok {
			*err = pe
			return
		}
		*err = p.errorAt(p.peek().Pos, fmt.Sprintf("internal error: %v", r))
	}
}

// recoverAny is p.recover's counterpart for a panic that happens before a
// Parser exists (i.e. in the lexer), so there's no position to attribute it
// to.
func recoverAny(err *error) {
	if r := recover(); r != nil {
		if pe, ok := r.(*Error); ok {
			*err = pe
			return
		}
		*err = &Error{Msg: fmt.Sprintf("internal error: %v", r)}
	}
}

// enterDepth guards recursive entry points (parseExprPrec, parseQuery,
// parseTableExpr) against unbounded nesting: a Go stack overflow from
// runaway recursion is fatal and cannot be recovered, unlike a *Error (see
// AGENT_TODO.md).
func (p *Parser) enterDepth() {
	p.depth++
	if p.maxDepth >= 0 && p.depth > p.maxDepth {
		p.errorf(p.peek().Pos, "max nesting depth (%d) exceeded", p.maxDepth)
	}
}

func (p *Parser) exitDepth() { p.depth-- }

// --- token access ---------------------------------------------------------

func (p *Parser) peek() lexer.Token { return p.peekN(0) }

func (p *Parser) peekN(n int) lexer.Token { return p.tokAt(p.pos + n) }

// tokAt returns the token at an absolute position, clamped to EOF.
func (p *Parser) tokAt(pos int) lexer.Token {
	if pos < len(p.toks) {
		return p.toks[pos]
	}
	return p.toks[len(p.toks)-1] // EOF
}

func (p *Parser) next() lexer.Token {
	t := p.peek()
	if t.Escape || t.Unicode {
		p.checkEscapes(t)
	}
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	p.checkContext()
	return t
}

// checkEscapes rejects malformed E” and U&” escapes as the token is consumed,
// so one bad string costs only its own statement in ParseScript.
func (p *Parser) checkEscapes(t lexer.Token) {
	var msg string
	if t.Escape {
		msg = lexer.CheckEscapeString(t.Value)
	} else {
		esc := byte('\\')
		if next := p.peekN(2); p.isKwAt(1, "UESCAPE") && next.Kind == lexer.String {
			if !lexer.ValidUEscape(next.Value) {
				p.errorf(next.Pos, "invalid Unicode escape character")
			}
			esc = next.Value[0]
		}
		msg = lexer.CheckUnicodeString(t.Value, esc)
	}
	if msg != "" {
		p.errorf(t.Pos, "%s", msg)
	}
}

// checkContext aborts the parse once the caller's context is done. Checking
// ctx.Err() on every token would add needless overhead, so it's sampled
// every 1024 tokens.
func (p *Parser) checkContext() {
	if p.ctx == nil {
		return
	}
	p.steps++
	if p.steps&1023 != 0 {
		return
	}
	if err := p.ctx.Err(); err != nil {
		p.errorf(p.peek().Pos, "parse aborted: %v", err)
	}
}

// isKw reports whether the current token is the bare word kw (upper-cased).
func (p *Parser) isKw(kws ...string) bool { return p.isKwAt(0, kws...) }

func (p *Parser) isKwAt(n int, kws ...string) bool {
	t := p.peekN(n)
	if t.Kind != lexer.Ident {
		return false
	}
	for _, kw := range kws {
		if t.Value == kw {
			return true
		}
	}
	return false
}

// isKwSeq reports whether the upcoming tokens spell the given words.
func (p *Parser) isKwSeq(kws ...string) bool {
	for i, kw := range kws {
		if !p.isKwAt(i, kw) {
			return false
		}
	}
	return true
}

func (p *Parser) acceptKw(kw string) bool {
	if p.isKw(kw) {
		p.next()
		return true
	}
	return false
}

// acceptKwSeq consumes the whole sequence if it is present.
func (p *Parser) acceptKwSeq(kws ...string) bool {
	if !p.isKwSeq(kws...) {
		return false
	}
	p.pos += len(kws)
	return true
}

func (p *Parser) expectKw(kws ...string) {
	for _, kw := range kws {
		if !p.acceptKw(kw) {
			p.unexpected(kw)
		}
	}
}

// acceptOneOf consumes one of the words and returns it, or "".
func (p *Parser) acceptOneOf(kws ...string) string {
	for _, kw := range kws {
		if p.acceptKw(kw) {
			return kw
		}
	}
	return ""
}

func (p *Parser) isOp(op string) bool {
	t := p.peek()
	return t.Kind == lexer.Op && t.Text == op
}

func (p *Parser) isOpAt(n int, op string) bool {
	t := p.peekN(n)
	return t.Kind == lexer.Op && t.Text == op
}

func (p *Parser) acceptOp(op string) bool {
	if p.isOp(op) {
		p.next()
		return true
	}
	return false
}

func (p *Parser) expectOp(op string) {
	if !p.acceptOp(op) {
		p.unexpected(fmt.Sprintf("%q", op))
	}
}

// --- errors -----------------------------------------------------------------

func (p *Parser) errorf(pos int, format string, args ...any) {
	panic(p.errorAt(pos, fmt.Sprintf(format, args...)))
}

func (p *Parser) errorAt(pos int, msg string) *Error {
	line, col := p.lineCol(pos)
	return &Error{Pos: pos, Line: line, Column: col, Msg: msg}
}

// lineCol converts a byte offset to a 1-based line and a 1-based column in
// characters. The line index is built once, and a lookup after an earlier
// position on the same line counts on from there, so a script of many
// statements on one line stays linear.
func (p *Parser) lineCol(pos int) (line, col int) {
	if p.lineStarts == nil {
		p.lineStarts = []int{0}
		for i := range len(p.src) {
			if p.src[i] == '\n' {
				p.lineStarts = append(p.lineStarts, i+1)
			}
		}
	}
	pos = min(pos, len(p.src))
	line = sort.SearchInts(p.lineStarts, pos+1)
	from, col := p.lineStarts[line-1], 1
	if p.lastPos >= from && p.lastPos <= pos && p.lastCol > 0 {
		from, col = p.lastPos, p.lastCol
	}
	col += utf8.RuneCountInString(p.src[from:pos])
	p.lastPos, p.lastCol = pos, col
	return line, col
}

func (p *Parser) unexpected(expected string) {
	t := p.peek()
	if t.Kind == lexer.EOF {
		p.errorf(t.Pos, "unexpected end of input, expected %s", expected)
	}
	p.errorf(t.Pos, "unexpected %s, expected %s", describe(t), expected)
}

func (p *Parser) unsupported(what string) {
	p.errorf(p.peek().Pos, "%s is not supported in dialect %s", what, p.d.Name)
}

func describe(t lexer.Token) string {
	switch t.Kind {
	case lexer.Ident:
		return fmt.Sprintf("%q", t.Text)
	case lexer.Op:
		return fmt.Sprintf("%q", t.Text)
	default:
		return fmt.Sprintf("%s %q", strings.ToLower(t.Kind.String()), t.Text)
	}
}

// --- identifiers -------------------------------------------------------------

// canBeIdent reports whether the current token may start a bare identifier.
func (p *Parser) canBeIdent() bool {
	t := p.peek()
	switch t.Kind {
	case lexer.QuotedIdent:
		return true
	case lexer.String:
		return p.d.Features.Names.StringAlias
	case lexer.Ident:
		return !p.d.IsReserved(t.Value)
	}
	return false
}

// parseIdent parses a non-reserved identifier.
func (p *Parser) parseIdent() ast.Ident {
	if !p.canBeIdent() {
		p.unexpected("identifier")
	}
	return p.parseAnyIdent()
}

// parseAnyIdent parses an identifier, accepting any keyword (PostgreSQL's
// ColLabel, used after AS and after a '.').
func (p *Parser) parseAnyIdent() ast.Ident {
	t := p.peek()
	switch t.Kind {
	case lexer.QuotedIdent:
		p.next()
		id := ast.Ident{Name: t.Value, Quoted: true, Unicode: t.Unicode}
		if t.Unicode && p.acceptKw("UESCAPE") {
			id.UEscape = p.parseStringValue()
		}
		return id
	case lexer.String:
		if p.d.Features.Names.StringAlias {
			p.next()
			return ast.Ident{Name: t.Value, Quoted: true}
		}
	case lexer.Ident:
		p.next()
		return ast.Ident{Name: t.Text}
	}
	p.unexpected("identifier")
	return ast.Ident{}
}

// parseIdentText parses an identifier and returns it as it must be printed.
func (p *Parser) parseIdentText() string { return identText(p.parseIdent()) }

func identText(id ast.Ident) string {
	if id.Quoted {
		return `"` + strings.ReplaceAll(id.Name, `"`, `""`) + `"`
	}
	return id.Name
}

// parseLabel parses a name after AS or a dot: any keyword in dialects with
// PostgreSQL's ColLabel rule, otherwise an ordinary identifier.
func (p *Parser) parseLabel() ast.Ident {
	if p.d.Features.Names.KeywordLabels {
		return p.parseAnyIdent()
	}
	return p.parseIdent()
}

// parseOpaque keeps the rest of the statement verbatim. The first verbWords
// tokens name the statement. Only parenthesis balance is checked.
func (p *Parser) parseOpaque(verbWords int) ast.Statement {
	words := make([]string, 0, verbWords)
	for i := 0; i < verbWords; i++ {
		words = append(words, p.peekN(i).Value)
	}
	return &ast.Opaque{Verb: strings.Join(words, " "), Text: p.takeRaw(false)}
}

// checkedOpaque returns the statement from token start up to the current
// position as an opaque statement whose grammar was checked.
func (p *Parser) checkedOpaque(start, verbWords int) *ast.Opaque {
	words := make([]string, 0, verbWords)
	for i := 0; i < verbWords; i++ {
		words = append(words, p.tokAt(start+i).Value)
	}
	last := p.peekN(-1)
	return &ast.Opaque{Verb: strings.Join(words, " "), Text: p.src[p.toks[start].Pos : last.Pos+len(last.Text)], Checked: true}
}

// takeRaw consumes tokens up to a top-level ';' (and, when stopAtComma, a
// top-level ','), returning the exact source text covered.
func (p *Parser) takeRaw(stopAtComma bool) string {
	start := p.peek().Pos
	end, depth := start, 0
	for {
		t := p.peek()
		if t.Kind == lexer.EOF {
			if depth > 0 {
				p.unexpected("closing bracket")
			}
			break
		}
		if t.Kind == lexer.Op {
			switch t.Text {
			case "(", "[":
				depth++
			case ")", "]":
				if depth == 0 {
					p.unexpected("end of statement")
				}
				depth--
			case ";":
				if depth == 0 {
					return p.src[start:end]
				}
			case ",":
				if depth == 0 && stopAtComma {
					return p.src[start:end]
				}
			}
		}
		end = t.Pos + len(t.Text)
		p.next()
	}
	return p.src[start:end]
}

// parseName parses a dotted name: a, a.b, a.b.c.
func (p *Parser) parseName() *ast.Name {
	n := &ast.Name{Parts: []ast.Ident{p.parseIdent()}}
	for p.isOp(".") && (p.peekN(1).Kind == lexer.Ident || p.peekN(1).Kind == lexer.QuotedIdent ||
		p.peekN(1).Kind == lexer.String && p.d.Features.Names.StringAlias) {
		if limit := p.d.Features.Names.MaxNameParts; limit > 0 && len(n.Parts) == limit {
			p.unexpected("end of name")
		}
		p.next()
		n.Parts = append(n.Parts, p.parseLabel())
	}
	return n
}

// parseObjectName parses a collation or a referenced table: dotted in
// PostgreSQL, a single name in SQLite's DDL.
func (p *Parser) parseObjectName() *ast.Name {
	if !p.d.Features.DDL.ExtendedDDL {
		return &ast.Name{Parts: []ast.Ident{p.parseIdent()}}
	}
	return p.parseName()
}

// parseRelationName parses the name of a table, view, sequence or index:
// at most catalog.schema.name in PostgreSQL.
func (p *Parser) parseRelationName() *ast.Name {
	pos := p.peek().Pos
	n := p.parseName()
	p.checkRelationParts(n, pos)
	return n
}

// checkRelationParts rejects a table name, starting at source offset pos,
// with more parts than the dialect allows.
func (p *Parser) checkRelationParts(n *ast.Name, pos int) {
	if limit := p.d.Features.Names.MaxRelationParts; limit > 0 && len(n.Parts) > limit {
		p.errorf(pos, "improper qualified name (too many dotted names)")
	}
}

func (p *Parser) parseIdentList() []ast.Ident {
	ids := []ast.Ident{p.parseIdent()}
	for p.acceptOp(",") {
		ids = append(ids, p.parseIdent())
	}
	return ids
}

func (p *Parser) parseParenIdentList() []ast.Ident {
	p.expectOp("(")
	ids := p.parseIdentList()
	p.expectOp(")")
	return ids
}

func (p *Parser) parseNameList() []*ast.Name {
	ns := []*ast.Name{p.parseName()}
	for p.acceptOp(",") {
		ns = append(ns, p.parseName())
	}
	return ns
}

// parseStringValue expects a string literal token and returns its content,
// with E'...' escapes resolved because the caller keeps only the text.
func (p *Parser) parseStringValue() string {
	t := p.peek()
	if t.Kind != lexer.String {
		p.unexpected("string literal")
	}
	p.next()
	if t.Escape {
		return lexer.Unescape(t.Value)
	}
	return t.Value
}

// parseStringLit parses a string literal, keeping its E” or U&” form.
func (p *Parser) parseStringLit() *ast.Literal {
	t := p.peek()
	if t.Kind != lexer.String {
		p.unexpected("string literal")
	}
	p.next()
	lit := &ast.Literal{Kind: ast.StringLit, Value: t.Value, Escape: t.Escape, Unicode: t.Unicode}
	if t.Unicode && p.acceptKw("UESCAPE") {
		lit.UEscape = p.parseStringValue()
	}
	return lit
}

// isInteger reports whether a number token has no fraction or exponent.
func isInteger(t lexer.Token) bool {
	return !strings.ContainsAny(t.Text, ".eE") || strings.HasPrefix(strings.ToLower(t.Text), "0x")
}

// parseIfExists consumes IF EXISTS.
func (p *Parser) parseIfExists() bool {
	ok := p.acceptKwSeq("IF", "EXISTS")
	if !ok {
		p.rejectBareIf()
	}
	return ok
}

// rejectBareIf: IF is an ordinary name in SQLite, except where IF [NOT]
// EXISTS may start.
func (p *Parser) rejectBareIf() {
	if p.d.Features.Expr.FreeFormTypeNames && p.isKw("IF") {
		p.unexpected("name")
	}
}

// parseIfNotExists consumes IF NOT EXISTS.
func (p *Parser) parseIfNotExists() bool {
	ok := p.acceptKwSeq("IF", "NOT", "EXISTS")
	p.rejectBareIf() // SQLite cannot read back the schema of an object named if
	return ok
}

// parseBehavior consumes CASCADE or RESTRICT.
func (p *Parser) parseBehavior() string { return p.acceptOneOf("CASCADE", "RESTRICT") }

// --- statements ---------------------------------------------------------------

func (p *Parser) parseStatement() ast.Statement {
	t := p.peek()
	if t.Kind == lexer.Op && t.Text == "(" && p.d.Features.Query.ParenQueries {
		return p.parseQuery()
	}
	if t.Kind != lexer.Ident {
		p.unexpected("statement")
	}
	if p.d.Features.Utility.ServerUtilities {
		if s := p.parseServerUtility(t.Value); s != nil {
			return s
		}
	}
	switch t.Value {
	case "SELECT", "VALUES":
		return p.parseQuery()
	case "TABLE":
		if p.d.Features.Query.TableKeyword {
			return p.parseQuery()
		}
	case "WITH":
		return p.parseWithStatement()
	case "INSERT", "REPLACE":
		return p.parseInsert(nil)
	case "UPDATE":
		return p.parseUpdate(nil)
	case "DELETE":
		return p.parseDelete(nil)
	case "MERGE":
		if p.d.Features.DML.Merge {
			return p.parseMerge(nil)
		}
	case "CREATE":
		return p.parseCreate()
	case "DROP":
		return p.parseDrop()
	case "ALTER":
		return p.parseAlter()
	case "TRUNCATE":
		if p.d.Features.DML.Truncate {
			return p.parseTruncate()
		}
	case "BEGIN":
		return p.parseBegin()
	case "START":
		if p.d.Features.Utility.TransactionModes {
			return p.parseBegin()
		}
	case "COMMIT", "END":
		return p.parseCommit()
	case "ROLLBACK":
		return p.parseRollback()
	case "ABORT":
		if p.d.Features.Utility.TransactionModes {
			return p.parseRollback()
		}
	case "SAVEPOINT":
		p.next()
		return &ast.Savepoint{Name: p.parseIdent()}
	case "RELEASE":
		p.next()
		p.acceptSavepointWord()
		return &ast.ReleaseSavepoint{Name: p.parseIdent()}
	case "EXPLAIN":
		return p.parseExplain()
	case "SET":
		if p.d.Features.Utility.SetShowReset {
			return p.parseSet()
		}
	case "SHOW":
		if p.d.Features.Utility.SetShowReset {
			return p.parseShow()
		}
	case "RESET":
		if p.d.Features.Utility.SetShowReset {
			return p.parseReset()
		}
	case "PRAGMA":
		if p.d.Features.Utility.Pragma {
			return p.parsePragma()
		}
	case "ATTACH":
		if p.d.Features.Utility.AttachDetach {
			return p.parseAttach()
		}
	case "DETACH":
		if p.d.Features.Utility.AttachDetach {
			return p.parseDetach()
		}
	case "VACUUM":
		if p.d.Features.Utility.Vacuum {
			return p.parseVacuum()
		}
	case "ANALYZE", "ANALYSE":
		return p.parseAnalyze()
	case "REINDEX":
		if p.d.Features.Utility.Reindex {
			return p.parseReindex()
		}
	}
	p.errorf(t.Pos, "unexpected %q at start of statement", t.Text)
	return nil
}

// parseWithStatement handles WITH ... followed by SELECT/INSERT/UPDATE/DELETE.
func (p *Parser) parseWithStatement() ast.Statement {
	with := p.parseWith()
	switch {
	case p.isKw("INSERT", "REPLACE"):
		return p.parseInsert(with)
	case p.isKw("UPDATE"):
		return p.parseUpdate(with)
	case p.isKw("DELETE"):
		return p.parseDelete(with)
	case p.isKw("MERGE") && p.d.Features.DML.Merge:
		return p.parseMerge(with)
	}
	return p.parseQueryAfterWith(with)
}
