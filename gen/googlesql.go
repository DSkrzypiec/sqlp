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
	g.w(" OPTIONS ")
	g.optionBody(opts)
}

// optionBody writes (name = value, ...).
func (g *Generator) optionBody(opts []ast.Option) {
	g.w("(")
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

// googleAlter writes ALTER of a table or of another of BigQuery's objects.
func (g *Generator) googleAlter(s *ast.AlterTable) {
	kind := s.Kind
	if kind == "" {
		kind = "TABLE"
	}
	if s.Only { // BigQuery has no ONLY
		g.warn("ALTER TABLE ONLY")
	}
	g.w("ALTER ", kind)
	g.kw(s.IfExists, "IF EXISTS")
	if s.Name != nil {
		g.w(" ")
		g.name(s.Name)
	}
	if s.On != nil {
		g.w(" ON ")
		g.name(s.On)
	}
	for i, a := range s.Actions {
		if i > 0 {
			g.w(",")
		}
		g.w(" ")
		g.googleAlterAction(a)
	}
	if s.Rebuild {
		if len(s.Actions) > 0 {
			g.w(",")
		}
		g.w(" REBUILD")
	}
}

func (g *Generator) googleAlterAction(a ast.AlterAction) {
	switch x := a.(type) {
	case *ast.SetOptions:
		g.w("SET")
		g.googleOptions(x.Options)
	case *ast.SetDefaultCollate:
		g.w("SET DEFAULT COLLATE ")
		g.str(x.Collate)
	case *ast.AddColumn:
		g.w("ADD COLUMN")
		g.kw(x.IfNotExists, "IF NOT EXISTS")
		g.w(" ")
		if x.Column.Type == nil { // a search index's column
			g.ident(x.Column.Name)
			g.googleOptions(x.Column.Options)
		} else {
			g.googleColumn(x.Column)
		}
	case *ast.AddConstraint:
		g.w("ADD ")
		if x.Constraint.Kind == ast.TableForeignKey && x.Constraint.Name != nil {
			g.w("CONSTRAINT")
			g.kw(x.IfNotExists, "IF NOT EXISTS")
			g.w(" ")
			g.ident(*x.Constraint.Name)
			g.w(" ")
			c := *x.Constraint
			c.Name = nil
			g.googleKey(&c)
			return
		}
		g.googleKey(x.Constraint)
	case *ast.AddReplica:
		g.w("ADD REPLICA ")
		g.ident(x.Name)
		g.googleOptions(x.Options)
	case *ast.DropReplica:
		g.w("DROP REPLICA ")
		g.ident(x.Name)
	case *ast.RenameTable:
		g.w("RENAME TO ")
		g.ident(x.New)
	case *ast.RenameColumn:
		g.w("RENAME COLUMN")
		g.kw(x.IfExists, "IF EXISTS")
		g.w(" ")
		g.ident(x.Old)
		g.w(" TO ")
		g.ident(x.New)
	case *ast.DropColumn:
		g.w("DROP COLUMN")
		g.kw(x.IfExists, "IF EXISTS")
		g.w(" ")
		g.ident(x.Name)
	case *ast.DropConstraint:
		g.w("DROP CONSTRAINT")
		g.kw(x.IfExists, "IF EXISTS")
		g.w(" ")
		g.ident(x.Name)
	case *ast.DropPrimaryKey:
		g.w("DROP PRIMARY KEY")
		g.kw(x.IfExists, "IF EXISTS")
	case *ast.AlterColumn:
		g.w("ALTER COLUMN")
		g.kw(x.IfExists, "IF EXISTS")
		g.w(" ")
		g.ident(x.Name)
		switch x.Kind {
		case ast.SetColumnOptions:
			g.w(" SET")
			g.googleOptions(x.Options)
		case ast.DropNotNull:
			g.w(" DROP NOT NULL")
		case ast.SetType:
			g.w(" SET DATA TYPE ")
			g.schemaType(x.Type)
		case ast.SetDefault:
			g.w(" SET DEFAULT ")
			g.expr(x.Expr)
		case ast.DropDefault:
			g.w(" DROP DEFAULT")
		case ast.SetIdentity, ast.AddIdentity:
			g.w(" SET GENERATED")
			g.kw(x.Generated != "", x.Generated)
			g.w(" AS IDENTITY (")
			for i, o := range x.Identity {
				if i > 0 {
					g.w(" ")
				}
				g.w(o.Name, " ")
				g.expr(o.Value)
			}
			g.w(")")
		case ast.DropGenerated:
			g.w(" DROP GENERATED")
		default:
			g.warn("this ALTER COLUMN action")
		}
	default:
		g.warn("this ALTER TABLE action")
	}
}

func (g *Generator) googleCreateFunction(s *ast.CreateFunction) {
	if s.Procedure || s.ReturnsSetOf || len(s.ReturnsTable) > 0 || len(s.Options) > 0 || s.Return != nil || len(s.Body) > 0 {
		g.warn("this form of CREATE FUNCTION")
	}
	g.w("CREATE")
	g.kw(s.OrReplace, "OR REPLACE")
	g.kw(s.Temp, "TEMP")
	g.kw(s.Aggregate, "AGGREGATE")
	g.kw(s.TableFunction, "TABLE")
	g.w(" FUNCTION")
	g.kw(s.IfNotExists, "IF NOT EXISTS")
	g.w(" ")
	g.name(s.Name)
	g.w("(")
	for i, p := range s.Params {
		if i > 0 {
			g.w(", ")
		}
		if p.Mode != "" || p.Default != nil || p.Name == nil {
			g.warn("a parameter mode, default or unnamed parameter")
		}
		if p.Name != nil {
			g.ident(*p.Name)
			g.w(" ")
		}
		g.dataType(p.Type)
		g.kw(p.NotAggregate, "NOT AGGREGATE")
	}
	g.w(")")
	if s.Returns != nil {
		g.w(" RETURNS ")
		g.dataType(s.Returns)
	}
	before := s.OptionsFirst || s.TableFunction || s.Remote
	switch {
	case s.Remote:
		g.w(" REMOTE WITH CONNECTION ")
		g.name(s.Connection)
	case s.Language != "":
		g.kw(s.Determinism != "", s.Determinism)
		g.w(" LANGUAGE ", s.Language)
	}
	if before {
		g.googleOptions(s.OptionList)
	}
	switch {
	case s.AsQuery != nil:
		g.w(" AS ")
		g.query(s.AsQuery)
	case s.Language != "":
		g.w(" AS ")
		g.str(s.Code)
	case !s.Remote:
		g.w(" AS (")
		g.expr(s.As)
		g.w(")")
	}
	if !before {
		g.googleOptions(s.OptionList)
	}
}

func (g *Generator) googleCreateIndex(s *ast.CreateIndex) {
	g.w("CREATE")
	g.kw(s.OrReplace, "OR REPLACE")
	g.w(" ", s.Kind, " INDEX")
	g.kw(s.IfNotExists, "IF NOT EXISTS")
	g.w(" ")
	g.name(s.Name)
	g.w(" ON ")
	g.name(s.Table)
	columns := func(list []*ast.IndexElem) {
		for i, e := range list {
			if i > 0 {
				g.w(", ")
			}
			g.expr(e.Expr)
			g.googleOptions(e.Options)
		}
	}
	g.w(" (")
	if s.AllColumns {
		g.w("ALL COLUMNS")
		if len(s.ColumnOptions) > 0 {
			g.w(" WITH COLUMN OPTIONS (")
			columns(s.ColumnOptions)
			g.w(")")
		}
	} else {
		columns(s.Columns)
	}
	g.w(")")
	if len(s.Storing) > 0 {
		g.w(" STORING (")
		g.identList(s.Storing)
		g.w(")")
	}
	if s.PartitionExpr != nil {
		g.w(" PARTITION BY ")
		g.expr(s.PartitionExpr)
	}
	g.googleOptions(s.Options)
}

func (g *Generator) googleGrant(s *ast.Grant) {
	if s.All || s.IsRole || s.AllIn != "" || s.WithOption != "" || s.GrantedBy != nil || s.OptionFor != "" || s.Behavior != "" {
		g.warn("this form of GRANT or REVOKE")
	}
	g.w(map[bool]string{true: "REVOKE ", false: "GRANT "}[s.Revoke])
	for i, p := range s.Privileges {
		if i > 0 {
			g.w(", ")
		}
		g.ident(p.Name)
	}
	g.w(" ON ", s.ObjectKind, " ")
	g.nameList(s.Objects)
	g.w(map[bool]string{true: " FROM ", false: " TO "}[s.Revoke])
	g.exprList(s.Grantees)
}

func (g *Generator) googleCreateProcedure(s *ast.CreateFunction) {
	if len(s.Options) > 0 || s.Return != nil || len(s.Body) > 0 || s.Returns != nil {
		g.warn("this form of CREATE PROCEDURE")
	}
	g.w("CREATE")
	g.kw(s.OrReplace, "OR REPLACE")
	g.w(" PROCEDURE")
	g.kw(s.IfNotExists, "IF NOT EXISTS")
	g.w(" ")
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
	}
	g.w(")")
	if s.Connection != nil {
		g.kw(s.ExternalSecurity != "", "EXTERNAL SECURITY "+s.ExternalSecurity)
		g.w(" WITH CONNECTION ")
		g.name(s.Connection)
		g.googleOptions(s.OptionList)
		g.w(" LANGUAGE ", s.Language)
		if !s.NoCode {
			g.w(" AS ")
			g.str(s.Code)
		}
		return
	}
	g.googleOptions(s.OptionList)
	g.w(" ")
	if s.Block != nil {
		g.stmt(s.Block)
	}
}

// googleStmt writes BigQuery's ML, export and load statements, reporting
// whether s was one.
func (g *Generator) googleStmt(s ast.Statement) bool {
	switch s.(type) {
	case *ast.CreateModel, *ast.ExportData, *ast.ExportModel, *ast.LoadData:
		if !g.d.Features.DDL.GoogleSQL {
			g.warn("BigQuery's CREATE MODEL, EXPORT and LOAD DATA")
		}
	}
	switch s := s.(type) {
	case *ast.CreateModel:
		g.w("CREATE")
		g.kw(s.OrReplace, "OR REPLACE")
		g.kw(s.Temp, "TEMP")
		g.w(" MODEL")
		g.kw(s.IfNotExists, "IF NOT EXISTS")
		g.w(" ")
		g.name(s.Name)
		if len(s.Transform) > 0 {
			g.w(" TRANSFORM (")
			g.selectItems(s.Transform)
			g.w(")")
		}
		if len(s.Input) > 0 {
			g.w(" INPUT ")
			g.typedColumns(s.Input)
			g.w(" OUTPUT ")
			g.typedColumns(s.Output)
		}
		if s.Remote {
			g.w(" REMOTE")
			switch {
			case s.ConnectionDefault:
				g.w(" WITH CONNECTION DEFAULT")
			case s.Connection != nil:
				g.w(" WITH CONNECTION ")
				g.name(s.Connection)
			}
		}
		g.googleOptions(s.Options)
		switch {
		case s.TrainingData != nil:
			g.w(" AS (training_data AS (")
			g.query(s.TrainingData)
			g.w(")")
			if s.CustomHoliday != nil {
				g.w(", custom_holiday AS (")
				g.query(s.CustomHoliday)
				g.w(")")
			}
			g.w(")")
		case s.AsQuery != nil:
			g.w(" AS ")
			g.query(s.AsQuery)
		}
	case *ast.ExportData:
		g.w("EXPORT DATA")
		if s.Connection != nil {
			g.w(" WITH CONNECTION ")
			g.name(s.Connection)
		}
		g.googleOptions(s.Options)
		g.w(" AS ")
		g.query(s.Query)
	case *ast.ExportModel:
		g.w("EXPORT MODEL ")
		g.name(s.Name)
		g.googleOptions(s.Options)
	case *ast.LoadData:
		g.w("LOAD DATA ", map[bool]string{true: "OVERWRITE", false: "INTO"}[s.Overwrite])
		g.kw(s.Temp, "TEMP TABLE")
		g.w(" ")
		g.name(s.Table)
		if s.Partitions != nil {
			g.kw(s.OverwritePartitions, "OVERWRITE")
			g.w(" PARTITIONS (", s.Partitions.Name, " = ")
			g.expr(s.Partitions.Value)
			g.w(")")
		}
		if s.HasColumns {
			g.w(" (")
			for i, c := range s.Columns {
				if i > 0 {
					g.w(", ")
				}
				g.googleColumn(c)
			}
			g.w(")")
		}
		if s.PartitionExpr != nil {
			g.w(" PARTITION BY ")
			g.expr(s.PartitionExpr)
		}
		if len(s.ClusterBy) > 0 {
			g.w(" CLUSTER BY ")
			g.exprList(s.ClusterBy)
		}
		g.googleOptions(s.Options)
		g.w(" FROM FILES ")
		g.optionBody(s.Files)
		if s.HasPartitionColumns {
			g.w(" WITH PARTITION COLUMNS")
			if len(s.PartitionColumns) > 0 {
				g.w(" ")
				g.typedColumns(s.PartitionColumns)
			}
		}
		if s.Connection != nil {
			g.w(" WITH CONNECTION ")
			g.name(s.Connection)
		}
	default:
		return false
	}
	return true
}

// typedColumns writes (name type, ...).
func (g *Generator) typedColumns(cols []*ast.ColumnDef) {
	g.w("(")
	for i, c := range cols {
		if i > 0 {
			g.w(", ")
		}
		g.ident(c.Name)
		g.w(" ")
		g.dataType(c.Type)
	}
	g.w(")")
}
