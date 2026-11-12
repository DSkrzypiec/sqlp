package gen

import "sqlp/ast"

// BigQuery's procedural language (UtilityFeatures.Scripting).

// scriptStmt writes a statement of BigQuery's scripts, reporting whether s
// was one.
func (g *Generator) scriptStmt(s ast.Statement) bool {
	if !g.d.Features.Utility.Scripting {
		switch s.(type) {
		case *ast.Declare, *ast.Assign, *ast.Block, *ast.IfStmt, *ast.CaseStmt, *ast.LoopStmt,
			*ast.LoopControl, *ast.RaiseStmt, *ast.ExecuteImmediate, *ast.Assert:
			g.warn("BigQuery's procedural statements")
		}
	}
	switch s := s.(type) {
	case *ast.Declare:
		g.w("DECLARE ")
		g.identList(s.Names)
		if s.Type != nil {
			g.w(" ")
			g.dataType(s.Type)
		}
		if s.Default != nil {
			g.w(" DEFAULT ")
			g.expr(s.Default)
		}
	case *ast.Assign:
		g.w("SET ")
		if s.Paren {
			g.w("(")
		}
		g.exprList(s.Targets)
		if s.Paren {
			g.w(")")
		}
		g.w(" = ")
		g.expr(s.Value)
	case *ast.Block:
		g.scriptLabel(s.Label)
		g.w("BEGIN")
		g.statements(s.Body)
		if s.HasException {
			g.w(" EXCEPTION WHEN ERROR THEN")
			g.statements(s.Exception)
		}
		g.w(" END")
		g.endLabel(s.Label, s.EndLabel)
	case *ast.IfStmt:
		for i, br := range s.Branches {
			g.w(map[bool]string{true: "IF ", false: " ELSEIF "}[i == 0])
			g.expr(br.Cond)
			g.w(" THEN")
			g.statements(br.Body)
		}
		if s.HasElse {
			g.w(" ELSE")
			g.statements(s.Else)
		}
		g.w(" END IF")
	case *ast.CaseStmt:
		g.w("CASE")
		if s.Operand != nil {
			g.w(" ")
			g.expr(s.Operand)
		}
		for _, br := range s.Whens {
			g.w(" WHEN ")
			g.expr(br.Cond)
			g.w(" THEN")
			g.statements(br.Body)
		}
		if s.HasElse {
			g.w(" ELSE")
			g.statements(s.Else)
		}
		g.w(" END CASE")
	case *ast.LoopStmt:
		g.scriptLabel(s.Label)
		g.w(s.Kind)
		switch s.Kind {
		case "WHILE":
			g.w(" ")
			g.expr(s.Cond)
			g.w(" DO")
		case "FOR":
			g.w(" ")
			g.ident(*s.Var)
			g.w(" IN (")
			g.query(s.Query)
			g.w(") DO")
		}
		g.statements(s.Body)
		if s.Kind == "REPEAT" {
			g.w(" UNTIL ")
			g.expr(s.Cond)
		}
		g.w(" END ", s.Kind)
		g.endLabel(s.Label, s.EndLabel)
	case *ast.LoopControl:
		g.w(s.Word)
		if s.Label != nil {
			g.w(" ")
			g.ident(*s.Label)
		}
	case *ast.RaiseStmt:
		g.w("RAISE")
		if s.Message != nil {
			g.w(" USING MESSAGE = ")
			g.expr(s.Message)
		}
	case *ast.ExecuteImmediate:
		g.w("EXECUTE IMMEDIATE ")
		g.expr(s.SQL)
		if len(s.Into) > 0 {
			g.w(" INTO ")
			g.identList(s.Into)
		}
		if len(s.Using) > 0 {
			g.w(" USING ")
			g.selectItems(s.Using)
		}
	case *ast.Assert:
		g.w("ASSERT ")
		g.expr(s.Expr)
		if s.Description != nil {
			g.w(" AS ")
			g.expr(s.Description)
		}
	default:
		return false
	}
	return true
}

// statements writes a statement list: each statement after a space and with
// its ';'.
func (g *Generator) statements(list []ast.Statement) {
	for _, s := range list {
		g.w(" ")
		g.stmt(s)
		g.w(";")
	}
}

// label writes a block's or loop's label and its colon.
func (g *Generator) scriptLabel(l *ast.Ident) {
	if l != nil {
		g.ident(*l)
		g.w(": ")
	}
}

func (g *Generator) endLabel(l *ast.Ident, written bool) {
	if l != nil && written {
		g.w(" ")
		g.ident(*l)
	}
}
