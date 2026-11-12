package parser

import (
	"strings"

	"sqlp/ast"
	"sqlp/lexer"
)

// --- transactions -------------------------------------------------------------

func (p *Parser) parseBegin() ast.Statement {
	f := p.d.Features.Utility
	b := &ast.Begin{}
	if p.acceptKw("START") {
		p.expectKw("TRANSACTION")
	} else {
		p.expectKw("BEGIN")
		if f.TransactionKinds {
			if m := p.acceptOneOf("DEFERRED", "IMMEDIATE", "EXCLUSIVE"); m != "" {
				b.Modes = append(b.Modes, m)
			}
			if p.acceptKw("TRANSACTION") && p.canBeIdent() {
				p.next() // SQLite ignores an optional transaction name
			}
			return b
		}
		p.acceptOneOf("WORK", "TRANSACTION")
	}
	if f.TransactionModes {
		b.Modes = p.parseTransactionModes()
	}
	return b
}

func (p *Parser) parseTransactionModes() []string {
	var modes []string
	for {
		switch {
		case p.acceptKwSeq("ISOLATION", "LEVEL"):
			switch {
			case p.acceptKwSeq("READ", "COMMITTED"):
				modes = append(modes, "ISOLATION LEVEL READ COMMITTED")
			case p.acceptKwSeq("READ", "UNCOMMITTED"):
				modes = append(modes, "ISOLATION LEVEL READ UNCOMMITTED")
			case p.acceptKwSeq("REPEATABLE", "READ"):
				modes = append(modes, "ISOLATION LEVEL REPEATABLE READ")
			case p.acceptKw("SERIALIZABLE"):
				modes = append(modes, "ISOLATION LEVEL SERIALIZABLE")
			default:
				p.unexpected("isolation level")
			}
		case p.acceptKwSeq("READ", "ONLY"):
			modes = append(modes, "READ ONLY")
		case p.acceptKwSeq("READ", "WRITE"):
			modes = append(modes, "READ WRITE")
		case p.acceptKwSeq("NOT", "DEFERRABLE"):
			modes = append(modes, "NOT DEFERRABLE")
		case p.acceptKw("DEFERRABLE"):
			modes = append(modes, "DEFERRABLE")
		default:
			if prev := p.peekN(-1); len(modes) > 0 && prev.Kind == lexer.Op && prev.Text == "," {
				p.unexpected("transaction mode")
			}
			return modes
		}
		p.acceptOp(",")
	}
}

// acceptSavepointWord consumes an optional SAVEPOINT, which is unreserved and
// may itself be the savepoint's name.
func (p *Parser) acceptSavepointWord() {
	if p.isKw("SAVEPOINT") && (!p.d.Features.Utility.TransactionModes || !p.atStatementEndAt(1)) {
		p.next()
	}
}

func (p *Parser) atStatementEndAt(n int) bool {
	t := p.peekN(n)
	return t.Kind == lexer.EOF || t.Kind == lexer.Op && t.Text == ";"
}

// parseTxnWord consumes the optional WORK/TRANSACTION after COMMIT or ROLLBACK.
func (p *Parser) parseTxnWord() {
	if p.d.Features.Utility.TransactionModes {
		p.acceptOneOf("WORK", "TRANSACTION")
	} else if p.acceptKw("TRANSACTION") && p.canBeIdent() && !p.isKw("TO") {
		p.next() // SQLite ignores an optional transaction name
	}
}

func (p *Parser) parseChain() bool {
	if !p.d.Features.Utility.TransactionModes {
		return false
	}
	if p.acceptKwSeq("AND", "NO", "CHAIN") {
		return false
	}
	return p.acceptKwSeq("AND", "CHAIN")
}

func (p *Parser) parseCommit() ast.Statement {
	p.next() // COMMIT or END
	p.parseTxnWord()
	return &ast.Commit{Chain: p.parseChain()}
}

func (p *Parser) parseRollback() ast.Statement {
	p.next() // ROLLBACK or ABORT
	p.parseTxnWord()
	rb := &ast.Rollback{}
	if p.d.Features.Utility.Savepoints && p.acceptKw("TO") {
		p.acceptSavepointWord()
		id := p.parseIdent()
		rb.To = &id
		return rb
	}
	rb.Chain = p.parseChain()
	return rb
}

// --- EXPLAIN -------------------------------------------------------------------

func (p *Parser) parseExplain() ast.Statement {
	p.expectKw("EXPLAIN")
	ex := &ast.Explain{}
	f := p.d.Features.Utility
	if f.ExplainQueryPlan && p.acceptKwSeq("QUERY", "PLAN") {
		ex.QueryPlan = true
	}
	if f.ExplainOptions {
		if p.isOp("(") && !p.isOpAt(1, "(") && !p.isKwAt(1, "SELECT", "VALUES", "WITH", "TABLE") { // else (SELECT 1)
			ex.Options = p.parseUtilityOptions(false)
		} else {
			ex.Analyze = p.acceptKw("ANALYZE") || p.acceptKw("ANALYSE")
			ex.Verbose = p.acceptKw("VERBOSE")
		}
	}
	ex.Statement = p.parseExplainable()
	return ex
}

// parseExplainable parses the statement under EXPLAIN. SQLite takes anything
// but another EXPLAIN; PostgreSQL has a fixed list.
func (p *Parser) parseExplainable() ast.Statement {
	if !p.d.Features.Utility.ExplainOptions {
		if p.isKw("EXPLAIN") {
			p.unexpected("statement other than EXPLAIN")
		}
		return p.parseStatement()
	}
	if !p.isOp("(") && !p.isKw("SELECT", "VALUES", "WITH", "TABLE", "INSERT", "UPDATE", "DELETE", "MERGE",
		"DECLARE", "EXECUTE", "REFRESH", "CREATE") {
		p.unexpected("explainable statement")
	}
	start := p.peek().Pos
	st := p.parseStatement()
	switch st := st.(type) {
	case *ast.CreateTable:
		if st.AsQuery != nil || st.AsExecute != nil {
			return st
		}
	case *ast.CreateView:
		if st.Materialized {
			return st
		}
	case *ast.Query, *ast.Insert, *ast.Update, *ast.Delete, *ast.Merge, *ast.Opaque, *ast.Execute, *ast.RefreshMatView:
		return st
	}
	p.errorf(start, "statement is not explainable")
	return nil
}

// parseUtilityOptions parses PostgreSQL's (NAME [value], ...) option lists.
// anyName allows reserved words as names, as COPY does.
func (p *Parser) parseUtilityOptions(anyName bool) []ast.Option {
	p.expectOp("(")
	var opts []ast.Option
	for {
		t := p.peek()
		if !anyName && t.Kind == lexer.Ident && p.d.IsReserved(t.Value) && !p.d.FuncNameKeywords[t.Value] && !p.isKw("ANALYZE", "ANALYSE") {
			p.unexpected("option name")
		}
		opt := ast.Option{Name: strings.ToUpper(p.parseAnyIdent().Name)}
		if !p.isOp(",") && !p.isOp(")") {
			opt.Value = p.parseOptionValue()
		}
		opts = append(opts, opt)
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp(")")
	return opts
}

// parseOptionValue parses a bare word, string, number, * or parenthesized list.
func (p *Parser) parseOptionValue() ast.Expr {
	if p.acceptOp("*") {
		return &ast.Star{}
	}
	if p.isOp("(") {
		return p.parseExpr()
	}
	return p.parseSetValue()
}

// --- SET / SHOW / RESET --------------------------------------------------------

// timezoneName is the GUC behind SET/SHOW/RESET TIME ZONE.
var timezoneName = &ast.Name{Parts: []ast.Ident{{Name: "timezone"}}}

func (p *Parser) parseSet() ast.Statement {
	// k is the word after SET and an optional LOCAL or SESSION, which a
	// variable may also be called.
	k := 1
	if (p.isKwAt(1, "LOCAL") || p.isKwAt(1, "SESSION") && !p.isKwAt(2, "AUTHORIZATION", "CHARACTERISTICS")) &&
		!p.isKwAt(2, "TO", "FROM") && !p.isOpAt(2, "=") && !p.isOpAt(2, ".") {
		k = 2
	}
	generic := p.isKwAt(k+1, "TO", "FROM") || p.isOpAt(k+1, "=") || p.isOpAt(k+1, ".")
	if !generic && (p.isKwAt(k, "ROLE", "NAMES", "XML", "SCHEMA", "CATALOG", "TRANSACTION") ||
		p.isKwAt(k, "SESSION") && p.isKwAt(k+1, "AUTHORIZATION", "CHARACTERISTICS") || k == 1 && p.isKwAt(1, "CONSTRAINTS")) {
		return p.parseSetSpecial(k)
	}
	p.expectKw("SET")
	s := &ast.Set{}
	if k == 2 {
		s.Scope = p.next().Value
	}
	if p.acceptKwSeq("TIME", "ZONE") {
		s.Name, s.TimeZone = timezoneName, true
		if p.acceptKw("DEFAULT") || p.acceptKw("LOCAL") {
			s.Default = true
			return s
		}
		s.Values = []ast.Expr{p.parseTimeZoneValue()}
		return s
	}
	s.Name = p.parseName()
	if p.acceptKwSeq("FROM", "CURRENT") {
		s.FromCurrent = true
		return s
	}
	if !p.acceptKw("TO") {
		p.expectOp("=")
	}
	if p.acceptKw("DEFAULT") {
		s.Default = true
		return s
	}
	s.Values = []ast.Expr{p.parseSetValue()}
	for p.acceptOp(",") {
		s.Values = append(s.Values, p.parseSetValue())
	}
	return s
}

// parseSetSpecial checks the forms of SET with grammar of their own
// (set_rest_more and ConstraintsSetStmt in gram.y) and keeps them verbatim.
// k is the index of the word after SET and the scope.
func (p *Parser) parseSetSpecial(k int) ast.Statement {
	start := p.pos
	verb := "SET " + p.peekN(k).Value
	p.pos += k
	expectString := func() {
		if p.peek().Kind != lexer.String {
			p.unexpected("string literal")
		}
		p.next()
	}
	modes := func() {
		if len(p.parseTransactionModes()) == 0 {
			p.unexpected("transaction mode")
		}
	}
	switch {
	case p.acceptKwSeq("TRANSACTION", "SNAPSHOT"):
		expectString()
	case p.acceptKw("TRANSACTION"):
		modes()
	case p.acceptKwSeq("SESSION", "CHARACTERISTICS"):
		p.expectKw("AS", "TRANSACTION")
		modes()
	case p.acceptKwSeq("SESSION", "AUTHORIZATION"), p.acceptKw("ROLE"):
		if !(p.peekN(-1).Value == "AUTHORIZATION" && p.acceptKw("DEFAULT")) {
			p.parseWordOrString()
		}
	case p.acceptKw("NAMES"):
		if !p.acceptKw("DEFAULT") && p.peek().Kind == lexer.String {
			p.next()
		}
	case p.acceptKw("SCHEMA"):
		expectString()
	case p.acceptKw("CATALOG"):
		pos := p.peekN(-1).Pos
		expectString()
		p.errorf(pos, "current database cannot be changed")
	case p.acceptKwSeq("XML", "OPTION"):
		if p.acceptOneOf("DOCUMENT", "CONTENT") == "" {
			p.unexpected("DOCUMENT or CONTENT")
		}
	case p.acceptKw("CONSTRAINTS"):
		if !p.acceptKw("ALL") {
			p.parseNameList()
		}
		if p.acceptOneOf("DEFERRED", "IMMEDIATE") == "" {
			p.unexpected("DEFERRED or IMMEDIATE")
		}
	}
	last := p.peekN(-1)
	return &ast.Opaque{Verb: verb, Text: p.src[p.toks[start].Pos : last.Pos+len(last.Text)]}
}

// parseTimeZoneValue parses zone_value: an interval literal as well as what
// any setting takes, except TRUE, FALSE and ON. (gram.y takes only an IDENT,
// so unreserved keywords such as year are wrongly accepted here.)
func (p *Parser) parseTimeZoneValue() ast.Expr {
	if p.isKw("TRUE", "FALSE", "ON") {
		p.unexpected("time zone")
	}
	if p.isKw("INTERVAL") {
		if e := p.tryTypedString(); e != nil {
			return e
		}
		p.unexpected("interval literal")
	}
	return p.parseSetValue()
}

// parseSetValue parses var_value: TRUE, FALSE, ON, a non-reserved word, a
// string or a number. pg_query also takes a parameter.
func (p *Parser) parseSetValue() ast.Expr {
	t := p.peek()
	switch {
	case t.Kind == lexer.Param:
		p.next()
		return &ast.Param{Text: t.Text}
	case p.isKw("TRUE", "FALSE", "ON"):
		p.next()
		if t.Value == "ON" {
			return &ast.Word{Text: t.Text}
		}
		return p.literalBool(t)
	case t.Kind == lexer.Ident:
		if p.d.IsReserved(t.Value) && !p.d.FuncNameKeywords[t.Value] {
			p.unexpected("value")
		}
		p.next()
		return &ast.Word{Text: t.Text}
	case t.Kind == lexer.String, t.Kind == lexer.QuotedIdent:
		return p.parsePrimary()
	}
	return p.parseSignedNumber()
}

func (p *Parser) literalBool(t lexer.Token) ast.Expr {
	if t.Value == "TRUE" {
		return &ast.Literal{Kind: ast.True}
	}
	return &ast.Literal{Kind: ast.False}
}

// parseWordOrExpr parses a bare word as a Word, anything else as an expression.
func (p *Parser) parseWordOrExpr() ast.Expr {
	t := p.peek()
	if t.Kind == lexer.Ident && !p.isKw("NULL", "TRUE", "FALSE") && !p.isOpAt(1, "(") && !p.isOpAt(1, ".") {
		p.next()
		return &ast.Word{Text: t.Text}
	}
	return p.parseExpr()
}

func (p *Parser) parseShow() ast.Statement {
	p.expectKw("SHOW")
	if p.acceptKw("ALL") {
		return &ast.Show{All: true}
	}
	if n := p.parseSpecialSetting(); n != nil {
		return &ast.Show{Name: n}
	}
	return &ast.Show{Name: p.parseName()}
}

// parseSpecialSetting parses the settings SHOW and RESET name with grammar of
// their own, returning the plain name that gives the same parse tree.
func (p *Parser) parseSpecialSetting() *ast.Name {
	word := ""
	switch {
	case p.acceptKwSeq("TIME", "ZONE"):
		return timezoneName
	case p.acceptKwSeq("TRANSACTION", "ISOLATION", "LEVEL"):
		word = "transaction_isolation"
	case p.acceptKwSeq("SESSION", "AUTHORIZATION"):
		word = "session_authorization"
	default:
		return nil
	}
	return &ast.Name{Parts: []ast.Ident{{Name: word}}}
}

func (p *Parser) parseReset() ast.Statement {
	p.expectKw("RESET")
	if p.acceptKw("ALL") {
		return &ast.Reset{All: true}
	}
	if n := p.parseSpecialSetting(); n != nil {
		return &ast.Reset{Name: n}
	}
	return &ast.Reset{Name: p.parseName()}
}

// --- SQLite utilities ---------------------------------------------------------

func (p *Parser) parsePragma() ast.Statement {
	p.expectKw("PRAGMA")
	pr := &ast.Pragma{Name: p.parseName()}
	switch {
	case p.acceptOp("="):
		pr.Value = p.parsePragmaValue()
	case p.acceptOp("("):
		pr.Call = true
		pr.Value = p.parsePragmaValue()
		p.expectOp(")")
	}
	return pr
}

// parsePragmaValue parses SQLite's nmnum: a signed number, a name, a string
// or one of the keywords ON, DELETE and DEFAULT.
func (p *Parser) parsePragmaValue() ast.Expr {
	t := p.peek()
	switch {
	case t.Kind == lexer.Number, p.isOp("-"), p.isOp("+"):
		return p.parseSignedNumber()
	case t.Kind == lexer.String:
		return p.parsePrimary()
	case p.isKw("ON", "DELETE", "DEFAULT"), t.Kind == lexer.Ident && !p.d.IsReserved(t.Value):
		p.next()
		return &ast.Word{Text: t.Text}
	case t.Kind == lexer.QuotedIdent:
		id := p.parseAnyIdent()
		return &ast.Name{Parts: []ast.Ident{id}}
	}
	p.unexpected("pragma value")
	return nil
}

func (p *Parser) parseAttach() ast.Statement {
	p.expectKw("ATTACH")
	p.acceptKw("DATABASE")
	a := &ast.Attach{Expr: p.parseExpr()}
	p.expectKw("AS")
	a.Schema = p.parseExpr()
	if p.acceptKw("KEY") {
		a.Key = p.parseExpr()
	}
	return a
}

func (p *Parser) parseDetach() ast.Statement {
	p.expectKw("DETACH")
	p.acceptKw("DATABASE")
	return &ast.Detach{Schema: p.parseExpr()}
}

func (p *Parser) parseMaintTargets() []*ast.MaintTarget {
	var targets []*ast.MaintTarget
	for {
		t := &ast.MaintTarget{Name: p.parseName()}
		if p.isOp("(") {
			t.Columns = p.parseParenIdentList()
		}
		targets = append(targets, t)
		if !p.acceptOp(",") {
			return targets
		}
	}
}

func (p *Parser) parseVacuum() ast.Statement {
	p.expectKw("VACUUM")
	v := &ast.Vacuum{}
	if p.d.Features.Utility.VacuumInto { // SQLite: VACUUM [schema] [INTO file]
		if p.canBeIdent() {
			id := p.parseIdent()
			v.Schema = &id
		}
		if p.acceptKw("INTO") {
			v.Into = p.parseExpr()
		}
		return v
	}
	if p.isOp("(") {
		v.Paren = p.parseUtilityOptions(false)
	} else {
		for _, opt := range []string{"FULL", "FREEZE", "VERBOSE"} { // gram.y fixes the order
			if p.acceptKw(opt) {
				v.Options = append(v.Options, opt)
			}
		}
		if p.acceptKw("ANALYZE") || p.acceptKw("ANALYSE") {
			v.Options = append(v.Options, "ANALYZE")
		}
	}
	if p.canBeIdent() {
		v.Tables = p.parseMaintTargets()
	}
	return v
}

func (p *Parser) parseAnalyze() ast.Statement {
	p.next() // ANALYZE / ANALYSE
	a := &ast.Analyze{}
	if p.d.Features.Utility.ExplainOptions {
		if p.isOp("(") {
			a.Paren = p.parseUtilityOptions(false)
		} else {
			a.Verbose = p.acceptKw("VERBOSE")
		}
		if p.canBeIdent() {
			a.Tables = p.parseMaintTargets()
		}
		return a
	}
	if p.canBeIdent() { // SQLite: a single [schema.]name
		a.Tables = []*ast.MaintTarget{{Name: p.parseName()}}
	}
	return a
}

func (p *Parser) parseReindex() ast.Statement {
	p.expectKw("REINDEX")
	r := &ast.Reindex{}
	if p.d.Features.DDL.ExtendedDDL {
		if p.isOp("(") {
			r.Paren = p.parseUtilityOptions(false)
		}
		r.Kind = p.acceptOneOf("TABLE", "INDEX", "SCHEMA", "DATABASE", "SYSTEM")
		if r.Kind == "" {
			p.unexpected("TABLE, INDEX, SCHEMA, DATABASE or SYSTEM")
		}
		r.Concurrently = p.acceptKw("CONCURRENTLY")
	}
	switch {
	case p.canBeIdent() && (r.Kind == "SCHEMA" || r.Kind == "DATABASE" || r.Kind == "SYSTEM"):
		r.Name = &ast.Name{Parts: []ast.Ident{p.parseIdent()}}
	case p.canBeIdent():
		r.Name = p.parseName()
	case r.Kind == "TABLE" || r.Kind == "INDEX" || r.Kind == "SCHEMA":
		p.unexpected("name")
	}
	return r
}
