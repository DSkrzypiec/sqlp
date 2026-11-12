package gen

import (
	"strings"

	"sqlp/ast"
)

// sequenceOptions writes options separated by spaces, each preceded by lead.
func (g *Generator) sequenceOptions(opts []ast.SequenceOption, lead string) {
	for i, o := range opts {
		if i > 0 && lead == "" {
			g.w(" ")
		}
		g.w(lead, o.Name)
		if o.Value != nil {
			g.w(" ")
			g.expr(o.Value)
		}
		if o.Type != nil {
			g.w(" ")
			g.dataType(o.Type)
		}
	}
}

// extendedStmt renders the less common statements; false if s is unknown.
func (g *Generator) extendedStmt(s ast.Statement) bool {
	switch s := s.(type) {
	case *ast.Opaque:
		if g.transpiling() {
			g.warn("verbatim statement " + s.Verb)
		}
		g.w(s.Text)
	case *ast.Merge:
		g.merge(s)
	case *ast.Grant:
		g.grant(s)
	case *ast.Copy:
		g.copyStmt(s)
	case *ast.CommentOn:
		g.w("COMMENT ON ", s.Kind, " ")
		if s.Type != nil {
			g.dataType(s.Type)
		} else {
			g.name(s.Object)
		}
		if s.On != nil {
			g.w(" ON ")
			if s.OnDomain {
				g.w("DOMAIN ")
			}
			g.name(s.On)
		}
		g.w(" IS ")
		g.expr(s.Comment)
	case *ast.Do:
		g.w("DO")
		for _, part := range s.Parts {
			if part.Language != "" {
				g.w(" LANGUAGE ", part.Language)
			} else {
				g.w(" ")
				g.str(part.Code)
			}
		}
	case *ast.ReturnStmt:
		g.w("RETURN ")
		g.expr(s.Expr)
	case *ast.Call:
		g.w("CALL ")
		g.funcCall(s.Func)
	case *ast.Prepare:
		g.w("PREPARE ")
		g.ident(s.Name)
		if len(s.Types) > 0 {
			g.w(" (")
			for i, t := range s.Types {
				if i > 0 {
					g.w(", ")
				}
				g.dataType(t)
			}
			g.w(")")
		}
		g.w(" AS ")
		g.stmt(s.Statement)
	case *ast.Execute:
		g.w("EXECUTE ")
		g.ident(s.Name)
		if len(s.Args) > 0 {
			g.w("(")
			g.exprList(s.Args)
			g.w(")")
		}
	case *ast.Deallocate:
		g.w("DEALLOCATE ")
		if s.All {
			g.w("ALL")
		} else {
			g.ident(*s.Name)
		}
	case *ast.Listen:
		g.w("LISTEN ")
		g.ident(s.Channel)
	case *ast.Unlisten:
		g.w("UNLISTEN ")
		if s.Channel == nil {
			g.w("*")
		} else {
			g.ident(*s.Channel)
		}
	case *ast.Notify:
		g.w("NOTIFY ")
		g.ident(s.Channel)
		if s.Payload != nil {
			g.w(", ")
			g.str(*s.Payload)
		}
	case *ast.Lock:
		g.w("LOCK TABLE ")
		g.relationList(s.Tables)
		g.kw(s.Mode != "", "IN "+s.Mode+" MODE")
		g.kw(s.NoWait, "NOWAIT")
	case *ast.RefreshMatView:
		g.w("REFRESH MATERIALIZED VIEW")
		g.kw(s.Concurrently, "CONCURRENTLY")
		g.w(" ")
		g.name(s.Name)
		g.withData(s.WithData)
	case *ast.CreateDomain:
		g.w("CREATE DOMAIN ")
		g.name(s.Name)
		g.w(" AS ")
		g.dataType(s.Type)
		for _, c := range s.Constraints {
			g.w(" ")
			g.columnConstraint(c)
		}
	case *ast.CreateCompositeType:
		g.w("CREATE TYPE ")
		g.name(s.Name)
		if !s.Shell {
			g.w(" AS (")
			for i, c := range s.Columns {
				if i > 0 {
					g.w(", ")
				}
				g.columnDef(c)
			}
			g.w(")")
		}
	case *ast.CreateFunction:
		g.createFunction(s)
	case *ast.CreateTrigger:
		g.createTrigger(s)
	case *ast.CreateVirtualTable:
		g.w("CREATE VIRTUAL TABLE")
		g.kw(s.IfNotExists, "IF NOT EXISTS")
		g.w(" ")
		g.name(s.Name)
		g.w(" USING ")
		g.ident(s.Module)
		if s.HasArgs {
			g.w("(", strings.Join(s.Args, ", "), ")")
		}
	default:
		return false
	}
	return true
}

func (g *Generator) merge(s *ast.Merge) {
	g.withPrefix(s.With)
	g.w("MERGE INTO")
	g.kw(s.Only, "ONLY")
	g.w(" ")
	g.name(s.Target)
	g.alias(s.Alias)
	g.w(" USING ")
	g.tableExpr(s.Source)
	g.w(" ON ")
	g.expr(s.On)
	for _, w := range s.Whens {
		g.w(" WHEN")
		g.kw(w.NotMatched, "NOT")
		g.w(" MATCHED")
		if w.Cond != nil {
			g.w(" AND ")
			g.expr(w.Cond)
		}
		g.w(" THEN ")
		switch {
		case w.DoNothing:
			g.w("DO NOTHING")
		case w.Delete:
			g.w("DELETE")
		case w.Insert:
			g.w("INSERT")
			if len(w.Columns) > 0 {
				g.w(" (")
				g.exprList(w.Columns)
				g.w(")")
			}
			g.kw(w.Overriding != "", "OVERRIDING "+w.Overriding+" VALUE")
			if w.DefaultValues {
				g.w(" DEFAULT VALUES")
			} else {
				g.w(" VALUES (")
				g.exprList(w.Values)
				g.w(")")
			}
		default:
			g.w("UPDATE SET ")
			g.assignments(w.Update)
		}
	}
}

func (g *Generator) grant(s *ast.Grant) {
	if s.Revoke {
		g.w("REVOKE")
		g.kw(s.OptionFor != "", s.OptionFor+" OPTION FOR")
	} else {
		g.w("GRANT")
	}
	g.w(" ")
	if s.All {
		g.w("ALL PRIVILEGES")
		if len(s.Privileges) > 0 {
			g.w(" (")
			g.identList(s.Privileges[0].Columns)
			g.w(")")
		}
	} else {
		for i, p := range s.Privileges {
			if i > 0 {
				g.w(", ")
			}
			if s.IsRole {
				g.ident(p.Name)
			} else {
				g.w(strings.ToUpper(p.Name.Name))
			}
			if len(p.Columns) > 0 {
				g.w(" (")
				g.identList(p.Columns)
				g.w(")")
			}
		}
	}
	if !s.IsRole {
		g.w(" ON")
		if s.AllIn != "" {
			g.w(" ALL ", s.AllIn, " IN SCHEMA")
		} else {
			g.kw(s.ObjectKind != "", s.ObjectKind)
		}
		g.w(" ")
		g.nameList(s.Objects)
	}
	if s.Revoke {
		g.w(" FROM ")
	} else {
		g.w(" TO ")
	}
	g.exprList(s.Grantees)
	g.kw(s.WithOption != "", "WITH "+s.WithOption)
	if s.GrantedBy != nil {
		g.w(" GRANTED BY ")
		g.expr(s.GrantedBy)
	}
	g.kw(s.Behavior != "", s.Behavior)
}

func (g *Generator) copyStmt(s *ast.Copy) {
	g.w("COPY ")
	if s.Binary {
		g.w("BINARY ")
	}
	if s.Query != nil {
		g.w("(")
		g.stmt(s.Query)
		g.w(")")
	} else {
		g.name(s.Table)
		if len(s.Columns) > 0 {
			g.w(" (")
			g.identList(s.Columns)
			g.w(")")
		}
	}
	if s.To {
		g.w(" TO ")
	} else {
		g.w(" FROM ")
	}
	if s.Program {
		g.w("PROGRAM ")
	}
	g.expr(s.Target)
	if s.Delimiters != nil {
		g.w(" USING DELIMITERS ")
		g.literal(s.Delimiters)
	}
	if len(s.ParenOpts) > 0 {
		g.w(" WITH ")
		g.options(s.ParenOpts, " ")
	}
	g.kw(s.LegacyWith, "WITH")
	for _, o := range s.LegacyOpts {
		g.w(" ", o.Name)
		if o.Value != nil {
			g.w(" ")
			g.expr(o.Value)
		}
	}
	if s.Where != nil {
		g.w(" WHERE ")
		g.expr(s.Where)
	}
}

func (g *Generator) createFunction(s *ast.CreateFunction) {
	g.w("CREATE")
	g.kw(s.OrReplace, "OR REPLACE")
	if s.Procedure {
		g.w(" PROCEDURE ")
	} else {
		g.w(" FUNCTION ")
	}
	g.name(s.Name)
	g.w("(")
	for i, p := range s.Params {
		if i > 0 {
			g.w(", ")
		}
		if p.Mode != "" {
			g.w(p.Mode, " ")
		}
		if p.Name != nil {
			g.ident(*p.Name)
			g.w(" ")
		}
		g.dataType(p.Type)
		if p.Default != nil {
			g.w(" DEFAULT ")
			g.expr(p.Default)
		}
	}
	g.w(")")
	switch {
	case s.ReturnsTable != nil:
		g.w(" RETURNS TABLE (")
		for i, c := range s.ReturnsTable {
			if i > 0 {
				g.w(", ")
			}
			g.columnDef(c)
		}
		g.w(")")
	case s.Returns != nil:
		g.w(" RETURNS ")
		g.kwInline(s.ReturnsSetOf, "SETOF ")
		g.dataType(s.Returns)
	}
	for _, o := range s.Options {
		g.w(" ", o.Name)
		if len(o.Values) > 0 {
			g.w(" ")
			g.exprList(o.Values)
		}
		for i, t := range o.Types {
			if i > 0 {
				g.w(",")
			}
			g.w(" FOR TYPE ")
			g.dataType(t)
		}
	}
	if s.Return != nil {
		g.w(" RETURN ")
		g.expr(s.Return)
	}
	if s.Atomic {
		g.w(" BEGIN ATOMIC ")
		g.statementBody(s.Body)
		g.w("END")
	}
}

// statementBody writes each statement followed by "; ".
func (g *Generator) statementBody(body []ast.Statement) {
	for _, st := range body {
		g.stmt(st)
		g.w("; ")
	}
}

func (g *Generator) createTrigger(s *ast.CreateTrigger) {
	g.w("CREATE")
	g.kw(s.OrReplace, "OR REPLACE")
	g.kw(s.Temp, "TEMPORARY")
	g.kw(s.Constraint, "CONSTRAINT")
	g.w(" TRIGGER")
	g.kw(s.IfNotExists, "IF NOT EXISTS")
	g.w(" ")
	g.name(s.Name)
	g.kw(s.Timing != "", s.Timing)
	for i, ev := range s.Events {
		if i > 0 {
			g.w(" OR")
		}
		g.w(" ", ev.Kind)
		if len(ev.Columns) > 0 {
			g.w(" OF ")
			g.identList(ev.Columns)
		}
	}
	g.w(" ON ")
	g.name(s.Table)
	if s.From != nil {
		g.w(" FROM ")
		g.name(s.From)
	}
	g.deferrable(s.Deferrable)
	if len(s.Referencing) > 0 {
		g.w(" REFERENCING ", strings.Join(s.Referencing, " "))
	}
	g.kw(s.ForEach != "", "FOR EACH "+s.ForEach)
	if s.When != nil {
		g.w(" WHEN ")
		g.expr(s.When)
	}
	if s.Function != nil {
		g.w(" EXECUTE FUNCTION ")
		g.funcCall(s.Function)
		return
	}
	g.w(" BEGIN ")
	g.statementBody(s.Body)
	g.w("END")
}
