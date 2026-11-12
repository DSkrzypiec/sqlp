package gen

import (
	"strings"

	"sqlp/ast"
)

// BigQuery's DDL, for a target dialect with DDLFeatures.GoogleSQL.

func (g *Generator) googleCreateTable(s *ast.CreateTable) {
	if s.Unlogged || s.PartitionOf != nil || s.OfType != nil || len(s.Inherits) > 0 || s.PartitionBy != nil ||
		s.Using != "" || len(s.With) > 0 || s.OnCommit != "" || s.Tablespace != "" || len(s.Likes) > 0 ||
		s.AsExecute != nil || s.WithData != nil || s.WithoutRowid || s.Strict {
		g.warn("this form of CREATE TABLE")
	}
	g.w("CREATE")
	g.kw(s.OrReplace, "OR REPLACE")
	g.kw(s.Temp, "TEMP")
	g.kw(s.Kind != "", s.Kind)
	g.w(" TABLE")
	g.kw(s.IfNotExists, "IF NOT EXISTS")
	g.w(" ")
	g.name(s.Name)
	if s.HasColumns {
		g.w(" (")
		for i, c := range s.Columns {
			if i > 0 {
				g.w(", ")
			}
			g.googleColumn(c)
		}
		for _, tc := range s.Constraints {
			g.w(", ")
			g.googleKey(tc)
		}
		g.w(")")
	}
	if src := s.Source; src != nil {
		g.w(" ", src.Kind, " ")
		g.name(src.Name)
		if src.SystemTime != nil {
			g.w(" FOR SYSTEM_TIME AS OF ")
			g.expr(src.SystemTime)
		}
	}
	if s.DefaultCollate != "" {
		g.w(" DEFAULT COLLATE ")
		g.str(s.DefaultCollate)
	}
	if s.PartitionExpr != nil {
		g.w(" PARTITION BY ")
		g.expr(s.PartitionExpr)
	}
	if len(s.ClusterBy) > 0 {
		g.w(" CLUSTER BY ")
		g.exprList(s.ClusterBy)
	}
	if s.HasPartitionColumns {
		g.w(" WITH PARTITION COLUMNS")
		if len(s.PartitionColumns) > 0 {
			g.w(" (")
			for i, c := range s.PartitionColumns {
				if i > 0 {
					g.w(", ")
				}
				g.ident(c.Name)
				g.w(" ")
				g.dataType(c.Type)
			}
			g.w(")")
		}
	}
	switch {
	case s.ConnectionDefault:
		g.w(" WITH CONNECTION DEFAULT")
	case s.Connection != nil:
		g.w(" WITH CONNECTION ")
		g.name(s.Connection)
	}
	g.googleOptions(s.Options)
	if s.AsQuery != nil {
		g.w(" AS ")
		g.query(s.AsQuery)
	}
}

// googleColumn writes a column: its type, then a default, generated value or
// identity, then keys and NOT NULL, then OPTIONS.
func (g *Generator) googleColumn(c *ast.ColumnDef) {
	g.ident(c.Name)
	if c.Type == nil {
		g.warn("a column without a type")
	} else {
		g.w(" ")
		g.schemaType(c.Type)
	}
	if c.Storage != "" || c.Compression != "" {
		g.warn("STORAGE or COMPRESSION")
	}
	for _, cc := range c.Constraints {
		switch cc.Kind {
		case ast.DefaultConstraint:
			g.w(" DEFAULT ")
			g.expr(cc.Expr)
		case ast.GeneratedConstraint:
			g.w(" GENERATED ALWAYS AS (")
			g.expr(cc.Expr)
			g.w(")")
			if cc.Generated == "STORED" {
				g.w(" STORED")
				g.googleOptions(cc.Options)
			}
		case ast.IdentityConstraint:
			if cc.Generated != "" {
				g.w(" GENERATED ", cc.Generated)
			}
			g.w(" AS IDENTITY (")
			for i, o := range cc.Identity {
				if i > 0 {
					g.w(" ")
				}
				g.w(o.Name, " ")
				g.expr(o.Value)
			}
			g.w(")")
		}
	}
	for _, cc := range c.Constraints {
		switch cc.Kind {
		case ast.DefaultConstraint, ast.GeneratedConstraint, ast.IdentityConstraint:
		case ast.PrimaryKeyConstraint:
			g.w(" PRIMARY KEY")
			g.notEnforced(cc.NotEnforced)
		case ast.ReferencesConstraint:
			if cc.Name != nil {
				g.w(" CONSTRAINT ")
				g.ident(*cc.Name)
			}
			g.w(" REFERENCES ")
			g.googleReferences(cc.References)
			g.notEnforced(cc.NotEnforced)
		case ast.NotNullConstraint:
			g.w(" NOT NULL")
		default:
			g.warn("this column constraint")
		}
	}
	g.googleOptions(c.Options)
}

// googleKey writes a table's PRIMARY KEY or FOREIGN KEY.
func (g *Generator) googleKey(tc *ast.TableConstraint) {
	switch tc.Kind {
	case ast.TablePrimaryKey:
		g.w("PRIMARY KEY (")
		g.indexElems(tc.Columns)
		g.w(")")
	case ast.TableForeignKey:
		if tc.Name != nil {
			g.w("CONSTRAINT ")
			g.ident(*tc.Name)
			g.w(" ")
		}
		g.w("FOREIGN KEY (")
		g.indexElems(tc.Columns)
		g.w(") REFERENCES ")
		g.googleReferences(tc.References)
	default:
		g.warn("this table constraint")
		g.tableConstraint(tc)
		return
	}
	g.notEnforced(tc.NotEnforced)
	g.googleOptions(tc.Options)
}

func (g *Generator) googleReferences(r *ast.References) {
	if r.Match != "" || r.OnDelete != "" || r.OnUpdate != "" || r.OnInsert != "" {
		g.warn("MATCH, ON DELETE or ON UPDATE")
	}
	g.name(r.Table)
	g.w("(")
	g.identList(r.Columns)
	g.w(")")
}

// notEnforced writes NOT ENFORCED, the only kind of key BigQuery has.
func (g *Generator) notEnforced(was bool) {
	if !was {
		g.warn("an enforced key")
	}
	g.w(" NOT ENFORCED")
}

// schemaType writes the type of a column, field or element, with COLLATE,
// NOT NULL and OPTIONS (...).
func (g *Generator) schemaType(dt *ast.DataType) {
	switch {
	case dt.Angle && strings.EqualFold(dt.Name, "ARRAY") && dt.Of != nil:
		g.w(dt.Name, "<")
		g.schemaType(dt.Of)
		g.w(">")
	case dt.Angle && strings.EqualFold(dt.Name, "STRUCT"):
		g.w(dt.Name, "<")
		for i, f := range dt.Struct {
			if i > 0 {
				g.w(", ")
			}
			if f.Name.Name != "" {
				g.ident(f.Name)
				g.w(" ")
			}
			g.schemaType(f.Type)
		}
		g.w(">")
	default:
		g.dataType(dt)
	}
	if dt.Collate != "" {
		g.w(" COLLATE ")
		g.str(dt.Collate)
	}
	g.kw(dt.NotNull, "NOT NULL")
	g.googleOptions(dt.Options)
}

// googleOptions writes OPTIONS (name = value, ...) when opts is not nil.
func (g *Generator) googleOptions(opts []ast.Option) {
	if opts == nil {
		return
	}
	g.w(" OPTIONS (")
	for i, o := range opts {
		if i > 0 {
			g.w(", ")
		}
		assign := o.Assign
		if assign == "" {
			assign = "="
		}
		g.w(o.Name, " ", assign, " ")
		g.expr(o.Value)
	}
	g.w(")")
}

func (g *Generator) googleCreateView(s *ast.CreateView) {
	if s.Temp || s.Unlogged || s.Using != "" || len(s.With) > 0 || s.Tablespace != "" || s.CheckOption != "" || s.WithData != nil {
		g.warn("this form of CREATE VIEW")
	}
	g.w("CREATE")
	g.kw(s.OrReplace, "OR REPLACE")
	g.kw(s.Materialized, "MATERIALIZED")
	g.w(" VIEW")
	g.kw(s.IfNotExists, "IF NOT EXISTS")
	g.w(" ")
	g.name(s.Name)
	if len(s.Columns) > 0 {
		g.w(" (")
		for i, c := range s.Columns {
			if i > 0 {
				g.w(", ")
			}
			g.ident(c)
			if i < len(s.ColumnOptions) {
				g.googleOptions(s.ColumnOptions[i])
			}
		}
		g.w(")")
	}
	g.kw(s.SQLSecurity != "", "SQL SECURITY "+s.SQLSecurity)
	if s.PartitionExpr != nil {
		g.w(" PARTITION BY ")
		g.expr(s.PartitionExpr)
	}
	if len(s.ClusterBy) > 0 {
		g.w(" CLUSTER BY ")
		g.exprList(s.ClusterBy)
	}
	g.googleOptions(s.Options)
	if s.ReplicaOf != nil {
		g.w(" AS REPLICA OF ")
		g.name(s.ReplicaOf)
		return
	}
	g.w(" AS ")
	g.query(s.Query)
}

func (g *Generator) googleCreateSchema(s *ast.CreateSchema) {
	if s.Authorization != nil || len(s.Elements) > 0 {
		g.warn("AUTHORIZATION or schema elements in CREATE SCHEMA")
	}
	g.w("CREATE")
	g.kw(s.External, "EXTERNAL")
	g.w(" SCHEMA")
	g.kw(s.IfNotExists, "IF NOT EXISTS")
	if s.Name != nil {
		g.w(" ")
		g.name(s.Name)
	}
	if s.DefaultCollate != "" {
		g.w(" DEFAULT COLLATE ")
		g.str(s.DefaultCollate)
	}
	if s.Connection != nil {
		g.w(" WITH CONNECTION ")
		g.name(s.Connection)
	}
	g.googleOptions(s.Options)
}
