package parser

import (
	"strings"

	"sqlp/ast"
	"sqlp/dialect"
	"sqlp/lexer"
)

// opaqueCreate lists CREATE targets accepted verbatim (see ast.Opaque).
var opaqueCreate = []string{
	"ACCESS", "AGGREGATE", "CAST", "COLLATION", "CONVERSION", "DATABASE", "DEFAULT", "EVENT",
	"FOREIGN", "GROUP", "LANGUAGE", "OPERATOR", "POLICY", "PROCEDURAL", "PUBLICATION", "RECURSIVE",
	"ROLE", "RULE", "SERVER", "STATISTICS", "SUBSCRIPTION", "TABLESPACE", "TEXT", "TRANSFORM",
	"TRUSTED", "USER",
}

// opaqueDrop lists DROP targets accepted verbatim (see ast.Opaque).
var opaqueDrop = []string{
	"ACCESS", "AGGREGATE", "CAST", "COLLATION", "CONVERSION", "DATABASE", "EVENT", "FOREIGN", "FUNCTION",
	"GROUP", "LANGUAGE", "OPERATOR", "OWNED", "POLICY", "PROCEDURAL", "PROCEDURE", "PUBLICATION",
	"ROUTINE", "RULE", "SERVER", "STATISTICS", "SUBSCRIPTION", "TABLESPACE", "TEXT", "TRANSFORM",
}

// orReplaceable lists the words that may follow CREATE OR REPLACE in
// PostgreSQL. TEMP and TEMPORARY must then be followed by VIEW.
var orReplaceable = []string{
	"VIEW", "TEMP", "TEMPORARY", "LOCAL", "GLOBAL", "UNLOGGED", "RECURSIVE", "TRIGGER", "CONSTRAINT",
	"FUNCTION", "PROCEDURE", "RULE", "AGGREGATE", "TRANSFORM", "LANGUAGE", "PROCEDURAL", "TRUSTED",
}

// opaqueAlter lists ALTER targets other than TABLE accepted verbatim.
var opaqueAlter = []string{
	"AGGREGATE", "COLLATION", "CONVERSION", "DATABASE", "DEFAULT", "DOMAIN", "EVENT", "EXTENSION",
	"FOREIGN", "FUNCTION", "GROUP", "INDEX", "LANGUAGE", "LARGE", "MATERIALIZED", "OPERATOR",
	"POLICY", "PROCEDURAL", "PROCEDURE", "PUBLICATION", "ROLE", "ROUTINE", "RULE", "SCHEMA",
	"SEQUENCE", "SERVER", "STATISTICS", "SUBSCRIPTION", "SYSTEM", "TABLESPACE", "TEXT", "TRIGGER",
	"TYPE", "USER", "VIEW",
}

// opaqueAlterAction lists ALTER TABLE actions accepted verbatim.
var opaqueAlterAction = []string{
	"OWNER", "ENABLE", "DISABLE", "ATTACH", "DETACH", "SET", "RESET", "CLUSTER", "VALIDATE",
	"REPLICA", "INHERIT", "NO", "FORCE", "OF", "NOT",
}

// --- CREATE --------------------------------------------------------------------

func (p *Parser) parseCreate() ast.Statement {
	start := p.pos
	p.expectKw("CREATE")
	ext := p.d.Features.ExtendedDDL
	orReplace := ext && p.acceptKwSeq("OR", "REPLACE")
	if orReplace && !p.isKw(orReplaceable...) {
		p.unexpected("an object that CREATE OR REPLACE allows")
	}
	if ext && p.isKw("GLOBAL", "LOCAL") && p.isKwAt(1, "TEMP", "TEMPORARY") {
		p.next()
	}
	temp := p.acceptKw("TEMP") || p.acceptKw("TEMPORARY")
	if temp && !p.isKw("TABLE", "VIEW", "RECURSIVE", "SEQUENCE") && !(p.d.Features.TempTriggers && p.isKw("TRIGGER")) {
		p.unexpected("TABLE, VIEW or another object that can be temporary")
	}
	unlogged := ext && !temp && p.acceptKw("UNLOGGED")
	if unlogged && !p.isKw("TABLE", "SEQUENCE", "VIEW", "MATERIALIZED", "RECURSIVE") {
		p.unexpected("TABLE, SEQUENCE or VIEW")
	}
	if orReplace && (temp || unlogged) && !p.isKw("VIEW", "RECURSIVE") {
		p.unexpected("VIEW")
	}
	switch {
	case p.acceptKw("TABLE"):
		return p.parseCreateTable(temp, unlogged)
	case p.isKw("UNIQUE", "INDEX"):
		return p.parseCreateIndex()
	case p.acceptKw("VIEW"):
		return p.parseCreateView(orReplace, temp, unlogged, false)
	case p.acceptKw("TRIGGER"):
		return p.parseCreateTrigger(orReplace, temp, false)
	case p.d.Features.VirtualTables && p.acceptKwSeq("VIRTUAL", "TABLE"):
		return p.parseCreateVirtualTable()
	}
	if ext {
		switch {
		case !orReplace && !temp && p.acceptKwSeq("MATERIALIZED", "VIEW"):
			return p.parseCreateView(orReplace, temp, unlogged, true)
		case p.isKwSeq("CONSTRAINT", "TRIGGER"):
			if orReplace {
				p.errorf(p.peek().Pos, "CREATE OR REPLACE CONSTRAINT TRIGGER is not supported")
			}
			p.pos += 2
			return p.parseCreateTrigger(orReplace, temp, true)
		case p.acceptKw("SCHEMA"):
			return p.parseCreateSchema()
		case p.acceptKw("SEQUENCE"):
			cs := &ast.CreateSequence{Temp: temp, Unlogged: unlogged, IfNotExists: p.parseIfNotExists()}
			cs.Name = p.parseName()
			cs.Options = p.parseSequenceOptions()
			return cs
		case p.acceptKw("TYPE"):
			return p.parseCreateType(start)
		case p.acceptKw("EXTENSION"):
			return p.parseCreateExtension()
		case p.acceptKw("DOMAIN"):
			return p.parseCreateDomain()
		case p.acceptKw("FUNCTION"):
			return p.parseCreateFunction(orReplace, false)
		case p.acceptKw("PROCEDURE"):
			return p.parseCreateFunction(orReplace, true)
		case p.isKw(opaqueCreate...):
			words := p.pos - start + 1
			if p.isKwSeq("RECURSIVE", "VIEW") { // needs its column names
				p.pos += 2
				p.parseRelationName()
				if !p.isOp("(") {
					p.unexpected(`"(" and the column names of a recursive view`)
				}
			}
			p.pos = start
			return p.parseOpaque(words)
		}
	}
	p.unexpected("TABLE, INDEX, VIEW or another object type")
	return nil
}

func (p *Parser) parseCreateTable(temp, unlogged bool) ast.Statement {
	ext := p.d.Features.ExtendedDDL
	ct := &ast.CreateTable{Temp: temp, Unlogged: unlogged, IfNotExists: p.parseIfNotExists()}
	ct.Name = p.parseRelationName()
	typeless := -1 // source offset of the first column without a type
	if ext && p.acceptKwSeq("PARTITION", "OF") {
		ct.PartitionOf = p.parseName()
	} else if ext && p.acceptKw("OF") {
		ct.OfType = p.parseName()
	}
	if p.acceptOp("(") {
		ct.HasColumns = true
		typed := ct.PartitionOf != nil || ct.OfType != nil // their list may not be empty
		if !(ext && !typed && p.isOp(")")) {
			typeless = p.parseTableElements(ct)
		}
		p.expectOp(")")
	}
	if ct.PartitionOf != nil {
		ct.Bound = p.parsePartitionBound()
	}
	if ext {
		p.parseTableStorage(ct)
	}
	if !ct.HasColumns && ct.PartitionOf == nil && ct.OfType == nil && !p.isKw("AS") {
		p.unexpected(`"(" or AS`)
	}
	for p.d.Features.WithoutRowid && (p.isKwSeq("WITHOUT", "ROWID") || p.isKw("STRICT")) {
		if p.acceptKw("STRICT") {
			ct.Strict = true
		} else {
			p.pos += 2
			ct.WithoutRowid = true
		}
		if !p.acceptOp(",") {
			break
		}
		if !p.isKwSeq("WITHOUT", "ROWID") && !p.isKw("STRICT") {
			p.unexpected("table option")
		}
	}
	if p.isKw("AS") && ct.HasColumns && (!ext || !namesOnly(ct)) {
		// PostgreSQL takes column names only before AS, SQLite nothing.
		p.unexpected("end of CREATE TABLE")
	}
	if p.acceptKw("AS") {
		if ext && p.isKw("EXECUTE") {
			ct.AsExecute = p.parseServerUtility("EXECUTE").(*ast.Execute)
		} else {
			ct.AsQuery = p.parseQuery()
		}
		if ext {
			ct.WithData = p.parseWithData()
		}
	} else if ext && ct.PartitionOf == nil && ct.OfType == nil && typeless >= 0 {
		p.errorf(typeless, "column definition needs a data type")
	}
	return ct
}

// parseTableElements parses the element list, returning the source offset of
// the first column written without a type, or -1.
// namesOnly reports whether a table's element list holds bare column names.
func namesOnly(ct *ast.CreateTable) bool {
	if len(ct.Columns) == 0 || len(ct.Constraints) > 0 || len(ct.Likes) > 0 {
		return false
	}
	for _, c := range ct.Columns {
		if c.Type != nil || len(c.Constraints) > 0 || c.Storage != "" || c.Compression != "" || len(c.Options) > 0 {
			return false
		}
	}
	return true
}

func (p *Parser) parseTableElements(ct *ast.CreateTable) int {
	typeless := -1
	for {
		switch {
		case p.d.Features.ExtendedDDL && p.isKw("LIKE"):
			l := p.parseTableLike()
			l.Position = len(ct.Columns)
			ct.Likes = append(ct.Likes, l)
		case p.isTableConstraintStart():
			if !p.d.Features.ExtendedDDL && len(ct.Columns) == 0 {
				p.unexpected("column definition") // SQLite: at least one column comes first
			}
			ct.Constraints = append(ct.Constraints, p.parseTableConstraint())
			// SQLite: the comma between table constraints is optional.
			if p.d.Features.FreeFormTypeNames && p.isTableConstraintStart() {
				continue
			}
		default:
			if p.d.Features.FreeFormTypeNames && len(ct.Constraints) > 0 {
				p.unexpected("table constraint") // SQLite: columns come first
			}
			pos := p.peek().Pos
			cd := p.parseColumnDef()
			if cd.Type == nil && typeless < 0 {
				typeless = pos
			}
			ct.Columns = append(ct.Columns, cd)
		}
		if !p.acceptOp(",") {
			return typeless
		}
	}
}

func (p *Parser) parseTableLike() *ast.TableLike {
	p.expectKw("LIKE")
	l := &ast.TableLike{Source: p.parseName()}
	for {
		dir := p.acceptOneOf("INCLUDING", "EXCLUDING")
		if dir == "" {
			return l
		}
		what := p.acceptOneOf("COMMENTS", "COMPRESSION", "CONSTRAINTS", "DEFAULTS", "GENERATED",
			"IDENTITY", "INDEXES", "STATISTICS", "STORAGE", "ALL")
		if what == "" {
			p.unexpected("LIKE option")
		}
		l.Options = append(l.Options, dir+" "+what)
	}
}

func (p *Parser) parsePartitionBound() *ast.PartitionBound {
	if p.acceptKw("DEFAULT") {
		return &ast.PartitionBound{Default: true}
	}
	p.expectKw("FOR", "VALUES")
	b := &ast.PartitionBound{}
	paren := func() []ast.Expr {
		p.expectOp("(")
		list := p.parseExprList()
		p.expectOp(")")
		return list
	}
	switch {
	case p.acceptKw("IN"):
		b.In = paren()
	case p.acceptKw("FROM"):
		b.From = paren()
		p.expectKw("TO")
		b.To = paren()
	case p.acceptKw("WITH"): // MODULUS and REMAINDER once each, in either order
		p.expectOp("(")
		for {
			pos := p.peek().Pos
			name := p.acceptOneOf("MODULUS", "REMAINDER")
			dst := &b.Modulus
			switch name {
			case "":
				p.unexpected("MODULUS or REMAINDER")
			case "REMAINDER":
				dst = &b.Remainder
			}
			if *dst != nil {
				p.errorf(pos, "%s for hash partition provided more than once", strings.ToLower(name))
			}
			if t := p.peek(); t.Kind != lexer.Number || !isInteger(t) {
				p.unexpected("integer")
			}
			*dst = &ast.Literal{Kind: ast.Integer, Value: p.next().Text}
			if !p.acceptOp(",") {
				break
			}
		}
		end := p.peek().Pos
		p.expectOp(")")
		if b.Modulus == nil {
			p.errorf(end, "modulus for hash partition must be specified")
		}
		if b.Remainder == nil {
			p.errorf(end, "remainder for hash partition must be specified")
		}
	default:
		p.unexpected("IN, FROM or WITH")
	}
	return b
}

// parseTableStorage parses the clauses after the element list, in gram.y order.
func (p *Parser) parseTableStorage(ct *ast.CreateTable) {
	if p.acceptKw("INHERITS") {
		p.expectOp("(")
		ct.Inherits = p.parseNameList()
		p.expectOp(")")
	}
	if p.acceptKwSeq("PARTITION", "BY") {
		strategy := p.acceptOneOf("RANGE", "LIST", "HASH")
		if strategy == "" {
			p.unexpected("RANGE, LIST or HASH")
		}
		p.expectOp("(")
		ct.PartitionBy = &ast.PartitionSpec{Strategy: strategy, Elems: p.parsePartitionElems()}
		p.expectOp(")")
	}
	if p.acceptKw("USING") {
		ct.Using = p.parseIdentText()
	}
	if p.isKw("WITH") && p.isOpAt(1, "(") {
		p.next()
		ct.With = p.parseParenOptions()
	} else {
		p.acceptKwSeq("WITHOUT", "OIDS")
	}
	if p.acceptKwSeq("ON", "COMMIT") {
		switch {
		case p.acceptKwSeq("PRESERVE", "ROWS"):
			ct.OnCommit = "PRESERVE ROWS"
		case p.acceptKwSeq("DELETE", "ROWS"):
			ct.OnCommit = "DELETE ROWS"
		case p.acceptKw("DROP"):
			ct.OnCommit = "DROP"
		default:
			p.unexpected("ON COMMIT action")
		}
	}
	if p.acceptKw("TABLESPACE") {
		ct.Tablespace = p.parseIdentText()
	}
}

func (p *Parser) parseWithData() *bool {
	switch {
	case p.acceptKwSeq("WITH", "NO", "DATA"):
		f := false
		return &f
	case p.acceptKwSeq("WITH", "DATA"):
		t := true
		return &t
	}
	return nil
}

// parseParenOptions parses (name [= value], ...), namespaced names included.
func (p *Parser) parseParenOptions() []ast.Option {
	p.expectOp("(")
	var opts []ast.Option
	for {
		name := identText(p.parseAnyIdent())
		for p.acceptOp(".") {
			name += "." + identText(p.parseAnyIdent())
		}
		opt := ast.Option{Name: name}
		if p.acceptOp("=") {
			opt.Value = p.parseWordOrExpr()
		}
		opts = append(opts, opt)
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp(")")
	return opts
}

func (p *Parser) isTableConstraintStart() bool {
	switch {
	case p.isKw("CONSTRAINT", "CHECK", "FOREIGN"):
		return true
	case p.isKw("PRIMARY") && p.isKwAt(1, "KEY"):
		return true
	case p.isKw("UNIQUE") && (p.isOpAt(1, "(") || p.isKwAt(1, "NULLS", "USING")):
		return true
	case p.d.Features.ExtendedDDL && p.isKw("EXCLUDE") && (p.isOpAt(1, "(") || p.isKwAt(1, "USING")):
		return true
	}
	return false
}

func (p *Parser) parseColumnDef() *ast.ColumnDef {
	cd := &ast.ColumnDef{Name: p.parseIdent()}
	free := p.d.Features.FreeFormTypeNames
	if p.d.Features.ExtendedDDL && p.acceptKwSeq("WITH", "OPTIONS") {
		// typed tables and partitions: options on an existing column, no type
	} else if !p.isOp(",") && !p.isOp(")") && !p.atStatementEnd() && (!p.isColumnConstraintStart() || free && p.isKw("GENERATED")) {
		cd.Type = p.parseType()
		if free {
			stripGeneratedAlways(cd)
		}
	}
	if p.d.Features.ExtendedDDL {
		if p.acceptKw("STORAGE") {
			cd.Storage = p.parseIdentOrDefault()
		}
		if p.acceptKw("COMPRESSION") {
			cd.Compression = p.parseIdentOrDefault()
		}
		if p.acceptKw("OPTIONS") {
			cd.Options = p.parseGenericOptions()
		}
	}
	for p.isColumnConstraintStart() {
		cd.Constraints = append(cd.Constraints, p.parseColumnConstraint())
	}
	return cd
}

// stripGeneratedAlways mirrors sqlite3AddColumn: GENERATED and ALWAYS are
// ordinary type words to the grammar, and a trailing GENERATED ALWAYS is
// removed from the type once the column is built.
func stripGeneratedAlways(cd *ast.ColumnDef) {
	words := strings.Fields(cd.Type.Name)
	n := len(words)
	if n >= 2 && strings.EqualFold(words[n-2], "GENERATED") && strings.EqualFold(words[n-1], "ALWAYS") {
		cd.Type.Name = strings.Join(words[:n-2], " ")
		if cd.Type.Name == "" {
			cd.Type = nil
		}
	}
}

func (p *Parser) atStatementEnd() bool {
	return p.peek().Kind == lexer.EOF || p.isOp(";")
}

func (p *Parser) isColumnConstraintStart() bool {
	if p.isKw("AS") {
		return p.d.Features.FreeFormTypeNames // SQLite shorthand for GENERATED ALWAYS AS
	}
	return p.isKw("CONSTRAINT", "NOT", "NULL", "PRIMARY", "UNIQUE", "CHECK", "DEFAULT",
		"REFERENCES", "COLLATE", "GENERATED", "DEFERRABLE") ||
		p.d.Features.ExtendedDDL && p.isKw("INITIALLY")
}

func (p *Parser) parseColumnConstraint() *ast.ColumnConstraint {
	cc := &ast.ColumnConstraint{}
	ext := p.d.Features.ExtendedDDL
	if p.acceptKw("CONSTRAINT") {
		id := p.parseIdent()
		cc.Name = &id
		if !ext && (!p.isColumnConstraintStart() || p.isKw("CONSTRAINT")) { // SQLite allows the name alone
			cc.Kind = ast.AttributeConstraint
			return cc
		}
		if ext && p.isDeferrableStart() { // PostgreSQL names only a constraint, not an attribute
			p.unexpected("column constraint")
		}
	}
	switch {
	case p.acceptKwSeq("NOT", "NULL"):
		cc.Kind = ast.NotNullConstraint
		cc.OnConflict = p.parseConflictClause()
	case p.acceptKw("NULL"):
		cc.Kind = ast.NullConstraint
		cc.OnConflict = p.parseConflictClause()
	case p.acceptKwSeq("PRIMARY", "KEY"):
		cc.Kind = ast.PrimaryKeyConstraint
		cc.IndexWith, cc.IndexTablespace = p.parseIndexParams()
		if p.d.Features.Autoincrement {
			cc.Direction = p.acceptOneOf("ASC", "DESC")
		}
		cc.OnConflict = p.parseConflictClause()
		cc.Autoincrement = p.d.Features.Autoincrement && p.acceptKw("AUTOINCREMENT")
	case p.acceptKw("UNIQUE"):
		cc.Kind = ast.UniqueConstraint
		cc.NullsNotDist = ext && p.parseNullsDistinct()
		cc.IndexWith, cc.IndexTablespace = p.parseIndexParams()
		cc.OnConflict = p.parseConflictClause()
	case p.acceptKw("CHECK"):
		cc.Kind = ast.CheckConstraint
		p.expectOp("(")
		cc.Expr = p.parseExpr()
		p.expectOp(")")
		cc.NoInherit = ext && p.acceptKwSeq("NO", "INHERIT")
	case p.acceptKw("DEFAULT"):
		cc.Kind = ast.DefaultConstraint
		cc.Expr = p.parseColumnDefault()
	case p.isKw("REFERENCES"):
		cc.Kind = ast.ReferencesConstraint
		cc.References = p.parseReferences()
	case p.acceptKw("COLLATE"):
		cc.Kind = ast.CollateConstraint
		cc.Collation = p.parseObjectName()
	case p.acceptKw("GENERATED"):
		p.parseGenerated(cc)
	case p.acceptKw("AS"):
		p.parseGeneratedExpr(cc)
	default:
		d := p.parseDeferrable()
		if d == nil {
			p.unexpected("column constraint")
		}
		cc.Kind = ast.AttributeConstraint
		cc.Deferrable = d
		return cc
	}
	cc.Deferrable = p.parseDeferrable()
	return cc
}

// parseColumnDefault parses the expression after DEFAULT. PostgreSQL takes a
// b_expr (no AND/OR/IS/...), so `DEFAULT 0 NOT NULL` does not absorb NOT NULL;
// SQLite only allows a signed literal, a name or a parenthesized expression.
func (p *Parser) parseColumnDefault() ast.Expr {
	if !p.d.Features.FreeFormTypeNames {
		if p.isKw("DEFAULT", "NOT") { // not the start of a b_expr
			p.unexpected("expression")
		}
		pos := p.peek().Pos
		e := p.parseBExpr()
		if _, prec, ok := p.infixOp(); ok && prec >= p.d.Precedence["IS"] && !p.isKw("COLLATE") { // COLLATE is a constraint here
			p.errorf(pos, "this expression needs parentheses in a column default")
		}
		return e
	}
	if p.isOp("-") || p.isOp("+") {
		op := p.next().Text
		switch p.peek().Kind {
		case lexer.Number, lexer.String, lexer.Blob:
		default:
			if !p.isKw("NULL", "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP") {
				p.unexpected("literal")
			}
		}
		return &ast.Unary{Op: op, Expr: p.parsePrimary()}
	}
	// A bare word is a name here, never the start of a function call.
	if p.peek().Kind == lexer.Ident && !p.isKw("NULL", "TRUE", "FALSE") && !p.d.BareFuncs[p.peek().Value] {
		return &ast.Name{Parts: []ast.Ident{p.parseIdent()}}
	}
	switch {
	case p.peek().Kind == lexer.Param: // SQLite: a literal, a name or ( expr )
		p.unexpected("literal or parenthesized expression")
	case p.isOp("(") && p.isKwAt(1, "SELECT", "VALUES", "WITH"): // the ( belongs to DEFAULT ( expr )
		p.next()
		p.unexpected("expression")
	}
	return p.parsePrimary()
}

// parseIndexParams parses PostgreSQL's opt_definition OptConsTableSpace after
// a key constraint: WITH (storage parameters) USING INDEX TABLESPACE x.
func (p *Parser) parseIndexParams() ([]ast.Option, string) {
	var with []ast.Option
	if p.d.Features.ExtendedDDL && p.isKw("WITH") && p.isOpAt(1, "(") {
		p.next()
		with = p.parseParenOptions()
	}
	return with, p.parseIndexTablespace()
}

// parseIndexTablespace parses PostgreSQL's USING INDEX TABLESPACE x.
func (p *Parser) parseIndexTablespace() string {
	if p.d.Features.ExtendedDDL && p.acceptKwSeq("USING", "INDEX", "TABLESPACE") {
		return p.parseIdentText()
	}
	return ""
}

func (p *Parser) parseGenerated(cc *ast.ColumnConstraint) {
	switch {
	case p.d.Features.ExtendedDDL && p.acceptKwSeq("BY", "DEFAULT"):
		cc.Generated = "BY DEFAULT"
	case p.acceptKw("ALWAYS"):
		cc.Generated = "ALWAYS"
	default:
		p.unexpected("ALWAYS or BY DEFAULT")
	}
	p.expectKw("AS")
	if p.d.Features.ExtendedDDL && p.acceptKw("IDENTITY") {
		cc.Kind = ast.IdentityConstraint
		cc.Identity = p.parseIdentityOptions()
		return
	}
	if cc.Generated == "BY DEFAULT" {
		p.errorf(p.peek().Pos, "for a generated column, GENERATED ALWAYS must be specified")
	}
	p.parseGeneratedExpr(cc)
}

func (p *Parser) parseGeneratedExpr(cc *ast.ColumnConstraint) {
	cc.Kind = ast.GeneratedConstraint
	p.expectOp("(")
	cc.Expr = p.parseExpr()
	p.expectOp(")")
	if p.d.Features.StoredGeneratedOnly {
		p.expectKw("STORED")
		cc.Generated = "STORED"
		return
	}
	cc.Generated = p.acceptOneOf("STORED", "VIRTUAL")
}

func (p *Parser) parseConflictClause() string {
	if !p.d.Features.ConflictClause || !p.isKwSeq("ON", "CONFLICT") {
		return ""
	}
	p.next()
	p.next()
	act := p.acceptOneOf(conflictActions...)
	if act == "" {
		p.unexpected("conflict action")
	}
	return act
}

func (p *Parser) parseReferences() *ast.References {
	p.expectKw("REFERENCES")
	r := &ast.References{Table: p.parseObjectName()}
	if p.isOp("(") {
		r.Columns = p.parseParenIdentList()
	}
	// PostgreSQL: MATCH first, then ON DELETE and ON UPDATE once each.
	loose := p.d.Features.LooseReferences
	for {
		switch {
		case p.isKw("MATCH") && (loose || r.Match == "" && r.OnDelete == "" && r.OnUpdate == ""):
			p.next()
			pos := p.peek().Pos
			if loose {
				if t := p.peek(); t.Kind == lexer.String || p.canBeIdent() {
					r.Match = p.next().Text
					continue
				}
				p.unexpected("name")
			}
			r.Match = p.acceptOneOf("FULL", "PARTIAL", "SIMPLE")
			if r.Match == "" {
				p.unexpected("FULL, PARTIAL or SIMPLE")
			}
			if r.Match == "PARTIAL" {
				p.errorf(pos, "MATCH PARTIAL is not supported")
			}
		case p.isKwSeq("ON", "DELETE") && (loose || r.OnDelete == ""):
			p.pos += 2
			r.OnDelete = p.parseRefAction()
		case p.isKwSeq("ON", "UPDATE") && (loose || r.OnUpdate == ""):
			p.pos += 2
			r.OnUpdate = p.parseRefAction()
			if strings.Contains(r.OnUpdate, "(") {
				p.errorf(p.peek().Pos, "a column list is only supported for ON DELETE actions")
			}
		case loose && p.acceptKwSeq("ON", "INSERT"):
			r.OnInsert = p.parseRefAction()
		default:
			return r
		}
	}
}

func (p *Parser) parseRefAction() string {
	switch {
	case p.acceptKwSeq("SET", "NULL"):
		return "SET NULL" + p.parseRefActionColumns()
	case p.acceptKwSeq("SET", "DEFAULT"):
		return "SET DEFAULT" + p.parseRefActionColumns()
	case p.acceptKwSeq("NO", "ACTION"):
		return "NO ACTION"
	}
	if act := p.acceptOneOf("CASCADE", "RESTRICT"); act != "" {
		return act
	}
	p.unexpected("referential action")
	return ""
}

// parseRefActionColumns parses the column list of ON DELETE SET NULL (a, b).
func (p *Parser) parseRefActionColumns() string {
	if !p.d.Features.ExtendedDDL || !p.acceptOp("(") {
		return ""
	}
	cols := []string{p.parseIdentText()}
	for p.acceptOp(",") {
		cols = append(cols, p.parseIdentText())
	}
	p.expectOp(")")
	return " (" + strings.Join(cols, ", ") + ")"
}

func (p *Parser) isDeferrableStart() bool {
	return p.isKw("DEFERRABLE") || p.isKwSeq("NOT", "DEFERRABLE") || p.d.Features.ExtendedDDL && p.isKw("INITIALLY")
}

// parseDeferrable parses [NOT] DEFERRABLE [INITIALLY DEFERRED|IMMEDIATE], and
// in PostgreSQL also INITIALLY ... [[NOT] DEFERRABLE]. It returns nil when
// there is none. PostgreSQL keeps each word pair of a column constraint as a
// constraint of its own, so the order written is kept.
func (p *Parser) parseDeferrable() *ast.Deferrable {
	d := &ast.Deferrable{}
	switch {
	case p.deferrableWord(d):
		p.initially(d)
	case p.d.Features.ExtendedDDL && p.initially(d):
		d.InitiallyFirst = p.deferrableWord(d)
	default:
		return nil
	}
	return d
}

func (p *Parser) deferrableWord(d *ast.Deferrable) bool {
	switch {
	case p.acceptKwSeq("NOT", "DEFERRABLE"):
		d.Deferrable = "NOT DEFERRABLE"
	case p.acceptKw("DEFERRABLE"):
		d.Deferrable = "DEFERRABLE"
	default:
		return false
	}
	return true
}

func (p *Parser) initially(d *ast.Deferrable) bool {
	if !p.acceptKw("INITIALLY") {
		return false
	}
	d.Initially = p.acceptOneOf("DEFERRED", "IMMEDIATE")
	if d.Initially == "" {
		p.unexpected("DEFERRED or IMMEDIATE")
	}
	return true
}

// constraintAttrs is what a PostgreSQL table constraint takes after it, as
// processCASbits in gram.y checks it.
type constraintAttrs struct {
	what                            string // for the error message: CHECK, UNIQUE, ...
	deferrable, notValid, noInherit bool
}

// parseConstraintAttrs parses PostgreSQL's ConstraintAttributeSpec: timing
// attributes, NOT VALID and NO INHERIT in any order. A repeat changes
// nothing, since the parse tree keeps only flags.
func (p *Parser) parseConstraintAttrs(allow constraintAttrs) (d *ast.Deferrable, notValid, noInherit bool) {
	start := p.peek().Pos
	var got ast.Deferrable
	for {
		pos := p.peek().Pos
		var one ast.Deferrable
		switch {
		case p.deferrableWord(&one):
			if got.Deferrable != "" && got.Deferrable != one.Deferrable {
				p.errorf(pos, "conflicting constraint properties")
			}
			got.InitiallyFirst = got.Deferrable == "" && got.Initially != ""
			got.Deferrable = one.Deferrable
		case p.initially(&one):
			if got.Initially != "" && got.Initially != one.Initially {
				p.errorf(pos, "conflicting constraint properties")
			}
			got.Initially = one.Initially
		case p.acceptKwSeq("NOT", "VALID"):
			notValid = true
		case p.acceptKwSeq("NO", "INHERIT"):
			noInherit = true
		default:
			if got.Deferrable == "NOT DEFERRABLE" && got.Initially == "DEFERRED" {
				p.errorf(start, "constraint declared INITIALLY DEFERRED must be DEFERRABLE")
			}
			cannot := ""
			switch {
			case !allow.deferrable && (got.Deferrable == "DEFERRABLE" || got.Initially == "DEFERRED"):
				cannot = "DEFERRABLE"
			case !allow.notValid && notValid:
				cannot = "NOT VALID"
			case !allow.noInherit && noInherit:
				cannot = "NO INHERIT"
			}
			if cannot != "" {
				p.errorf(start, "%s constraints cannot be marked %s", allow.what, cannot)
			}
			if got.Deferrable != "" || got.Initially != "" {
				d = &got
			}
			return d, notValid, noInherit
		}
	}
}

func (p *Parser) parseTableConstraint() *ast.TableConstraint {
	ext := p.d.Features.ExtendedDDL
	tc := &ast.TableConstraint{}
	if p.acceptKw("CONSTRAINT") {
		id := p.parseIdent()
		tc.Name = &id
		if !ext && (p.isOp(",") || p.isOp(")") || p.isKw("CONSTRAINT")) { // SQLite allows the name alone
			tc.Kind = ast.TableNameOnly
			return tc
		}
	}
	// PostgreSQL's key lists are plain column names; SQLite allows COLLATE and
	// ASC/DESC in PRIMARY KEY and UNIQUE.
	columns := func() {
		if ext && p.acceptKwSeq("USING", "INDEX") {
			tc.UsingIndex = p.parseIdentText()
			return
		}
		p.expectOp("(")
		if p.d.Features.IndexOpclass {
			tc.Columns = p.parseColumnElems()
		} else {
			tc.Columns = p.parseIndexElems()
		}
		if p.d.Features.Autoincrement && tc.Kind == ast.TablePrimaryKey {
			tc.Autoincrement = p.acceptKw("AUTOINCREMENT")
		}
		p.expectOp(")")
		if ext && p.acceptKw("INCLUDE") {
			tc.Include = p.parseParenIdentList()
		}
		tc.IndexWith, tc.IndexTablespace = p.parseIndexParams()
		tc.OnConflict = p.parseConflictClause()
	}
	switch {
	case p.acceptKwSeq("PRIMARY", "KEY"):
		tc.Kind = ast.TablePrimaryKey
		columns()
	case p.acceptKw("UNIQUE"):
		tc.Kind = ast.TableUnique
		tc.NullsNotDist = ext && p.parseNullsDistinct()
		columns()
	case p.acceptKw("CHECK"):
		tc.Kind = ast.TableCheck
		p.expectOp("(")
		tc.Expr = p.parseExpr()
		p.expectOp(")")
		tc.OnConflict = p.parseConflictClause()
	case p.acceptKwSeq("FOREIGN", "KEY"):
		tc.Kind = ast.TableForeignKey
		p.expectOp("(")
		tc.Columns = p.parseColumnElems()
		p.expectOp(")")
		tc.References = p.parseReferences()
	case ext && p.acceptKw("EXCLUDE"):
		p.parseExclude(tc)
	default:
		p.unexpected("table constraint")
	}
	if !ext {
		if tc.Kind == ast.TableForeignKey { // SQLite: only a foreign key takes one
			tc.Deferrable = p.parseDeferrable()
		}
		return tc
	}
	allow := map[ast.TableConstraintKind]constraintAttrs{
		ast.TablePrimaryKey: {what: "PRIMARY KEY", deferrable: true},
		ast.TableUnique:     {what: "UNIQUE", deferrable: true},
		ast.TableExclude:    {what: "EXCLUDE", deferrable: true},
		ast.TableCheck:      {what: "CHECK", notValid: true, noInherit: true},
		ast.TableForeignKey: {what: "FOREIGN KEY", deferrable: true, notValid: true},
	}[tc.Kind]
	tc.Deferrable, tc.NotValid, tc.NoInherit = p.parseConstraintAttrs(allow)
	return tc
}

func (p *Parser) parseExclude(tc *ast.TableConstraint) {
	tc.Kind = ast.TableExclude
	if p.acceptKw("USING") {
		tc.Method = p.parseIdentText()
	}
	p.expectOp("(")
	for {
		el := &ast.ExcludeElem{Elem: p.parseIndexElem()}
		p.expectKw("WITH")
		if p.isKw("OPERATOR") {
			el.Op = p.parseOperatorName()
		} else {
			if !dialect.IsOperatorText(p.peek().Text) {
				p.unexpected("operator")
			}
			el.Op = p.next().Text
		}
		tc.Excludes = append(tc.Excludes, el)
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp(")")
	if p.acceptKw("INCLUDE") {
		tc.Include = p.parseParenIdentList()
	}
	tc.IndexWith, tc.IndexTablespace = p.parseIndexParams()
	if p.acceptKw("WHERE") {
		p.expectOp("(")
		tc.Where = p.parseExpr()
		p.expectOp(")")
	}
}

// parseColumnElems parses a list of plain column names as index elements.
func (p *Parser) parseColumnElems() []*ast.IndexElem {
	var elems []*ast.IndexElem
	for _, id := range p.parseIdentList() {
		elems = append(elems, &ast.IndexElem{Expr: &ast.Name{Parts: []ast.Ident{id}}})
	}
	return elems
}

// parseIndexElems parses `expr [COLLATE c] [opclass] [ASC|DESC] [NULLS ...], ...`.
func (p *Parser) parseIndexElems() []*ast.IndexElem {
	elems := []*ast.IndexElem{p.parseIndexElem()}
	for p.acceptOp(",") {
		elems = append(elems, p.parseIndexElem())
	}
	return elems
}

// parsePartitionElems parses PARTITION BY's elements, which are index
// elements without ASC, DESC or NULLS.
func (p *Parser) parsePartitionElems() []*ast.IndexElem {
	var elems []*ast.IndexElem
	for {
		e := p.parseIndexElemNoOrder()
		elems = append(elems, e)
		if !p.acceptOp(",") {
			return elems
		}
	}
}

func (p *Parser) parseIndexElem() *ast.IndexElem {
	e := p.parseIndexElemNoOrder()
	e.Direction = p.acceptOneOf("ASC", "DESC")
	if p.acceptKw("NULLS") {
		e.Nulls = p.acceptOneOf("FIRST", "LAST")
		if e.Nulls == "" {
			p.unexpected("FIRST or LAST")
		}
	}
	return e
}

func (p *Parser) parseIndexElemNoOrder() *ast.IndexElem {
	// In PostgreSQL an element is a column, function call or parenthesized
	// expression, so stop before COLLATE and let it attach to the element.
	// SQLite takes any expression; there a top-level COLLATE is unwrapped.
	minPrec := 0
	if p.d.Features.IndexOpclass {
		minPrec = p.d.Precedence["COLLATE"]
	}
	pos := p.peek().Pos
	e := &ast.IndexElem{Expr: p.parseExprPrec(minPrec)}
	if p.d.Features.IndexOpclass && !isIndexExpr(e.Expr) {
		p.errorf(pos, "index expressions must be a column, a function call or parenthesized")
	}
	if c, ok := e.Expr.(*ast.Collate); ok {
		e.Expr, e.Collation = c.Expr, c.Collation
	}
	if p.acceptKw("COLLATE") {
		e.Collation = p.parseName()
	}
	if p.d.Features.IndexOpclass && p.canBeIdent() && !p.isKw("ASC", "DESC", "NULLS") {
		e.Opclass = p.parseName()
		if p.isOp("(") {
			e.OpclassOptions = p.parseParenOptions()
		}
	}
	return e
}

// isIndexExpr reports whether e is what gram.y's index_elem accepts.
func isIndexExpr(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Name:
		return len(e.Parts) == 1
	case *ast.FuncCall: // func_expr_windowless
		return e.Over == nil && e.Filter == nil && len(e.WithinGroup) == 0
	case *ast.Cast: // CAST(x AS t), not x::t
		return e.Style != ast.CastDoubleColon
	case *ast.Paren, *ast.Extract, *ast.Substring, *ast.Trim, *ast.Position, *ast.Overlay:
		return true
	}
	return false
}

func (p *Parser) parseCreateIndex() ast.Statement {
	ext := p.d.Features.ExtendedDDL
	ci := &ast.CreateIndex{Unique: p.acceptKw("UNIQUE")}
	p.expectKw("INDEX")
	ci.Concurrently = ext && p.acceptKw("CONCURRENTLY")
	ci.IfNotExists = p.parseIfNotExists()
	switch {
	case !ext:
		ci.Name = p.parseName()
	case ci.IfNotExists || !p.isKw("ON"): // the index goes in its table's schema
		ci.Name = &ast.Name{Parts: []ast.Ident{p.parseIdent()}}
	}
	p.expectKw("ON")
	if ext {
		ci.Only, ci.Table = p.parseRelationExpr()
	} else { // SQLite: the schema goes on the index name
		ci.Table = &ast.Name{Parts: []ast.Ident{p.parseIdent()}}
	}
	if ext && p.acceptKw("USING") {
		ci.Method = p.parseIdentText()
	}
	p.expectOp("(")
	ci.Columns = p.parseIndexElems()
	p.expectOp(")")
	if ext {
		if p.acceptKw("INCLUDE") {
			ci.Include = p.parseParenIdentList()
		}
		ci.NullsNotDist = p.parseNullsDistinct()
		if p.isKw("WITH") && p.isOpAt(1, "(") {
			p.next()
			ci.With = p.parseParenOptions()
		}
		if p.acceptKw("TABLESPACE") {
			ci.Tablespace = p.parseIdentText()
		}
	}
	if p.acceptKw("WHERE") {
		ci.Where = p.parseExpr()
	}
	return ci
}

// parseNullsDistinct parses optional NULLS [NOT] DISTINCT; the default is
// NULLS DISTINCT, so only the NOT form is reported.
func (p *Parser) parseNullsDistinct() bool {
	if p.acceptKwSeq("NULLS", "NOT", "DISTINCT") {
		return true
	}
	p.acceptKwSeq("NULLS", "DISTINCT")
	return false
}

func (p *Parser) parseCreateView(orReplace, temp, unlogged, materialized bool) ast.Statement {
	ext := p.d.Features.ExtendedDDL
	cv := &ast.CreateView{OrReplace: orReplace, Temp: temp, Unlogged: unlogged, Materialized: materialized}
	if !ext || materialized { // PostgreSQL has no CREATE VIEW IF NOT EXISTS
		cv.IfNotExists = p.parseIfNotExists()
	}
	cv.Name = p.parseName()
	if p.isOp("(") {
		cv.Columns = p.parseParenIdentList()
	}
	if ext && materialized && p.acceptKw("USING") {
		cv.Using = p.parseIdentText()
	}
	if ext && p.isKw("WITH") && p.isOpAt(1, "(") {
		p.next()
		cv.With = p.parseParenOptions()
	}
	if ext && materialized && p.acceptKw("TABLESPACE") {
		cv.Tablespace = p.parseIdentText()
	}
	p.expectKw("AS")
	cv.Query = p.parseQuery()
	if !ext {
		return cv
	}
	if materialized {
		cv.WithData = p.parseWithData()
		return cv
	}
	switch {
	case p.acceptKwSeq("WITH", "CHECK", "OPTION"):
		cv.CheckOption = "CHECK"
	case p.acceptKwSeq("WITH", "CASCADED", "CHECK", "OPTION"):
		cv.CheckOption = "CASCADED"
	case p.acceptKwSeq("WITH", "LOCAL", "CHECK", "OPTION"):
		cv.CheckOption = "LOCAL"
	}
	return cv
}

func (p *Parser) parseCreateSchema() ast.Statement {
	cs := &ast.CreateSchema{IfNotExists: p.parseIfNotExists()}
	if p.canBeIdent() {
		id := p.parseIdent()
		cs.Name = &id
	}
	if p.acceptKw("AUTHORIZATION") {
		cs.Authorization = p.parseGrantee()
	}
	if cs.Name == nil && cs.Authorization == nil {
		p.unexpected("schema name")
	}
	if cs.IfNotExists && p.isKw("CREATE", "GRANT") {
		p.errorf(p.peek().Pos, "CREATE SCHEMA IF NOT EXISTS cannot include schema elements")
	}
	for p.isKw("CREATE", "GRANT") {
		pos := p.peek().Pos
		el := p.parseStatement()
		if !isSchemaElement(el) {
			p.errorf(pos, "CREATE SCHEMA takes only CREATE TABLE, VIEW, INDEX, SEQUENCE, TRIGGER and GRANT")
		}
		cs.Elements = append(cs.Elements, el)
	}
	return cs
}

// isSchemaElement reports whether st is gram.y's schema_stmt.
func isSchemaElement(st ast.Statement) bool {
	switch st := st.(type) {
	case *ast.CreateTable:
		return st.AsQuery == nil && st.AsExecute == nil
	case *ast.CreateView:
		return !st.Materialized
	case *ast.CreateIndex, *ast.CreateSequence, *ast.CreateTrigger:
		return true
	case *ast.Grant:
		return !st.Revoke
	case *ast.Opaque:
		return strings.HasSuffix(st.Verb, " RECURSIVE") // CREATE [TEMP] RECURSIVE VIEW
	}
	return false
}

func (p *Parser) parseCreateType(start int) ast.Statement {
	name := p.parseName()
	if p.atStatementEnd() {
		return &ast.CreateCompositeType{Name: name, Shell: true}
	}
	if p.isOp("(") || p.isKwSeq("AS", "RANGE") { // base and range types
		words := 2
		p.pos = start
		return p.parseOpaque(words)
	}
	p.expectKw("AS")
	if p.acceptKw("ENUM") {
		p.expectOp("(")
		ce := &ast.CreateEnum{Name: name, Labels: []string{}}
		if !p.isOp(")") {
			ce.Labels = append(ce.Labels, p.parseStringValue())
			for p.acceptOp(",") {
				ce.Labels = append(ce.Labels, p.parseStringValue())
			}
		}
		p.expectOp(")")
		return ce
	}
	p.expectOp("(")
	ct := &ast.CreateCompositeType{Name: name}
	if !p.isOp(")") {
		for { // name type [COLLATE c]: no constraints
			pos := p.peek().Pos
			cd := &ast.ColumnDef{Name: p.parseIdent()}
			if p.isOp(",") || p.isOp(")") {
				p.errorf(pos, "column definition needs a data type")
			}
			cd.Type = p.parseType()
			if p.acceptKw("COLLATE") {
				cd.Constraints = []*ast.ColumnConstraint{{Kind: ast.CollateConstraint, Collation: p.parseName()}}
			}
			ct.Columns = append(ct.Columns, cd)
			if !p.acceptOp(",") {
				break
			}
		}
	}
	p.expectOp(")")
	return ct
}

func (p *Parser) parseIdentOrDefault() string {
	if p.acceptKw("DEFAULT") {
		return "DEFAULT"
	}
	return p.parseIdentText()
}

// parseGenericOptions parses the (name 'value', ...) of PostgreSQL's
// OPTIONS for foreign data.
func (p *Parser) parseGenericOptions() []ast.Option {
	p.expectOp("(")
	var opts []ast.Option
	for {
		opt := ast.Option{Name: identText(p.parseAnyIdent())}
		opt.Value = p.parseStringLit()
		opts = append(opts, opt)
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp(")")
	return opts
}

func (p *Parser) parseCreateExtension() ast.Statement {
	ce := &ast.CreateExtension{IfNotExists: p.parseIfNotExists()}
	ce.Name = p.parseIdent()
	p.acceptKw("WITH")
	for {
		var opt ast.ExtensionOption
		switch {
		case p.acceptKw("SCHEMA"):
			id := p.parseIdent()
			opt.Schema = &id
		case p.acceptKw("VERSION"):
			opt.Version = p.parseWordOrString()
		case p.acceptKw("CASCADE"):
			opt.Cascade = true
		default:
			return ce
		}
		ce.Options = append(ce.Options, opt)
	}
}

func (p *Parser) parseCreateDomain() ast.Statement {
	cd := &ast.CreateDomain{Name: p.parseName()}
	p.acceptKw("AS")
	cd.Type = p.parseType()
	for p.isColumnConstraintStart() {
		cd.Constraints = append(cd.Constraints, p.parseColumnConstraint())
	}
	return cd
}

// parseIdentityOptions parses the optional parenthesized options after
// IDENTITY, which hold at least one option.
func (p *Parser) parseIdentityOptions() []ast.SequenceOption {
	if !p.acceptOp("(") {
		return nil
	}
	opts := p.parseSequenceOptions()
	if len(opts) == 0 {
		p.unexpected("sequence option")
	}
	p.expectOp(")")
	return opts
}

// parseSequenceOptions parses CREATE SEQUENCE and IDENTITY options.
func (p *Parser) parseSequenceOptions() []ast.SequenceOption {
	var opts []ast.SequenceOption
	for {
		var opt ast.SequenceOption
		switch {
		case p.acceptKw("INCREMENT"):
			p.acceptKw("BY")
			opt = ast.SequenceOption{Name: "INCREMENT BY", Value: p.parseSignedNumber()}
		case p.acceptKw("START"):
			p.acceptKw("WITH")
			opt = ast.SequenceOption{Name: "START WITH", Value: p.parseSignedNumber()}
		case p.acceptKw("RESTART"):
			opt = ast.SequenceOption{Name: "RESTART"}
			if p.acceptKw("WITH") || p.peek().Kind == lexer.Number || p.isOp("-") || p.isOp("+") {
				opt = ast.SequenceOption{Name: "RESTART WITH", Value: p.parseSignedNumber()}
			}
		case p.acceptKwSeq("SEQUENCE", "NAME"):
			opt = ast.SequenceOption{Name: "SEQUENCE NAME", Value: p.parseName()}
		case p.acceptKw("MINVALUE"):
			opt = ast.SequenceOption{Name: "MINVALUE", Value: p.parseSignedNumber()}
		case p.acceptKw("MAXVALUE"):
			opt = ast.SequenceOption{Name: "MAXVALUE", Value: p.parseSignedNumber()}
		case p.acceptKw("CACHE"):
			opt = ast.SequenceOption{Name: "CACHE", Value: p.parseSignedNumber()}
		case p.acceptKw("CYCLE"):
			opt = ast.SequenceOption{Name: "CYCLE"}
		case p.acceptKwSeq("NO", "CYCLE"):
			opt = ast.SequenceOption{Name: "NO CYCLE"}
		case p.acceptKwSeq("NO", "MINVALUE"):
			opt = ast.SequenceOption{Name: "NO MINVALUE"}
		case p.acceptKwSeq("NO", "MAXVALUE"):
			opt = ast.SequenceOption{Name: "NO MAXVALUE"}
		case p.acceptKwSeq("OWNED", "BY"):
			if p.acceptKw("NONE") {
				opt = ast.SequenceOption{Name: "OWNED BY NONE"}
			} else {
				opt = ast.SequenceOption{Name: "OWNED BY", Value: p.parseName()}
			}
		case p.acceptKw("AS"):
			start := p.pos
			if p.isKw("SETOF") {
				p.unexpected("type name")
			}
			opt = ast.SequenceOption{Name: "AS", Type: p.parseType()}
			if len(opt.Type.ArrayBounds) > 0 { // SimpleTypename: no array
				for p.pos = start; !p.isOp("[") && !p.isKw("ARRAY"); p.pos++ {
				}
				p.unexpected("sequence option")
			}
		default:
			return opts
		}
		opts = append(opts, opt)
	}
}

// --- triggers and virtual tables ------------------------------------------------

func (p *Parser) parseCreateTrigger(orReplace, temp, constraint bool) ast.Statement {
	ext := p.d.Features.ExtendedDDL
	ct := &ast.CreateTrigger{OrReplace: orReplace, Temp: temp, Constraint: constraint}
	if !ext {
		ct.IfNotExists = p.parseIfNotExists()
	}
	ct.Name = p.parseName()
	switch {
	case p.acceptKwSeq("INSTEAD", "OF"):
		ct.Timing = "INSTEAD OF"
	default:
		ct.Timing = p.acceptOneOf("BEFORE", "AFTER")
		if ct.Timing == "" && ext {
			p.unexpected("BEFORE, AFTER or INSTEAD OF")
		}
	}
	ct.Events = []ast.TriggerEvent{p.parseTriggerEvent()}
	for ext && p.acceptKw("OR") {
		ev := p.parseTriggerEvent()
		for _, seen := range ct.Events {
			if seen.Kind == ev.Kind {
				p.errorf(p.peek().Pos, "duplicate trigger events specified")
			}
		}
		ct.Events = append(ct.Events, ev)
	}
	p.expectKw("ON")
	ct.Table = p.parseName()
	if ext {
		p.parsePgTriggerTail(ct)
		return ct
	}
	if p.acceptKwSeq("FOR", "EACH", "ROW") {
		ct.ForEach = "ROW"
	}
	if p.acceptKw("WHEN") {
		ct.When = p.parseExpr()
	}
	p.expectKw("BEGIN")
	for {
		if !p.isKw("UPDATE", "INSERT", "REPLACE", "DELETE", "SELECT", "WITH", "VALUES") {
			p.unexpected("trigger statement")
		}
		pos := p.peek().Pos
		st := p.parseStatement()
		if msg := triggerStatementProblem(st); msg != "" {
			p.errorf(pos, "%s", msg)
		}
		ct.Body = append(ct.Body, st)
		p.expectOp(";")
		if p.acceptKw("END") {
			return ct
		}
	}
}

// triggerStatementProblem returns what SQLite's trigger_cmd does not allow in
// an INSERT, UPDATE or DELETE, or "".
func triggerStatementProblem(st ast.Statement) string {
	var with bool
	var alias *ast.Ident
	switch st := st.(type) {
	case *ast.Insert:
		if st.DefaultValues {
			return "DEFAULT VALUES is not allowed in a trigger"
		}
		with, alias = st.With != nil, st.Alias
	case *ast.Update:
		if len(st.OrderBy) > 0 || st.Limit != nil {
			return "ORDER BY and LIMIT are not allowed in a trigger"
		}
		with, alias = st.With != nil, st.Alias
	case *ast.Delete:
		if len(st.OrderBy) > 0 || st.Limit != nil {
			return "ORDER BY and LIMIT are not allowed in a trigger"
		}
		with, alias = st.With != nil, st.Alias
	}
	switch {
	case with:
		return "WITH is not allowed on INSERT, UPDATE or DELETE in a trigger"
	case alias != nil:
		return "an alias is not allowed on INSERT, UPDATE or DELETE in a trigger"
	}
	return ""
}

func (p *Parser) parseTriggerEvent() ast.TriggerEvent {
	kinds := []string{"INSERT", "DELETE", "UPDATE"}
	if p.d.Features.ExtendedDDL {
		kinds = append(kinds, "TRUNCATE")
	}
	ev := ast.TriggerEvent{Kind: p.acceptOneOf(kinds...)}
	if ev.Kind == "" {
		p.unexpected("trigger event")
	}
	if ev.Kind == "UPDATE" && p.acceptKw("OF") {
		ev.Columns = p.parseIdentList()
	}
	return ev
}

func (p *Parser) parsePgTriggerTail(ct *ast.CreateTrigger) {
	if ct.Constraint { // FROM and deferrability exist only on constraint triggers
		if p.acceptKw("FROM") {
			ct.From = p.parseName()
		}
		ct.Deferrable, _, _ = p.parseConstraintAttrs(constraintAttrs{what: "TRIGGER", deferrable: true})
	}
	if p.acceptKw("REFERENCING") {
		for p.isKw("OLD", "NEW") {
			which := p.next().Value
			what := p.acceptOneOf("TABLE", "ROW")
			if what == "" {
				p.unexpected("TABLE or ROW")
			}
			p.acceptKw("AS")
			ct.Referencing = append(ct.Referencing, which+" "+what+" AS "+p.parseIdentText())
		}
	}
	if p.acceptKw("FOR") {
		p.acceptKw("EACH")
		ct.ForEach = p.acceptOneOf("ROW", "STATEMENT")
		if ct.ForEach == "" {
			p.unexpected("ROW or STATEMENT")
		}
	}
	if p.acceptKw("WHEN") {
		p.expectOp("(")
		ct.When = &ast.Paren{Expr: p.parseExpr()}
		p.expectOp(")")
	}
	p.expectKw("EXECUTE")
	if p.acceptOneOf("FUNCTION", "PROCEDURE") == "" {
		p.unexpected("FUNCTION or PROCEDURE")
	}
	ct.Function = p.parseFuncCall(p.parseName())
}

func (p *Parser) parseCreateVirtualTable() ast.Statement {
	cv := &ast.CreateVirtualTable{IfNotExists: p.parseIfNotExists()}
	cv.Name = p.parseName()
	p.expectKw("USING")
	cv.Module = p.parseIdent()
	if !p.acceptOp("(") {
		return cv
	}
	cv.HasArgs = true
	if p.acceptOp(")") {
		return cv
	}
	for { // an argument may be empty, so a comma always introduces another
		cv.Args = append(cv.Args, strings.TrimSpace(p.takeRawArg()))
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp(")")
	return cv
}

// takeRawArg consumes one module argument, stopping at a top-level ',' or ')'.
func (p *Parser) takeRawArg() string {
	start := p.peek().Pos
	end, depth := start, 0
	for {
		t := p.peek()
		if t.Kind == lexer.EOF {
			p.unexpected(`")"`)
		}
		if t.Kind == lexer.Op {
			switch t.Text {
			case "(":
				depth++
			case ")":
				if depth == 0 {
					return p.src[start:end]
				}
				depth--
			case ",":
				if depth == 0 {
					return p.src[start:end]
				}
			}
		}
		end = t.Pos + len(t.Text)
		p.next()
	}
}

// --- DROP ----------------------------------------------------------------------

func (p *Parser) parseDrop() ast.Statement {
	start := p.pos
	p.expectKw("DROP")
	ext := p.d.Features.ExtendedDDL
	if ext && (p.isKw(opaqueDrop...) || p.isKwSeq("USER", "MAPPING")) {
		p.pos = start
		return p.parseOpaque(2)
	}
	d := &ast.Drop{}
	switch {
	case ext && p.acceptKwSeq("MATERIALIZED", "VIEW"):
		d.Kind = "MATERIALIZED VIEW"
	default:
		kinds := []string{"TABLE", "VIEW", "INDEX", "TRIGGER"}
		if ext {
			kinds = append(kinds, "SCHEMA", "SEQUENCE", "TYPE", "DOMAIN", "EXTENSION", "ROLE", "USER")
		}
		d.Kind = p.acceptOneOf(kinds...)
		if d.Kind == "" {
			p.unexpected("object type")
		}
	}
	d.Concurrently = ext && d.Kind == "INDEX" && p.acceptKw("CONCURRENTLY")
	d.IfExists = p.parseIfExists()
	switch {
	case ext && (d.Kind == "TYPE" || d.Kind == "DOMAIN"):
		d.Types = []*ast.DataType{p.parseType()}
		for p.acceptOp(",") {
			d.Types = append(d.Types, p.parseType())
		}
	case ext && singlePartKinds[d.Kind]:
		for {
			d.Names = append(d.Names, &ast.Name{Parts: []ast.Ident{p.parseIdent()}})
			if d.Kind == "TRIGGER" || !p.acceptOp(",") {
				break
			}
		}
	case p.d.Features.MultiDrop && d.Kind != "TRIGGER":
		d.Names = p.parseNameList()
	default:
		d.Names = []*ast.Name{p.parseName()}
	}
	if ext && d.Kind == "TRIGGER" {
		p.expectKw("ON")
		d.On = p.parseName()
	}
	if ext {
		d.Behavior = p.parseBehavior()
	}
	return d
}

// singlePartKinds name objects that PostgreSQL never qualifies: a schema, an
// extension, a role, and a trigger, which is qualified by its table.
var singlePartKinds = map[string]bool{
	"SCHEMA": true, "EXTENSION": true, "ROLE": true, "USER": true, "DATABASE": true, "TABLESPACE": true,
	"TRIGGER": true, "RULE": true, "POLICY": true, "CONSTRAINT": true,
}

// --- ALTER ----------------------------------------------------------------------

func (p *Parser) parseAlter() ast.Statement {
	start := p.pos
	p.expectKw("ALTER")
	ext := p.d.Features.ExtendedDDL
	if ext && p.isKw(opaqueAlter...) {
		p.pos = start
		return p.parseOpaque(2)
	}
	if ext && p.isKwSeq("TABLE", "ALL") {
		p.pos = start
		return p.parseOpaque(2)
	}
	p.expectKw("TABLE")
	at := &ast.AlterTable{}
	at.IfExists = ext && p.parseIfExists()
	at.Only, at.Name = p.parseRelationExpr()
	at.Actions = []ast.AlterAction{p.parseAlterAction()}
	for ext && p.acceptOp(",") {
		at.Actions = append(at.Actions, p.parseAlterAction())
	}
	return at
}

func (p *Parser) parseAlterAction() ast.AlterAction {
	ext := p.d.Features.ExtendedDDL
	switch {
	case p.acceptKw("ADD"):
		if ext && (p.isTableConstraintStart() || p.isKw("CONSTRAINT", "CHECK")) {
			return &ast.AddConstraint{Constraint: p.parseTableConstraint()}
		}
		p.acceptKw("COLUMN")
		ifNE := ext && p.parseIfNotExists()
		pos := p.peek().Pos
		cd := p.parseColumnDef()
		if ext && cd.Type == nil {
			p.errorf(pos, "column definition needs a data type")
		}
		return &ast.AddColumn{IfNotExists: ifNE, Column: cd}
	case p.acceptKw("DROP"):
		if ext && p.acceptKw("CONSTRAINT") {
			dc := &ast.DropConstraint{IfExists: p.parseIfExists(), Name: p.parseIdent()}
			dc.Behavior = p.parseBehavior()
			return dc
		}
		p.acceptKw("COLUMN")
		ifE := ext && p.parseIfExists()
		dc := &ast.DropColumn{IfExists: ifE, Name: p.parseIdent()}
		if ext {
			dc.Behavior = p.parseBehavior()
		}
		return dc
	case p.acceptKw("RENAME"):
		switch {
		case p.acceptKw("TO"):
			return &ast.RenameTable{New: p.parseIdent()}
		case ext && p.acceptKw("CONSTRAINT"):
			old := p.parseIdent()
			p.expectKw("TO")
			return &ast.RenameConstraint{Old: old, New: p.parseIdent()}
		}
		p.acceptKw("COLUMN")
		old := p.parseIdent()
		p.expectKw("TO")
		return &ast.RenameColumn{Old: old, New: p.parseIdent()}
	case ext && p.isKwSeq("ALTER", "CONSTRAINT"):
		return &ast.AlterOpaque{Text: p.takeRaw(true)}
	case ext && p.isKw("ALTER"):
		return p.parseAlterColumn()
	case ext && p.acceptKwSeq("SET", "SCHEMA"):
		return &ast.SetSchema{Schema: p.parseIdent()}
	case ext && p.isKw(opaqueAlterAction...):
		return &ast.AlterOpaque{Text: p.takeRaw(true)}
	}
	p.unexpected("ALTER TABLE action")
	return nil
}

func (p *Parser) parseAlterColumn() ast.AlterAction {
	start := p.pos
	p.expectKw("ALTER")
	p.acceptKw("COLUMN")
	ac := &ast.AlterColumn{Name: p.parseIdent()}
	switch {
	case p.acceptKwSeq("SET", "DEFAULT"):
		ac.Kind = ast.SetDefault
		ac.Expr = p.parseExpr()
	case p.acceptKwSeq("DROP", "DEFAULT"):
		ac.Kind = ast.DropDefault
	case p.acceptKwSeq("SET", "NOT", "NULL"):
		ac.Kind = ast.SetNotNull
	case p.acceptKwSeq("DROP", "NOT", "NULL"):
		ac.Kind = ast.DropNotNull
	case p.acceptKwSeq("SET", "DATA", "TYPE"), p.acceptKw("TYPE"):
		ac.Kind = ast.SetType
		ac.Type = p.parseType()
		if p.acceptKw("COLLATE") {
			ac.Collation = p.parseName()
		}
		if p.acceptKw("USING") {
			ac.Expr = p.parseExpr()
		}
	case p.acceptKwSeq("ADD", "GENERATED"):
		ac.Kind = ast.AddIdentity
		if p.acceptKwSeq("BY", "DEFAULT") {
			ac.Generated = "BY DEFAULT"
		} else {
			p.expectKw("ALWAYS")
			ac.Generated = "ALWAYS"
		}
		p.expectKw("AS", "IDENTITY")
		ac.Identity = p.parseIdentityOptions()
	case p.isKw("SET", "RESET", "ADD", "RESTART", "DROP"): // statistics, storage, identity, ...
		p.pos = start
		return &ast.AlterOpaque{Text: p.takeRaw(true)}
	default:
		p.unexpected("column action")
	}
	return ac
}
