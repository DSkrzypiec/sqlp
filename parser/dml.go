package parser

import (
	"sqlp/ast"
	"sqlp/lexer"
)

// conflictActions are SQLite's OR <action> / ON CONFLICT <action> words.
var conflictActions = []string{"ROLLBACK", "ABORT", "FAIL", "IGNORE", "REPLACE"}

// parseOrAction parses SQLite's `OR ROLLBACK|ABORT|FAIL|IGNORE|REPLACE`.
func (p *Parser) parseOrAction() string {
	if !p.d.Features.InsertOr || !p.isKw("OR") {
		return ""
	}
	p.next()
	act := p.acceptOneOf(conflictActions...)
	if act == "" {
		p.unexpected("conflict action")
	}
	return act
}

func (p *Parser) parseInsert(with *ast.With) ast.Statement {
	ins := &ast.Insert{With: with}
	if p.acceptKw("REPLACE") {
		if !p.d.Features.InsertOr {
			p.unsupported("REPLACE INTO")
		}
		ins.Replace = true
	} else {
		p.expectKw("INSERT")
		ins.Or = p.parseOrAction()
	}
	p.expectKw("INTO")
	ins.Table = p.parseRelationName()
	if p.acceptKw("AS") {
		id := p.parseIdent()
		ins.Alias = &id
	}
	if p.isOp("(") && !p.isQueryStart() {
		p.next()
		ins.Columns = []ast.Expr{p.parseSetTarget()}
		for p.acceptOp(",") {
			ins.Columns = append(ins.Columns, p.parseSetTarget())
		}
		p.expectOp(")")
	}
	if p.d.Features.ExtendedDDL && p.acceptKw("OVERRIDING") {
		ins.Overriding = p.parseOverriding()
	}
	if p.isKwSeq("DEFAULT", "VALUES") {
		if p.d.Features.ExtendedDDL && (ins.Columns != nil || ins.Overriding != "") {
			p.unexpected("query") // PostgreSQL: DEFAULT VALUES stands alone
		}
		p.pos += 2
		ins.DefaultValues = true
	} else {
		ins.Source = p.parseQuery()
	}
	for p.isKwSeq("ON", "CONFLICT") {
		if p.d.Features.UpsertMulti && (ins.DefaultValues || len(ins.OnConflict) > 0 && ins.OnConflict[len(ins.OnConflict)-1].Target == nil) {
			p.unexpected("end of INSERT") // SQLite: no upsert after DEFAULT VALUES or after one without a target
		}
		ins.OnConflict = append(ins.OnConflict, p.parseOnConflict())
		if !p.d.Features.UpsertMulti {
			break
		}
	}
	ins.Returning = p.parseReturning()
	return ins
}

// parseOverriding parses `SYSTEM|USER VALUE`, after OVERRIDING.
func (p *Parser) parseOverriding() string {
	who := p.acceptOneOf("SYSTEM", "USER")
	if who == "" {
		p.unexpected("SYSTEM or USER")
	}
	p.expectKw("VALUE")
	return who
}

func (p *Parser) parseOnConflict() *ast.OnConflict {
	p.expectKw("ON", "CONFLICT")
	oc := &ast.OnConflict{}
	switch {
	case p.isOp("("):
		p.next()
		oc.Target = p.parseIndexElems()
		p.expectOp(")")
		if p.acceptKw("WHERE") {
			oc.TargetWhere = p.parseExpr()
		}
	case p.d.Features.ConflictOnConstraint && p.acceptKwSeq("ON", "CONSTRAINT"):
		id := p.parseIdent()
		oc.Constraint = &id
	}
	p.expectKw("DO")
	if p.acceptKw("NOTHING") {
		oc.DoNothing = true
		return oc
	}
	p.expectKw("UPDATE", "SET")
	oc.Updates = p.parseAssignments()
	if p.acceptKw("WHERE") {
		oc.Where = p.parseExpr()
	}
	return oc
}

func (p *Parser) parseAssignments() []*ast.Assignment {
	as := []*ast.Assignment{p.parseAssignment()}
	for p.acceptOp(",") {
		as = append(as, p.parseAssignment())
	}
	return as
}

func (p *Parser) parseAssignment() *ast.Assignment {
	a := &ast.Assignment{}
	if p.acceptOp("(") {
		a.Paren = true
		a.Targets = []ast.Expr{p.parseSetTarget()}
		for p.acceptOp(",") {
			a.Targets = append(a.Targets, p.parseSetTarget())
		}
		p.expectOp(")")
	} else {
		a.Targets = []ast.Expr{p.parseSetTarget()}
	}
	p.expectOp("=")
	a.Value = p.parseExpr()
	return a
}

// parseSetTarget parses a column, with PostgreSQL's a.b and a[1] indirection.
func (p *Parser) parseSetTarget() ast.Expr {
	name := &ast.Name{Parts: []ast.Ident{p.parseIdent()}}
	if !p.d.Features.ArrayLiterals {
		return name
	}
	var e ast.Expr = name
	for {
		switch {
		case p.isOp("."):
			p.next()
			if n, ok := e.(*ast.Name); ok {
				n.Parts = append(n.Parts, p.parseAnyIdent())
			} else {
				e = &ast.Field{Expr: e, Name: p.parseAnyIdent()}
			}
		case p.isOp("["):
			e = p.parseSubscript(e)
		default:
			return e
		}
	}
}

// parseWhere parses an optional WHERE, including PostgreSQL's CURRENT OF.
func (p *Parser) parseWhere() ast.Expr {
	if !p.acceptKw("WHERE") {
		return nil
	}
	if p.d.Features.ServerUtilities && p.acceptKwSeq("CURRENT", "OF") {
		return &ast.Word{Text: "CURRENT OF " + p.parseIdent().Name}
	}
	return p.parseExpr()
}

func (p *Parser) parseUpdate(with *ast.With) ast.Statement {
	p.expectKw("UPDATE")
	u := &ast.Update{With: with, Or: p.parseOrAction()}
	u.Only, u.Table = p.parseRelationExpr()
	u.Alias = p.parseDMLAlias()
	u.Indexed = p.parseIndexed()
	p.expectKw("SET")
	u.Set = p.parseAssignments()
	if p.acceptKw("FROM") {
		u.From = p.parseFromList()
	}
	u.Where = p.parseWhere()
	u.Returning = p.parseReturning()
	if p.d.Features.OrderLimitInDML {
		if p.acceptKwSeq("ORDER", "BY") {
			u.OrderBy = p.parseOrderItems()
		}
		if p.isKw("LIMIT") {
			p.parseLimit(&u.Limit)
		}
	}
	return u
}

func (p *Parser) parseDelete(with *ast.With) ast.Statement {
	p.expectKw("DELETE", "FROM")
	d := &ast.Delete{With: with}
	d.Only, d.Table = p.parseRelationExpr()
	d.Alias = p.parseDMLAlias()
	d.Indexed = p.parseIndexed()
	if p.d.Features.DeleteUsing && p.acceptKw("USING") {
		d.Using = p.parseFromList()
	}
	d.Where = p.parseWhere()
	d.Returning = p.parseReturning()
	if p.d.Features.OrderLimitInDML {
		if p.acceptKwSeq("ORDER", "BY") {
			d.OrderBy = p.parseOrderItems()
		}
		if p.isKw("LIMIT") {
			p.parseLimit(&d.Limit)
		}
	}
	return d
}

// parseDMLAlias parses the optional `[AS] alias` after an UPDATE/DELETE target.
func (p *Parser) parseDMLAlias() *ast.Ident {
	switch {
	case p.acceptKw("AS"):
		id := p.parseIdent()
		return &id
	case p.d.Features.BareDMLAlias && p.peek().Kind != lexer.EOF && p.canBeIdent() && !p.isKw("SET"):
		id := p.parseIdent()
		return &id
	}
	return nil
}

func (p *Parser) parseIndexed() *ast.Indexed {
	if !p.d.Features.IndexedBy {
		return nil
	}
	switch {
	case p.acceptKwSeq("INDEXED", "BY"):
		return &ast.Indexed{Name: p.parseIdent()}
	case p.acceptKwSeq("NOT", "INDEXED"):
		return &ast.Indexed{Not: true}
	}
	return nil
}

// parseRelationList parses [ONLY] name [*], ...
func (p *Parser) parseRelationList() []*ast.TableRef {
	var refs []*ast.TableRef
	for {
		r := &ast.TableRef{}
		r.Only, r.Name = p.parseRelationExpr()
		refs = append(refs, r)
		if !p.acceptOp(",") {
			return refs
		}
	}
}

func (p *Parser) parseTruncate() ast.Statement {
	p.expectKw("TRUNCATE")
	p.acceptKw("TABLE")
	t := &ast.Truncate{Tables: p.parseRelationList()}
	switch {
	case p.acceptKwSeq("RESTART", "IDENTITY"):
		t.Identity = "RESTART IDENTITY"
	case p.acceptKwSeq("CONTINUE", "IDENTITY"):
		t.Identity = "CONTINUE IDENTITY"
	}
	t.Behavior = p.parseBehavior()
	return t
}

// parseMerge parses MERGE INTO target USING source ON cond WHEN ...
func (p *Parser) parseMerge(with *ast.With) ast.Statement {
	p.expectKw("MERGE", "INTO")
	m := &ast.Merge{With: with}
	m.Only, m.Target = p.parseRelationExpr()
	m.Alias = p.parseMergeAlias()
	p.expectKw("USING")
	m.Source = p.parseTableExpr()
	p.expectKw("ON")
	m.On = p.parseExpr()
	for p.isKw("WHEN") {
		m.Whens = append(m.Whens, p.parseMergeWhen())
	}
	if len(m.Whens) == 0 {
		p.unexpected("WHEN")
	}
	return m
}

func (p *Parser) parseMergeAlias() *ast.Alias {
	switch {
	case p.acceptKw("AS"):
		return &ast.Alias{Name: p.parseIdent()}
	case p.canBeIdent():
		return &ast.Alias{Name: p.parseIdent()}
	}
	return nil
}

func (p *Parser) parseMergeWhen() *ast.MergeWhen {
	p.expectKw("WHEN")
	w := &ast.MergeWhen{NotMatched: p.acceptKw("NOT")}
	p.expectKw("MATCHED")
	if p.acceptKw("AND") {
		w.Cond = p.parseExpr()
	}
	p.expectKw("THEN")
	switch {
	case p.acceptKwSeq("DO", "NOTHING"):
		w.DoNothing = true
	case !w.NotMatched && p.acceptKw("DELETE"):
		w.Delete = true
	case !w.NotMatched && p.acceptKwSeq("UPDATE", "SET"):
		w.Update = p.parseAssignments()
	case w.NotMatched && p.acceptKw("INSERT"):
		w.Insert = true
		if p.acceptOp("(") {
			for {
				w.Columns = append(w.Columns, p.parseSetTarget())
				if !p.acceptOp(",") {
					break
				}
			}
			p.expectOp(")")
		}
		if p.acceptKw("OVERRIDING") {
			w.Overriding = p.parseOverriding()
		}
		if p.acceptKwSeq("DEFAULT", "VALUES") {
			w.DefaultValues = true
		} else {
			p.expectKw("VALUES")
			p.expectOp("(")
			w.Values = p.parseExprList()
			p.expectOp(")")
		}
	default:
		p.unexpected("merge action")
	}
	return w
}
