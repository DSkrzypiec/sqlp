package parser

import (
	"strings"

	"sqlp/ast"
	"sqlp/lexer"
)

// opaqueUtilities lists statements kept verbatim (see ast.Opaque). Those in
// checkUtility have their grammar checked first.
var opaqueUtilities = []string{
	"CHECKPOINT", "CLOSE", "CLUSTER", "DECLARE", "DISCARD", "FETCH", "IMPORT", "LOAD", "MOVE",
	"REASSIGN", "SECURITY",
}

// checkUtility checks the grammar of a utility statement kept verbatim and
// returns it, or returns nil when the statement's grammar is not checked.
func (p *Parser) checkUtility(word string) ast.Statement {
	start := p.pos
	expectString := func() {
		if p.peek().Kind != lexer.String {
			p.unexpected("string literal")
		}
		p.next()
	}
	words := 1
	switch word {
	case "CHECKPOINT":
		p.next()
	case "CLOSE":
		p.next()
		if !p.acceptKw("ALL") {
			p.parseIdent()
		}
	case "DISCARD":
		p.next()
		if p.acceptOneOf("ALL", "TEMP", "TEMPORARY", "PLANS", "SEQUENCES") == "" {
			p.unexpected("ALL, TEMP, PLANS or SEQUENCES")
		}
	case "LOAD":
		p.next()
		expectString()
	case "PREPARE", "COMMIT", "ROLLBACK": // PREPARE TRANSACTION, COMMIT PREPARED, ROLLBACK PREPARED
		p.pos += 2
		words = 2
		expectString()
	case "REASSIGN":
		p.next()
		p.expectKw("OWNED", "BY")
		p.parseGrantee()
		for p.acceptOp(",") {
			p.parseGrantee()
		}
		p.expectKw("TO")
		p.parseGrantee()
	case "SECURITY", "IMPORT": // only the leading words are checked
		p.next()
		if word == "SECURITY" {
			p.expectKw("LABEL")
		} else {
			p.expectKw("FOREIGN", "SCHEMA")
		}
		p.pos = start
		return p.parseOpaque(2)
	case "DECLARE":
		p.next()
		p.parseIdent()
		for p.acceptOneOf("BINARY", "INSENSITIVE", "ASENSITIVE", "SCROLL") != "" || p.acceptKwSeq("NO", "SCROLL") {
			// cursor options, in any order
		}
		p.expectKw("CURSOR")
		if !p.acceptKwSeq("WITH", "HOLD") {
			p.acceptKwSeq("WITHOUT", "HOLD")
		}
		p.expectKw("FOR")
		p.parseQuery()
	case "FETCH", "MOVE":
		p.next()
		p.parseFetchArgs()
	default:
		return nil
	}
	return p.checkedOpaque(start, words)
}

// parseFetchArgs parses the direction and cursor of FETCH and MOVE.
func (p *Parser) parseFetchArgs() {
	signed := func() bool {
		if p.peekN(p.signLen()).Kind == lexer.Number {
			p.parseSignedNumber()
			return true
		}
		return false
	}
	switch {
	case p.acceptOneOf("NEXT", "PRIOR", "FIRST", "LAST") != "":
	case p.acceptOneOf("ABSOLUTE", "RELATIVE") != "":
		if !signed() {
			p.unexpected("number")
		}
	case p.acceptOneOf("FORWARD", "BACKWARD") != "":
		if !p.acceptKw("ALL") {
			signed()
		}
	case p.acceptKw("ALL"), signed():
	}
	p.acceptOneOf("FROM", "IN") // then the cursor, after a direction or alone
	p.parseIdent()
}

// signLen is 1 when the current token is a sign, else 0.
func (p *Parser) signLen() int {
	if p.isOp("-") || p.isOp("+") {
		return 1
	}
	return 0
}

// parseServerUtility dispatches PostgreSQL's server-side statements. It
// returns nil when word does not start one.
func (p *Parser) parseServerUtility(word string) ast.Statement {
	switch word {
	case "GRANT", "REVOKE":
		return p.parseGrant()
	case "COPY":
		return p.parseCopy()
	case "COMMENT":
		return p.parseCommentOn()
	case "DO":
		return p.parseDo()
	case "CALL":
		p.next()
		pos := p.peek().Pos
		fc := p.parseFuncCall(p.parseName())
		if fc.Over != nil || fc.Filter != nil || len(fc.WithinGroup) > 0 {
			p.errorf(pos, "CALL takes a plain function call")
		}
		return &ast.Call{Func: fc}
	case "PREPARE":
		if p.isKwAt(1, "TRANSACTION") {
			return p.checkUtility("PREPARE")
		}
		return p.parsePrepare()
	case "EXECUTE":
		p.next()
		e := &ast.Execute{Name: p.parseIdent()}
		if p.acceptOp("(") {
			e.Args = p.parseExprList()
			p.expectOp(")")
		}
		return e
	case "DEALLOCATE":
		p.next()
		p.acceptKw("PREPARE")
		if p.acceptKw("ALL") {
			return &ast.Deallocate{All: true}
		}
		id := p.parseIdent()
		return &ast.Deallocate{Name: &id}
	case "LISTEN":
		p.next()
		return &ast.Listen{Channel: p.parseIdent()}
	case "UNLISTEN":
		p.next()
		if p.acceptOp("*") {
			return &ast.Unlisten{}
		}
		id := p.parseIdent()
		return &ast.Unlisten{Channel: &id}
	case "NOTIFY":
		p.next()
		n := &ast.Notify{Channel: p.parseIdent()}
		if p.acceptOp(",") {
			s := p.parseStringValue()
			n.Payload = &s
		}
		return n
	case "LOCK":
		return p.parseLock()
	case "REFRESH":
		p.next()
		p.expectKw("MATERIALIZED", "VIEW")
		r := &ast.RefreshMatView{Concurrently: p.acceptKw("CONCURRENTLY")}
		r.Name = p.parseName()
		r.WithData = p.parseWithData()
		return r
	}
	if (word == "COMMIT" || word == "ROLLBACK") && p.isKwAt(1, "PREPARED") {
		return p.checkUtility(word)
	}
	for _, kw := range opaqueUtilities {
		if word == kw {
			if st := p.checkUtility(word); st != nil {
				return st
			}
			return p.parseOpaque(1)
		}
	}
	return nil
}

// --- GRANT / REVOKE -------------------------------------------------------------

func (p *Parser) parseGrant() ast.Statement {
	start := p.pos
	g := &ast.Grant{Revoke: p.next().Value == "REVOKE"}
	if g.Revoke {
		switch {
		case p.isKw("GRANT") && p.isKwAt(1, "OPTION"): // GRANT is reserved: only privileges ON objects
			p.pos += 2
			p.expectKw("FOR")
			g.OptionFor = "GRANT"
		case p.canBeIdent() && p.isKwAt(1, "OPTION") && p.isKwAt(2, "FOR"): // ADMIN, INHERIT, SET: only roles
			g.OptionFor = p.next().Value
			p.pos += 2
		}
	}
	if p.acceptKw("ALL") {
		g.All = true
		p.acceptKw("PRIVILEGES")
		if p.isOp("(") { // GRANT ALL (col) ON t
			g.Privileges = []*ast.Privilege{{Columns: p.parseParenIdentList()}}
		}
	} else {
		g.Privileges = []*ast.Privilege{p.parsePrivilege()}
		for p.acceptOp(",") {
			g.Privileges = append(g.Privileges, p.parsePrivilege())
		}
	}
	if g.All && !p.isKw("ON") { // ALL is no role
		p.expectKw("ON")
	}
	if p.isKw("ON") {
		// Objects with their own syntax are kept verbatim: signatures,
		// large objects, foreign servers and data wrappers, parameters.
		if p.isKwAt(1, "FUNCTION", "PROCEDURE", "ROUTINE", "LARGE", "FOREIGN", "PARAMETER") {
			p.pos = start
			return p.parseOpaque(1)
		}
		p.next()
		p.parseGrantObjects(g)
	} else {
		if g.OptionFor == "GRANT" {
			p.expectKw("ON")
		}
		g.IsRole = true
	}
	if g.Revoke {
		p.expectKw("FROM")
	} else {
		p.expectKw("TO")
	}
	g.Grantees = []ast.Expr{p.parseGrantee()}
	for p.acceptOp(",") {
		g.Grantees = append(g.Grantees, p.parseGrantee())
	}
	if !g.Revoke && p.acceptKw("WITH") {
		if g.IsRole {
			g.WithOption = p.parseGrantWith()
		} else { // privileges on objects: only WITH GRANT OPTION
			p.expectKw("GRANT", "OPTION")
			g.WithOption = "GRANT OPTION"
		}
	}
	if p.acceptKwSeq("GRANTED", "BY") {
		g.GrantedBy = p.parseGrantee()
	}
	if !g.IsRole && g.OptionFor != "" && g.OptionFor != "GRANT" {
		p.unexpected("GRANT OPTION FOR")
	}
	if g.Revoke {
		g.Behavior = p.parseBehavior()
	}
	return g
}

// parseGrantWith parses `name {OPTION|TRUE|FALSE}, ...` after WITH.
func (p *Parser) parseGrantWith() string {
	var parts []string
	for {
		name := p.parseAnyIdent().Name
		val := p.acceptOneOf("OPTION", "TRUE", "FALSE")
		if val == "" {
			p.unexpected("OPTION, TRUE or FALSE")
		}
		parts = append(parts, strings.ToUpper(name)+" "+val)
		if !p.acceptOp(",") {
			return strings.Join(parts, ", ")
		}
	}
}

func (p *Parser) parsePrivilege() *ast.Privilege {
	var name ast.Ident
	if p.isKw("SELECT", "REFERENCES", "CREATE") {
		name = p.parseAnyIdent()
	} else {
		name = p.parseIdent()
	}
	pr := &ast.Privilege{Name: name}
	if p.isOp("(") {
		pr.Columns = p.parseParenIdentList()
	}
	return pr
}

func (p *Parser) parseGrantObjects(g *ast.Grant) {
	if p.isKw("ALL") && p.isKwAt(1, "TABLES", "SEQUENCES", "FUNCTIONS", "PROCEDURES", "ROUTINES") {
		p.next()
		g.AllIn = p.next().Value
		p.expectKw("IN", "SCHEMA")
		g.Objects = p.parseNameList()
		return
	}
	if !p.isKwAt(1, "TO", "FROM") && !p.isOpAt(1, ",") && !p.isOpAt(1, ".") { // else an object so named
		g.ObjectKind = p.acceptOneOf("TABLE", "SEQUENCE", "SCHEMA", "DATABASE", "DOMAIN", "LANGUAGE",
			"TABLESPACE", "TYPE", "FUNCTION", "PROCEDURE", "ROUTINE")
	}
	g.Objects = []*ast.Name{p.parseGrantObject()}
	for p.acceptOp(",") {
		g.Objects = append(g.Objects, p.parseGrantObject())
	}
}

// parseGrantObject parses a name, skipping a function signature if present.
func (p *Parser) parseGrantObject() *ast.Name {
	n := p.parseName()
	if p.isOp("(") {
		p.unsupported("GRANT on a function signature")
	}
	return n
}

// parseGrantee parses a role name or PUBLIC, CURRENT_USER, ...
func (p *Parser) parseGrantee() ast.Expr {
	p.acceptKw("GROUP")
	if p.isKw("PUBLIC", "CURRENT_USER", "SESSION_USER", "CURRENT_ROLE") {
		return &ast.Word{Text: p.next().Value}
	}
	if t := p.peek(); t.Kind == lexer.Ident && p.d.FuncNameKeywords[t.Value] {
		return &ast.Word{Text: p.next().Text}
	}
	return p.parseName()
}

// --- COPY -----------------------------------------------------------------------

func (p *Parser) parseCopy() ast.Statement {
	p.expectKw("COPY")
	c := &ast.Copy{}
	if p.acceptOp("(") {
		c.Query = p.parsePreparable()
		p.expectOp(")")
	} else {
		c.Binary = p.acceptKw("BINARY")
		c.Table = p.parseName()
		if p.isOp("(") {
			c.Columns = p.parseParenIdentList()
		}
	}
	switch p.acceptOneOf("FROM", "TO") {
	case "TO":
		c.To = true
	case "":
		p.unexpected("FROM or TO")
	}
	c.Program = p.acceptKw("PROGRAM")
	switch t := p.peek(); {
	case t.Kind == lexer.String:
		c.Target = p.parseStringLit()
	case p.isKw("STDIN", "STDOUT"):
		c.Target = &ast.Word{Text: p.next().Value}
	default:
		p.unexpected("file name, STDIN or STDOUT")
	}
	if c.Query != nil && !c.To {
		p.unexpected("TO after a COPY query")
	}
	if c.Query == nil && (p.isKw("DELIMITERS") || p.isKwSeq("USING", "DELIMITERS")) {
		p.acceptKw("USING")
		p.next()
		c.Delimiters = p.parseStringLit()
	}
	c.LegacyWith = p.acceptKw("WITH")
	if p.isOp("(") {
		c.LegacyWith = false
		c.ParenOpts = p.parseUtilityOptions(true)
	} else {
		c.LegacyOpts = p.parseLegacyCopyOptions()
	}
	if !c.To && p.acceptKw("WHERE") {
		c.Where = p.parseExpr()
	}
	return c
}

func (p *Parser) parseLegacyCopyOptions() []ast.Option {
	var opts []ast.Option
	for {
		switch {
		case p.isKw("BINARY", "CSV", "HEADER", "FREEZE", "OIDS"):
			opts = append(opts, ast.Option{Name: p.next().Value})
		case p.isKw("FORCE"):
			opts = append(opts, p.parseForceOption())
		case p.isKw("DELIMITER", "NULL", "QUOTE", "ESCAPE", "ENCODING"):
			name := p.next().Value
			p.acceptKw("AS")
			opts = append(opts, ast.Option{Name: name, Value: p.parseStringLit()})
		default:
			return opts
		}
	}
}

// parseForceOption parses FORCE QUOTE cols|*, FORCE [NOT] NULL cols.
func (p *Parser) parseForceOption() ast.Option {
	p.expectKw("FORCE")
	var name string
	switch {
	case p.acceptKw("QUOTE"):
		name = "FORCE QUOTE"
		if p.acceptOp("*") {
			return ast.Option{Name: name, Value: &ast.Star{}}
		}
	case p.acceptKwSeq("NOT", "NULL"):
		name = "FORCE NOT NULL"
	case p.acceptKw("NULL"):
		name = "FORCE NULL"
	default:
		p.unexpected("QUOTE, NOT NULL or NULL")
	}
	var cols []string
	for {
		cols = append(cols, p.parseIdentText())
		if !p.acceptOp(",") {
			break
		}
	}
	return ast.Option{Name: name, Value: &ast.Word{Text: strings.Join(cols, ", ")}}
}

// --- COMMENT ON -------------------------------------------------------------------

func (p *Parser) parseCommentOn() ast.Statement {
	start := p.pos
	p.expectKw("COMMENT", "ON")
	c := &ast.CommentOn{}
	switch {
	case p.acceptKwSeq("MATERIALIZED", "VIEW"):
		c.Kind = "MATERIALIZED VIEW"
	default:
		c.Kind = p.acceptOneOf("TABLE", "COLUMN", "INDEX", "VIEW", "SCHEMA", "DATABASE", "SEQUENCE",
			"TYPE", "DOMAIN", "ROLE", "EXTENSION", "TRIGGER", "CONSTRAINT", "RULE", "POLICY", "TABLESPACE")
	}
	if c.Kind == "" { // functions, aggregates, operators, ...
		p.pos = start
		return p.parseOpaque(3)
	}
	switch {
	case c.Kind == "TYPE" || c.Kind == "DOMAIN":
		c.Type = p.parseType()
	case singlePartKinds[c.Kind]:
		c.Object = &ast.Name{Parts: []ast.Ident{p.parseIdent()}}
	default:
		c.Object = p.parseName()
	}
	switch c.Kind {
	case "CONSTRAINT", "TRIGGER", "RULE", "POLICY": // name ON table, and only these
		p.expectKw("ON")
		c.OnDomain = c.Kind == "CONSTRAINT" && p.acceptKw("DOMAIN")
		c.On = p.parseName()
	}
	p.expectKw("IS")
	if p.acceptKw("NULL") {
		c.Comment = &ast.Literal{Kind: ast.Null}
	} else {
		c.Comment = p.parseStringLit()
	}
	return c
}

// --- procedural ---------------------------------------------------------------------

// parseWordOrString parses `plpgsql` or `'plpgsql'` and returns it as it must
// be printed.
func (p *Parser) parseWordOrString() string {
	if p.peek().Kind == lexer.String {
		return "'" + strings.ReplaceAll(p.parseStringValue(), "'", "''") + "'"
	}
	if t := p.peek(); t.Kind == lexer.Ident && p.d.IsReserved(t.Value) && !p.d.FuncNameKeywords[t.Value] {
		p.unexpected("non-reserved word or string")
	}
	return p.parseAnyIdent().Name
}

func (p *Parser) parseDo() ast.Statement {
	p.expectKw("DO")
	d := &ast.Do{}
	for len(d.Parts) == 0 || p.isKw("LANGUAGE") || p.peek().Kind == lexer.String {
		if p.acceptKw("LANGUAGE") {
			d.Parts = append(d.Parts, ast.DoPart{Language: p.parseWordOrString()})
		} else {
			d.Parts = append(d.Parts, ast.DoPart{Code: p.parseStringValue()})
		}
	}
	return d
}

func (p *Parser) parsePrepare() ast.Statement {
	p.expectKw("PREPARE")
	pr := &ast.Prepare{Name: p.parseIdent()}
	if p.acceptOp("(") {
		pr.Types = []*ast.DataType{p.parseType()}
		for p.acceptOp(",") {
			pr.Types = append(pr.Types, p.parseType())
		}
		p.expectOp(")")
	}
	p.expectKw("AS")
	pr.Statement = p.parsePreparable()
	return pr
}

// parsePreparable parses gram.y's PreparableStmt: a query, INSERT, UPDATE,
// DELETE or MERGE.
func (p *Parser) parsePreparable() ast.Statement {
	pos := p.peek().Pos
	st := p.parseStatement()
	switch st.(type) {
	case *ast.Query, *ast.Insert, *ast.Update, *ast.Delete, *ast.Merge:
	default:
		p.errorf(pos, "expected SELECT, VALUES, INSERT, UPDATE, DELETE or MERGE")
	}
	return st
}

func (p *Parser) parseLock() ast.Statement {
	p.expectKw("LOCK")
	p.acceptKw("TABLE")
	l := &ast.Lock{Tables: p.parseRelationList()}
	if p.acceptKw("IN") {
		modes := [][]string{
			{"ACCESS", "SHARE"}, {"ROW", "SHARE"}, {"ROW", "EXCLUSIVE"},
			{"SHARE", "UPDATE", "EXCLUSIVE"}, {"SHARE", "ROW", "EXCLUSIVE"},
			{"ACCESS", "EXCLUSIVE"}, {"SHARE"}, {"EXCLUSIVE"},
		}
		for _, m := range modes {
			if p.acceptKwSeq(m...) {
				l.Mode = joinWords(m)
				break
			}
		}
		if l.Mode == "" {
			p.unexpected("lock mode")
		}
		p.expectKw("MODE")
	}
	l.NoWait = p.acceptKw("NOWAIT")
	return l
}

func joinWords(ws []string) string {
	s := ws[0]
	for _, w := range ws[1:] {
		s += " " + w
	}
	return s
}

// --- CREATE FUNCTION / PROCEDURE --------------------------------------------------

func (p *Parser) parseCreateFunction(orReplace, procedure bool) ast.Statement {
	cf := &ast.CreateFunction{Procedure: procedure, OrReplace: orReplace, Name: p.parseName()}
	p.expectOp("(")
	if !p.isOp(")") {
		for {
			cf.Params = append(cf.Params, p.parseFuncParam())
			if !p.acceptOp(",") {
				break
			}
		}
	}
	p.expectOp(")")
	if !procedure && p.isKw("RETURNS") && !p.isKwAt(1, "NULL") {
		p.next()
		p.parseReturns(cf)
	}
	for p.parseFuncOption(cf) {
	}
	return cf
}

func (p *Parser) parseReturns(cf *ast.CreateFunction) {
	if p.acceptKw("TABLE") {
		p.expectOp("(")
		for {
			if p.d.ColNameKeywords[p.peek().Value] {
				p.unexpected("column name")
			}
			cf.ReturnsTable = append(cf.ReturnsTable, &ast.ColumnDef{Name: p.parseIdent(), Type: p.parseFuncType()})
			if !p.acceptOp(",") {
				break
			}
		}
		p.expectOp(")")
		return
	}
	cf.ReturnsSetOf = p.acceptKw("SETOF")
	cf.Returns = p.parseFuncType()
}

// parseFuncType parses a type, also allowing t.c%TYPE.
func (p *Parser) parseFuncType() *ast.DataType {
	dt := p.parseType()
	if p.isOp("%") && p.isKwAt(1, "TYPE") && strings.Contains(dt.Name, ".") {
		p.pos += 2
		dt.Name += "%TYPE"
	}
	return dt
}

func (p *Parser) parseFuncParam() *ast.FuncParam {
	fp := &ast.FuncParam{Mode: p.acceptOneOf("IN", "INOUT", "OUT", "VARIADIC")}
	if p.canBeIdent() && !p.startsMultiWordType() && !p.d.ColNameKeywords[p.peek().Value] {
		p.try(func() {
			id := p.parseIdent()
			if fp.Mode == "" { // the mode may also follow the name
				fp.Mode = p.acceptOneOf("IN", "INOUT", "OUT", "VARIADIC")
			}
			typ := p.parseFuncType()
			if !p.isOp(",") && !p.isOp(")") && !p.isOp("=") && !p.isKw("DEFAULT") {
				p.unexpected("parameter end")
			}
			fp.Name, fp.Type = &id, typ
		})
	}
	if fp.Type == nil {
		fp.Type = p.parseFuncType()
	}
	if p.acceptKw("DEFAULT") || p.acceptOp("=") {
		fp.Default = p.parseExpr()
	}
	return fp
}

// startsMultiWordType reports whether the next two words are one type name,
// such as `double precision`, rather than a parameter name and a type.
func (p *Parser) startsMultiWordType() bool {
	for _, second := range pgMultiWord[p.peek().Value] {
		if p.isKwAt(1, second) {
			return true
		}
	}
	return false
}

// parseFuncOption parses one function attribute; false when there are no more.
func (p *Parser) parseFuncOption(cf *ast.CreateFunction) bool {
	add := func(name string, values ...ast.Expr) bool {
		cf.Options = append(cf.Options, ast.FuncOption{Name: name, Values: values})
		return true
	}
	switch {
	case p.acceptKw("AS"):
		values := []ast.Expr{p.parseStringLit()}
		if p.acceptOp(",") {
			values = append(values, p.parseStringLit())
		}
		return add("AS", values...)
	case p.acceptKw("LANGUAGE"):
		return add("LANGUAGE", &ast.Word{Text: p.parseWordOrString()})
	case p.acceptKwSeq("NOT", "LEAKPROOF"):
		return add("NOT LEAKPROOF")
	case p.acceptKwSeq("CALLED", "ON", "NULL", "INPUT"):
		return add("CALLED ON NULL INPUT")
	case p.acceptKwSeq("RETURNS", "NULL", "ON", "NULL", "INPUT"):
		return add("RETURNS NULL ON NULL INPUT")
	case p.acceptKwSeq("EXTERNAL", "SECURITY"), p.acceptKw("SECURITY"):
		who := p.acceptOneOf("INVOKER", "DEFINER")
		if who == "" {
			p.unexpected("INVOKER or DEFINER")
		}
		return add("SECURITY " + who)
	case p.acceptKw("PARALLEL"):
		return add("PARALLEL", &ast.Word{Text: p.parseIdent().Name})
	case p.acceptKw("COST"):
		return add("COST", p.parseSignedNumber())
	case p.acceptKw("ROWS"):
		return add("ROWS", p.parseSignedNumber())
	case p.isKwSeq("TRANSFORM", "FOR"):
		p.next()
		var types []*ast.DataType
		for {
			p.expectKw("FOR", "TYPE")
			types = append(types, p.parseType())
			if !p.acceptOp(",") {
				break
			}
		}
		cf.Options = append(cf.Options, ast.FuncOption{Name: "TRANSFORM", Types: types})
		return true
	case p.acceptKw("RESET"):
		switch {
		case p.acceptKw("ALL"):
			return add("RESET ALL")
		case p.acceptKwSeq("TIME", "ZONE"):
			return add("RESET TIME ZONE")
		case p.acceptKwSeq("TRANSACTION", "ISOLATION", "LEVEL"):
			return add("RESET TRANSACTION ISOLATION LEVEL")
		case p.acceptKwSeq("SESSION", "AUTHORIZATION"):
			return add("RESET SESSION AUTHORIZATION")
		}
		return add("RESET " + nameText(p.parseName()))
	case p.acceptKw("SUPPORT"):
		return add("SUPPORT", p.parseName())
	case p.isKw("IMMUTABLE", "STABLE", "VOLATILE", "LEAKPROOF", "STRICT", "WINDOW"):
		return add(p.next().Value)
	case p.acceptKw("SET"):
		return p.parseFuncSetOption(add)
	case p.acceptKw("RETURN"):
		cf.Return = p.parseExpr()
		return false
	case p.acceptKwSeq("BEGIN", "ATOMIC"):
		cf.Atomic = true
		for !p.isKw("END") {
			if p.acceptOp(";") {
				continue
			}
			if p.acceptKw("RETURN") {
				cf.Body = append(cf.Body, &ast.ReturnStmt{Expr: p.parseExpr()})
			} else {
				cf.Body = append(cf.Body, p.parseStatement())
			}
			p.expectOp(";")
		}
		p.expectKw("END")
		return false
	}
	return false
}

func (p *Parser) parseFuncSetOption(add func(string, ...ast.Expr) bool) bool {
	if p.acceptKwSeq("TIME", "ZONE") {
		return add("SET TIME ZONE", p.parseTimeZoneValue())
	}
	name := nameText(p.parseName())
	if p.acceptKwSeq("FROM", "CURRENT") {
		return add("SET " + name + " FROM CURRENT")
	}
	if !p.acceptKw("TO") {
		p.expectOp("=")
	}
	values := []ast.Expr{p.parseWordOrExpr()}
	for p.acceptOp(",") {
		values = append(values, p.parseWordOrExpr())
	}
	return add("SET "+name+" TO", values...)
}
