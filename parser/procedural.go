package parser

import (
	"strings"

	"sqlp/ast"
	"sqlp/lexer"
)

// BigQuery's procedural language (UtilityFeatures.Scripting). The rules here
// were checked with dry runs; tools/corpusgen/bigquery/script.sql freezes
// them.

// scriptFrame is an enclosing block or loop, for BREAK, CONTINUE and labels.
type scriptFrame struct {
	label string // its key, "" when unlabelled
	loop  bool
}

// parseScriptStatement parses the statements only scripts have, or returns
// nil when the current word starts none.
func (p *Parser) parseScriptStatement(word string) ast.Statement {
	label := p.peekN(1).Kind == lexer.Op && p.peekN(1).Text == ":" && (p.peek().Kind == lexer.Ident || p.peek().Kind == lexer.QuotedIdent)
	if label {
		id := p.parseIdent()
		p.next()
		if !p.isKw("BEGIN", "LOOP", "WHILE", "REPEAT", "FOR") {
			p.unexpected("BEGIN or a loop after a label")
		}
		return p.parseLabelled(&id)
	}
	switch word {
	case "DECLARE":
		return p.parseDeclare()
	case "SET":
		return p.parseAssign()
	case "BEGIN":
		if p.isOpAt(1, ";") || p.peekN(1).Kind == lexer.EOF || p.isKwAt(1, "TRANSACTION") {
			p.next()
			p.acceptKw("TRANSACTION")
			return &ast.Begin{}
		}
		return p.parseLabelled(nil)
	case "START":
		if p.isKwAt(1, "TRANSACTION") {
			p.pos += 2
			return &ast.Begin{}
		}
	case "LOOP", "WHILE", "REPEAT", "FOR":
		return p.parseLabelled(nil)
	case "IF":
		return p.parseIfStmt()
	case "CASE":
		return p.parseCaseStmt()
	case "BREAK", "LEAVE", "CONTINUE", "ITERATE":
		return p.parseLoopControl()
	case "RAISE":
		p.next()
		r := &ast.RaiseStmt{}
		if p.acceptKwSeq("USING", "MESSAGE") {
			p.expectOp("=")
			r.Message = p.parseExpr()
		} else if p.handlers == 0 {
			p.errorf(p.peekN(-1).Pos, "RAISE without a message only re-raises within an exception handler")
		}
		return r
	case "RETURN":
		p.next()
		return &ast.ReturnStmt{}
	case "CALL":
		p.next()
		return &ast.Call{Func: p.parseFuncCall(p.parseName())}
	case "EXECUTE":
		if p.isKwAt(1, "IMMEDIATE") {
			return p.parseExecuteImmediate()
		}
	case "ASSERT":
		p.next()
		a := &ast.Assert{Expr: p.parseExpr()}
		if p.acceptKw("AS") {
			a.Description = p.parseStringLit()
		}
		return a
	}
	return nil
}

// parseStatementList parses statements, each ending with ';', up to one of
// the words in ends. DECLARE may open the list when declare is set.
func (p *Parser) parseStatementList(declare bool, ends ...string) []ast.Statement {
	outer := p.declareClosed
	p.declareClosed = !declare
	var list []ast.Statement
	for !p.isKw(ends...) {
		if p.peek().Kind == lexer.EOF {
			p.unexpected(ends[0])
		}
		list = append(list, p.parseStatement())
		p.expectOp(";")
	}
	p.declareClosed = outer
	return list
}

// parseDeclare parses DECLARE names [type] [DEFAULT expr].
func (p *Parser) parseDeclare() ast.Statement {
	if p.declareClosed {
		p.errorf(p.peek().Pos, "DECLARE comes only at the start of a block or script")
	}
	p.next()
	d := &ast.Declare{Names: []ast.Ident{p.parseIdent()}}
	for p.acceptOp(",") {
		d.Names = append(d.Names, p.parseIdent())
	}
	if !p.isKw("DEFAULT") {
		d.Type = p.parseType()
	}
	if p.acceptKw("DEFAULT") {
		d.Default = p.parseExpr()
	}
	return d
}

// parseAssign parses SET name = expr, SET (names) = expr and SET
// @@system_variable = expr.
func (p *Parser) parseAssign() ast.Statement {
	p.next()
	a := &ast.Assign{}
	target := func() ast.Expr {
		if p.isOp("@@") {
			return p.parsePrefix()
		}
		return &ast.Name{Parts: []ast.Ident{p.parseIdent()}}
	}
	if p.acceptOp("(") {
		a.Paren = true
		for {
			a.Targets = append(a.Targets, target())
			if !p.acceptOp(",") {
				break
			}
		}
		p.expectOp(")")
	} else {
		a.Targets = []ast.Expr{target()}
	}
	p.expectOp("=")
	a.Value = p.parseExpr()
	return a
}

// parseLabelled parses a block or a loop, with label when one was written.
func (p *Parser) parseLabelled(label *ast.Ident) ast.Statement {
	key := ""
	if label != nil {
		key = p.labelKey(*label)
		for _, f := range p.frames {
			if f.label == key {
				p.errorf(p.peekN(-2).Pos, "label %s is already in use", label.Name)
			}
		}
	}
	kind := p.next().Value
	p.frames = append(p.frames, scriptFrame{label: key, loop: kind != "BEGIN"})
	defer func() { p.frames = p.frames[:len(p.frames)-1] }()
	endLabel := func() bool {
		if label == nil || !(p.peek().Kind == lexer.Ident && !p.d.IsReserved(p.peek().Value) || p.peek().Kind == lexer.QuotedIdent) {
			return false
		}
		pos := p.peek().Pos
		if id := p.parseIdent(); p.labelKey(id) != key {
			p.errorf(pos, "the label after END is %s, not %s", id.Name, label.Name)
		}
		return true
	}
	if kind == "BEGIN" {
		b := &ast.Block{Label: label, Body: p.parseStatementList(true, "END", "EXCEPTION")}
		p.parseHandler(b)
		p.expectKw("END")
		b.EndLabel = endLabel()
		return b
	}
	l := &ast.LoopStmt{Label: label, Kind: kind}
	switch kind {
	case "LOOP":
		l.Body = p.parseStatementList(false, "END")
	case "WHILE":
		l.Cond = p.parseExpr()
		p.expectKw("DO")
		l.Body = p.parseStatementList(false, "END")
	case "REPEAT":
		l.Body = p.parseStatementList(false, "UNTIL")
		p.expectKw("UNTIL")
		l.Cond = p.parseExpr()
	case "FOR":
		id := p.parseIdent()
		l.Var = &id
		p.expectKw("IN")
		p.expectOp("(")
		l.Query = p.parseQuery()
		p.expectOp(")")
		p.expectKw("DO")
		l.Body = p.parseStatementList(false, "END")
	}
	p.expectKw("END")
	p.expectKw(kind)
	l.EndLabel = endLabel()
	return l
}

// parseHandler parses a block's EXCEPTION WHEN ERROR THEN statements.
func (p *Parser) parseHandler(b *ast.Block) {
	if !p.acceptKw("EXCEPTION") {
		return
	}
	p.expectKw("WHEN", "ERROR", "THEN")
	b.HasException = true
	p.handlers++
	b.Exception = p.parseStatementList(false, "END")
	p.handlers--
}

// labelKey returns the key of a label, which ignores case as names do.
func (p *Parser) labelKey(id ast.Ident) string { return strings.ToUpper(id.Name) }

// parseLoopControl parses BREAK, LEAVE, CONTINUE or ITERATE [label].
func (p *Parser) parseLoopControl() ast.Statement {
	pos := p.peek().Pos
	lc := &ast.LoopControl{Word: p.next().Value}
	again := lc.Word == "CONTINUE" || lc.Word == "ITERATE"
	if t := p.peek(); t.Kind == lexer.QuotedIdent || t.Kind == lexer.Ident && !p.d.IsReserved(t.Value) {
		id := p.parseIdent()
		lc.Label = &id
		for i := len(p.frames) - 1; i >= 0; i-- {
			if f := p.frames[i]; f.label == p.labelKey(id) {
				if again && !f.loop {
					p.errorf(pos, "%s with a label needs the label of a loop", lc.Word)
				}
				return lc
			}
		}
		p.errorf(pos, "label %s is not that of an enclosing block or loop", id.Name)
	}
	for _, f := range p.frames {
		if f.loop {
			return lc
		}
	}
	p.errorf(pos, "%s without a label is only allowed in a loop", lc.Word)
	return lc
}

// parseIfStmt parses IF ... END IF.
func (p *Parser) parseIfStmt() ast.Statement {
	p.next()
	s := &ast.IfStmt{}
	for {
		br := &ast.CondBranch{Cond: p.parseExpr()}
		p.expectKw("THEN")
		br.Body = p.parseStatementList(false, "ELSEIF", "ELSE", "END")
		s.Branches = append(s.Branches, br)
		if !p.acceptKw("ELSEIF") {
			break
		}
	}
	if p.acceptKw("ELSE") {
		s.HasElse = true
		s.Else = p.parseStatementList(false, "END")
	}
	p.expectKw("END", "IF")
	return s
}

// parseCaseStmt parses CASE [operand] WHEN ... END CASE.
func (p *Parser) parseCaseStmt() ast.Statement {
	p.next()
	s := &ast.CaseStmt{}
	if !p.isKw("WHEN") {
		s.Operand = p.parseExpr()
	}
	for p.acceptKw("WHEN") {
		br := &ast.CondBranch{Cond: p.parseExpr()}
		p.expectKw("THEN")
		br.Body = p.parseStatementList(false, "WHEN", "ELSE", "END")
		s.Whens = append(s.Whens, br)
	}
	if len(s.Whens) == 0 {
		p.unexpected("WHEN")
	}
	if p.acceptKw("ELSE") {
		s.HasElse = true
		s.Else = p.parseStatementList(false, "END")
	}
	p.expectKw("END", "CASE")
	return s
}

// parseExecuteImmediate parses EXECUTE IMMEDIATE sql [INTO vars] [USING
// expr [AS name], ...].
func (p *Parser) parseExecuteImmediate() ast.Statement {
	p.pos += 2
	e := &ast.ExecuteImmediate{SQL: p.parseExpr()}
	if p.acceptKw("INTO") {
		for {
			e.Into = append(e.Into, p.parseIdent())
			if !p.acceptOp(",") {
				break
			}
		}
	}
	if p.acceptKw("USING") {
		for {
			it := &ast.SelectItem{Expr: p.parseExpr()}
			if p.acceptKw("AS") {
				id := p.parseIdent()
				it.Alias = &id
			}
			e.Using = append(e.Using, it)
			if !p.acceptOp(",") {
				break
			}
		}
	}
	return e
}

// parseGoogleProcedure parses CREATE PROCEDURE after PROCEDURE.
func (p *Parser) parseGoogleProcedure(orReplace bool) ast.Statement {
	cf := &ast.CreateFunction{Procedure: true, OrReplace: orReplace}
	cf.IfNotExists = p.parseCreateIfNotExists(orReplace)
	cf.Name = p.parseName()
	p.expectOp("(")
	for !p.isOp(")") || len(cf.Params) > 0 {
		fp := &ast.FuncParam{Mode: p.acceptOneOf("IN", "OUT", "INOUT")}
		id := p.parseIdent()
		fp.Name = &id
		fp.Type = p.parseParamType(false)
		cf.Params = append(cf.Params, fp)
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp(")")
	if p.acceptKwSeq("EXTERNAL", "SECURITY") {
		p.expectKw("INVOKER")
		cf.ExternalSecurity = "INVOKER"
	}
	if cf.ExternalSecurity != "" || p.isKwSeq("WITH", "CONNECTION") {
		p.expectKw("WITH", "CONNECTION")
		cf.Connection = p.parseName()
		if p.isKw("OPTIONS") {
			cf.OptionList = p.parseOptionList()
		}
		p.expectKw("LANGUAGE")
		cf.Language = p.parseIdent().Name
		if p.acceptKw("AS") {
			cf.Code = p.parseStringValue()
		} else {
			cf.NoCode = true
		}
		return cf
	}
	if p.isKw("OPTIONS") {
		cf.OptionList = p.parseOptionList()
	}
	if !p.isKw("BEGIN") {
		p.unexpected("BEGIN")
	}
	// The body is a scope of its own: no label, handler or declaration
	// state carries in.
	frames, handlers := p.frames, p.handlers
	p.frames, p.handlers = nil, 0
	cf.Block = p.parseLabelled(nil).(*ast.Block)
	p.frames, p.handlers = frames, handlers
	return cf
}
