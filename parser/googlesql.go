package parser

import (
	"sqlp/ast"
	"sqlp/lexer"
)

// BigQuery's DDL (DDLFeatures.GoogleSQL). The rules here were checked with
// dry runs; tools/corpusgen/bigquery/ddl.sql freezes them.

// parseGoogleCreate parses CREATE ..., the word CREATE already read.
func (p *Parser) parseGoogleCreate() ast.Statement {
	orReplace := p.acceptKwSeq("OR", "REPLACE")
	temp := p.acceptKw("TEMP") || p.acceptKw("TEMPORARY")
	switch {
	case p.acceptKw("TABLE"):
		return p.parseGoogleTable(orReplace, temp, "")
	case temp:
	case p.acceptKwSeq("EXTERNAL", "TABLE"):
		return p.parseGoogleTable(orReplace, false, "EXTERNAL")
	case p.acceptKwSeq("SNAPSHOT", "TABLE"):
		return p.parseGoogleTable(orReplace, false, "SNAPSHOT")
	case p.acceptKw("VIEW"):
		return p.parseGoogleView(orReplace, false)
	case p.acceptKwSeq("MATERIALIZED", "VIEW"):
		return p.parseGoogleView(orReplace, true)
	case p.acceptKw("SCHEMA"):
		return p.parseGoogleSchema(orReplace, false)
	case p.acceptKwSeq("EXTERNAL", "SCHEMA"):
		return p.parseGoogleSchema(orReplace, true)
	}
	if temp {
		p.unexpected("TABLE")
	}
	p.unexpected("TABLE, VIEW, SCHEMA or another object type")
	return nil
}

// parseCreateIfNotExists parses IF NOT EXISTS, which OR REPLACE excludes.
func (p *Parser) parseCreateIfNotExists(orReplace bool) bool {
	pos := p.peek().Pos
	ok := p.parseIfNotExists()
	if ok && orReplace {
		p.errorf(pos, "CREATE cannot have both OR REPLACE and IF NOT EXISTS")
	}
	return ok
}

// parseGoogleTable parses CREATE [EXTERNAL|SNAPSHOT] TABLE after TABLE.
func (p *Parser) parseGoogleTable(orReplace, temp bool, kind string) ast.Statement {
	if orReplace && kind == "SNAPSHOT" {
		p.errorf(p.peekN(-1).Pos, "OR REPLACE is not allowed with CREATE SNAPSHOT TABLE")
	}
	ct := &ast.CreateTable{OrReplace: orReplace, Temp: temp, Kind: kind}
	ct.IfNotExists = p.parseCreateIfNotExists(orReplace)
	ct.Name = p.parseRelationName()
	if kind == "SNAPSHOT" {
		p.expectKw("CLONE")
		ct.Source = p.parseTableSource("CLONE")
		if p.isKw("OPTIONS") {
			ct.Options = p.parseOptionList()
		}
		return ct
	}
	if p.acceptOp("(") {
		ct.HasColumns = true
		p.parseGoogleElements(ct)
		p.expectOp(")")
	}
	if kind == "" && !ct.HasColumns {
		if w := p.acceptOneOf("LIKE", "COPY", "CLONE"); w != "" {
			ct.Source = p.parseTableSource(w)
		}
	}
	copied := ct.Source != nil && ct.Source.Kind != "LIKE"
	if kind == "" && p.acceptKwSeq("DEFAULT", "COLLATE") {
		ct.DefaultCollate = p.parseStringValue()
	}
	if kind == "" && p.isKwSeq("PARTITION", "BY") {
		if copied {
			p.errorf(p.peek().Pos, "CREATE TABLE %s cannot have PARTITION BY", ct.Source.Kind)
		}
		p.pos += 2
		ct.PartitionExpr = p.parseExpr()
	}
	if kind == "" && p.isKwSeq("CLUSTER", "BY") {
		if copied {
			p.errorf(p.peek().Pos, "CREATE TABLE %s cannot have CLUSTER BY", ct.Source.Kind)
		}
		p.pos += 2
		ct.ClusterBy = p.parseExprList()
	}
	if kind == "EXTERNAL" && p.acceptKwSeq("WITH", "PARTITION", "COLUMNS") {
		ct.HasPartitionColumns = true
		if p.acceptOp("(") {
			for {
				ct.PartitionColumns = append(ct.PartitionColumns, &ast.ColumnDef{Name: p.parseIdent(), Type: p.parseType()})
				if !p.acceptOp(",") {
					break
				}
			}
			p.expectOp(")")
		}
	}
	if p.acceptKwSeq("WITH", "CONNECTION") {
		if !p.acceptKw("DEFAULT") {
			ct.Connection = p.parseName()
		} else {
			ct.ConnectionDefault = true
		}
	}
	switch {
	case p.isKw("OPTIONS"):
		ct.Options = p.parseOptionList()
	case kind == "EXTERNAL":
		p.unexpected("OPTIONS")
	}
	if kind == "" && p.acceptKw("AS") {
		ct.AsQuery = p.parseQuery()
	}
	if kind == "" && !ct.HasColumns && ct.Source == nil && ct.AsQuery == nil {
		p.errorf(p.peekN(-1).Pos, "CREATE TABLE needs columns, AS query, LIKE, COPY or CLONE")
	}
	return ct
}

// parseTableSource parses the table after LIKE, COPY or CLONE.
func (p *Parser) parseTableSource(kind string) *ast.TableSource {
	src := &ast.TableSource{Kind: kind, Name: p.parseRelationName()}
	if kind != "LIKE" && p.acceptKwSeq("FOR", "SYSTEM_TIME", "AS", "OF") {
		src.SystemTime = p.parseExpr()
	}
	return src
}

// parseGoogleElements parses a table's columns and keys, at least one column.
func (p *Parser) parseGoogleElements(ct *ast.CreateTable) {
	for {
		switch {
		case p.acceptKwSeq("PRIMARY", "KEY"):
			tc := &ast.TableConstraint{Kind: ast.TablePrimaryKey, Columns: p.parseKeyColumns()}
			p.parseNotEnforced(&tc.NotEnforced)
			if p.isKw("OPTIONS") {
				tc.Options = p.parseOptionList()
			}
			ct.Constraints = append(ct.Constraints, tc)
		case p.isKw("CONSTRAINT") || p.isKwSeq("FOREIGN", "KEY"):
			tc := &ast.TableConstraint{Kind: ast.TableForeignKey}
			if p.acceptKw("CONSTRAINT") {
				id := p.parseIdent()
				tc.Name = &id
			}
			p.expectKw("FOREIGN", "KEY")
			tc.Columns = p.parseKeyColumns()
			p.expectKw("REFERENCES")
			tc.References = p.parseGoogleReferences()
			p.parseNotEnforced(&tc.NotEnforced)
			if p.isKw("OPTIONS") {
				tc.Options = p.parseOptionList()
			}
			ct.Constraints = append(ct.Constraints, tc)
		default:
			ct.Columns = append(ct.Columns, p.parseGoogleColumn())
		}
		if !p.acceptOp(",") {
			break
		}
	}
	if len(ct.Columns) == 0 {
		p.errorf(p.peek().Pos, "a table needs at least one column")
	}
}

// parseNotEnforced requires NOT ENFORCED: BigQuery enforces no key.
func (p *Parser) parseNotEnforced(dst *bool) {
	p.expectKw("NOT", "ENFORCED")
	*dst = true
}

// parseKeyColumns parses (a, b) after PRIMARY KEY or FOREIGN KEY.
func (p *Parser) parseKeyColumns() []*ast.IndexElem {
	var cols []*ast.IndexElem
	for _, id := range p.parseParenIdentList() {
		cols = append(cols, &ast.IndexElem{Expr: &ast.Name{Parts: []ast.Ident{id}}})
	}
	return cols
}

// parseGoogleReferences parses table(columns) after REFERENCES.
func (p *Parser) parseGoogleReferences() *ast.References {
	return &ast.References{Table: p.parseRelationName(), Columns: p.parseParenIdentList()}
}

// parseGoogleColumn parses a column of a table: its type, then a default, a
// generated value or an identity, then keys and NOT NULL in any order, then
// OPTIONS.
func (p *Parser) parseGoogleColumn() *ast.ColumnDef {
	cd := &ast.ColumnDef{Name: p.parseIdent(), Type: p.parseSchemaType(false)}
	add := func(cc *ast.ColumnConstraint) { cd.Constraints = append(cd.Constraints, cc) }
	identity := func(generated string) {
		p.expectKw("AS", "IDENTITY")
		cc := &ast.ColumnConstraint{Kind: ast.IdentityConstraint, Generated: generated}
		p.expectOp("(")
		if p.acceptKwSeq("START", "WITH") {
			cc.Identity = append(cc.Identity, ast.SequenceOption{Name: "START WITH", Value: p.parseSignedNumber()})
		}
		if p.acceptKwSeq("INCREMENT", "BY") {
			cc.Identity = append(cc.Identity, ast.SequenceOption{Name: "INCREMENT BY", Value: p.parseSignedNumber()})
		}
		p.expectOp(")")
		add(cc)
	}
	switch {
	case p.acceptKw("DEFAULT"):
		add(&ast.ColumnConstraint{Kind: ast.DefaultConstraint, Expr: p.parseExpr()})
	case p.isKw("AS") && p.isKwAt(1, "IDENTITY"):
		identity("")
	case p.isKwSeq("GENERATED", "BY", "DEFAULT"):
		p.pos += 3
		identity("BY DEFAULT")
	case p.isKwSeq("GENERATED", "ALWAYS", "AS") && p.isKwAt(3, "IDENTITY"):
		p.pos += 2
		identity("ALWAYS")
	case p.isKwSeq("GENERATED", "ALWAYS", "AS") || p.isKw("AS") && p.isOpAt(1, "("):
		// [GENERATED ALWAYS] AS (expr) [STORED] [OPTIONS (...)]: BigQuery
		// takes only some functions here, and says so at analysis.
		p.acceptKwSeq("GENERATED", "ALWAYS")
		p.expectKw("AS")
		p.expectOp("(")
		cc := &ast.ColumnConstraint{Kind: ast.GeneratedConstraint, Expr: p.parseExpr()}
		p.expectOp(")")
		if p.acceptKw("STORED") {
			cc.Generated = "STORED"
			if p.isKw("OPTIONS") {
				cc.Options = p.parseOptionList()
			}
		}
		add(cc)
	}
	seen := map[ast.ColumnConstraintKind]bool{}
	for {
		pos := p.peek().Pos
		var cc *ast.ColumnConstraint
		switch {
		case p.acceptKwSeq("PRIMARY", "KEY"):
			cc = &ast.ColumnConstraint{Kind: ast.PrimaryKeyConstraint}
			p.parseNotEnforced(&cc.NotEnforced)
		case p.isKw("CONSTRAINT", "REFERENCES"):
			cc = &ast.ColumnConstraint{Kind: ast.ReferencesConstraint}
			if p.acceptKw("CONSTRAINT") {
				id := p.parseIdent()
				cc.Name = &id
			}
			p.expectKw("REFERENCES")
			cc.References = p.parseGoogleReferences()
			p.parseNotEnforced(&cc.NotEnforced)
		case p.acceptKwSeq("NOT", "NULL"):
			cc = &ast.ColumnConstraint{Kind: ast.NotNullConstraint}
		}
		if cc == nil {
			break
		}
		if seen[cc.Kind] {
			p.errorf(pos, "an attribute of the column is written twice")
		}
		seen[cc.Kind] = true
		add(cc)
	}
	if p.isKw("OPTIONS") {
		cd.Options = p.parseOptionList()
	}
	return cd
}

// parseSchemaType parses the type of a column, or of a field or element
// within one: STRING may say COLLATE, and a STRUCT's fields and an ARRAY's
// element may say NOT NULL and OPTIONS (...), which field asks for.
func (p *Parser) parseSchemaType(field bool) *ast.DataType {
	t := p.peek()
	var dt *ast.DataType
	switch {
	case t.Kind == lexer.Ident && t.Value == "ARRAY" && p.isOpAt(1, "<"):
		p.pos += 2
		dt = &ast.DataType{Name: t.Text, Angle: true, Of: p.parseSchemaType(true)}
		p.expectCloseAngle()
	case t.Kind == lexer.Ident && t.Value == "STRUCT" && p.isOpAt(1, "<>"):
		p.pos += 2
		dt = &ast.DataType{Name: t.Text, Angle: true}
	case t.Kind == lexer.Ident && t.Value == "STRUCT" && p.isOpAt(1, "<"):
		p.pos += 2
		dt = &ast.DataType{Name: t.Text, Angle: true}
		for !p.isCloseAngle() {
			dt.Struct = append(dt.Struct, &ast.StructField{Name: p.parseAnyIdent(), Type: p.parseSchemaType(true)})
			if !p.acceptOp(",") {
				break
			}
		}
		p.expectCloseAngle()
	default:
		dt = p.parseType()
	}
	if p.acceptKw("COLLATE") {
		dt.Collate = p.parseStringValue()
	}
	if field {
		dt.NotNull = p.acceptKwSeq("NOT", "NULL")
		if p.isKw("OPTIONS") {
			dt.Options = p.parseOptionList()
		}
	}
	return dt
}

// parseOptionList parses OPTIONS (name = value, ...). The list may be empty;
// the result is never nil, so that OPTIONS () is kept.
func (p *Parser) parseOptionList() []ast.Option {
	p.expectKw("OPTIONS")
	p.expectOp("(")
	opts := []ast.Option{}
	for !p.isOp(")") || len(opts) > 0 { // no trailing comma
		t := p.peek()
		if t.Kind != lexer.QuotedIdent && !p.canBeIdent() {
			p.unexpected("option name")
		}
		p.next()
		o := ast.Option{Name: t.Text}
		switch {
		case p.acceptOp("="):
			o.Assign = "="
		case p.acceptOp("+="):
			o.Assign = "+="
		case p.acceptOp("-="):
			o.Assign = "-="
		default:
			p.unexpected(`"="`)
		}
		o.Value = p.parseExpr()
		opts = append(opts, o)
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp(")")
	return opts
}

// parseGoogleView parses CREATE [MATERIALIZED] VIEW after VIEW.
func (p *Parser) parseGoogleView(orReplace, materialized bool) ast.Statement {
	v := &ast.CreateView{OrReplace: orReplace, Materialized: materialized}
	v.IfNotExists = p.parseCreateIfNotExists(orReplace)
	v.Name = p.parseRelationName()
	if !materialized && p.acceptOp("(") {
		any := false
		for {
			v.Columns = append(v.Columns, p.parseIdent())
			var opts []ast.Option
			if p.isKw("OPTIONS") {
				opts = p.parseOptionList()
				any = true
			}
			v.ColumnOptions = append(v.ColumnOptions, opts)
			if !p.acceptOp(",") {
				break
			}
		}
		p.expectOp(")")
		if !any {
			v.ColumnOptions = nil
		}
	}
	if !materialized && p.acceptKwSeq("SQL", "SECURITY") {
		p.expectKw("INVOKER")
		v.SQLSecurity = "INVOKER"
	}
	if materialized && p.acceptKwSeq("PARTITION", "BY") {
		v.PartitionExpr = p.parseExpr()
	}
	if materialized && p.acceptKwSeq("CLUSTER", "BY") {
		v.ClusterBy = p.parseExprList()
	}
	if p.isKw("OPTIONS") {
		v.Options = p.parseOptionList()
	}
	p.expectKw("AS")
	if materialized && p.acceptKwSeq("REPLICA", "OF") {
		v.ReplicaOf = p.parseRelationName()
		return v
	}
	v.Query = p.parseQuery()
	return v
}

// parseGoogleSchema parses CREATE [EXTERNAL] SCHEMA after SCHEMA.
func (p *Parser) parseGoogleSchema(orReplace, external bool) ast.Statement {
	if orReplace {
		p.errorf(p.peekN(-1).Pos, "OR REPLACE is not allowed with CREATE SCHEMA")
	}
	cs := &ast.CreateSchema{External: external, IfNotExists: p.parseIfNotExists(), Name: p.parseName()}
	if !external && p.acceptKwSeq("DEFAULT", "COLLATE") {
		cs.DefaultCollate = p.parseStringValue()
	}
	if external && p.acceptKwSeq("WITH", "CONNECTION") {
		cs.Connection = p.parseName()
	}
	switch {
	case p.isKw("OPTIONS"):
		cs.Options = p.parseOptionList()
	case external:
		p.unexpected("OPTIONS")
	}
	return cs
}

// googleDropKinds are the objects DROP takes, longer names first.
var googleDropKinds = [][]string{
	{"MATERIALIZED", "VIEW"}, {"SNAPSHOT", "TABLE"}, {"EXTERNAL", "TABLE"}, {"EXTERNAL", "SCHEMA"},
	{"TABLE", "FUNCTION"}, {"ROW", "ACCESS", "POLICY"}, {"SEARCH", "INDEX"}, {"VECTOR", "INDEX"},
	{"PROPERTY", "GRAPH"}, {"SCHEMA"}, {"TABLE"}, {"VIEW"}, {"FUNCTION"}, {"PROCEDURE"}, {"MODEL"},
	{"CAPACITY"}, {"RESERVATION"}, {"ASSIGNMENT"}, {"CONNECTION"}, {"DATA_POLICY"},
}

// parseGoogleDrop parses DROP ..., the word DROP already read.
func (p *Parser) parseGoogleDrop() ast.Statement {
	d := &ast.Drop{}
	if p.acceptKwSeq("ALL", "ROW", "ACCESS", "POLICIES") {
		d.Kind = "ALL ROW ACCESS POLICIES"
		p.expectKw("ON")
		d.On = p.parseRelationName()
		return d
	}
	for _, words := range googleDropKinds {
		if p.acceptKwSeq(words...) {
			d.Kind = joinWords(words)
			break
		}
	}
	if d.Kind == "" {
		p.unexpected("object type")
	}
	d.IfExists = p.parseIfExists()
	switch d.Kind {
	case "TABLE", "VIEW", "MATERIALIZED VIEW", "SNAPSHOT TABLE", "EXTERNAL TABLE":
		d.Names = []*ast.Name{p.parseRelationName()}
	default:
		d.Names = []*ast.Name{p.parseName()}
	}
	if d.Kind == "FUNCTION" && p.acceptOp("(") {
		d.Params = []*ast.DataType{}
		for !p.isOp(")") || len(d.Params) > 0 {
			d.Params = append(d.Params, p.parseType())
			if !p.acceptOp(",") {
				break
			}
		}
		p.expectOp(")")
	}
	switch d.Kind {
	case "ROW ACCESS POLICY", "SEARCH INDEX", "VECTOR INDEX":
		p.expectKw("ON")
		d.On = p.parseRelationName()
	case "SCHEMA":
		d.Behavior = p.acceptOneOf("CASCADE", "RESTRICT")
	}
	return d
}

// parseUndrop parses UNDROP SCHEMA [IF NOT EXISTS] name.
func (p *Parser) parseUndrop() ast.Statement {
	p.expectKw("UNDROP")
	p.expectKw("SCHEMA")
	return &ast.Undrop{IfNotExists: p.parseIfNotExists(), Name: p.parseName()}
}
