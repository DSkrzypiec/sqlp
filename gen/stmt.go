package gen

import (
	"strings"

	"sqlp/ast"
)

func (g *Generator) stmt(s ast.Statement) {
	switch s := s.(type) {
	case *ast.Query:
		g.query(s)
	case *ast.Insert:
		g.insert(s)
	case *ast.Update:
		g.update(s)
	case *ast.Delete:
		g.delete(s)
	case *ast.Truncate:
		if !g.d.Features.DML.Truncate {
			g.warn("TRUNCATE")
		} else if !g.d.Features.DML.TruncateOptions && (len(s.Tables) > 1 || s.Tables[0].Only || s.Identity != "" || s.Behavior != "") {
			g.warn("TRUNCATE with ONLY, several tables, IDENTITY, CASCADE or RESTRICT")
		}
		g.w("TRUNCATE TABLE ")
		g.relationList(s.Tables)
		g.kw(s.Identity != "", s.Identity)
		g.kw(s.Behavior != "", s.Behavior)
	case *ast.CreateTable:
		if g.d.Features.DDL.GoogleSQL {
			g.googleCreateTable(s)
		} else {
			g.createTable(s)
		}
	case *ast.CreateIndex:
		if s.Kind != "" {
			g.googleCreateIndex(s)
		} else {
			g.createIndex(s)
		}
	case *ast.CreateView:
		if g.d.Features.DDL.GoogleSQL {
			g.googleCreateView(s)
		} else {
			g.createView(s)
		}
	case *ast.Undrop:
		g.w("UNDROP SCHEMA")
		g.kw(s.IfNotExists, "IF NOT EXISTS")
		g.w(" ")
		g.name(s.Name)
	case *ast.CreateSchema:
		if g.d.Features.DDL.GoogleSQL {
			g.googleCreateSchema(s)
			break
		}
		g.w("CREATE SCHEMA")
		g.kw(s.IfNotExists, "IF NOT EXISTS")
		if s.Name != nil {
			g.w(" ")
			g.name(s.Name)
		}
		if s.Authorization != nil {
			g.w(" AUTHORIZATION ")
			g.expr(s.Authorization)
		}
		for _, el := range s.Elements {
			g.w(" ")
			g.stmt(el)
		}
	case *ast.CreateSequence:
		g.w("CREATE")
		g.kw(s.Temp, "TEMPORARY")
		g.unlogged(s.Unlogged)
		g.w(" SEQUENCE")
		g.kw(s.IfNotExists, "IF NOT EXISTS")
		g.w(" ")
		g.name(s.Name)
		g.sequenceOptions(s.Options, " ")
	case *ast.CreateEnum:
		g.w("CREATE TYPE ")
		g.name(s.Name)
		g.w(" AS ENUM (")
		for i, l := range s.Labels {
			if i > 0 {
				g.w(", ")
			}
			g.str(l)
		}
		g.w(")")
	case *ast.CreateExtension:
		g.w("CREATE EXTENSION")
		g.kw(s.IfNotExists, "IF NOT EXISTS")
		g.w(" ")
		g.ident(s.Name)
		for _, o := range s.Options {
			switch {
			case o.Schema != nil:
				g.w(" SCHEMA ")
				g.ident(*o.Schema)
			case o.Cascade:
				g.w(" CASCADE")
			default:
				g.w(" VERSION ", o.Version)
			}
		}
	case *ast.Drop:
		g.w("DROP ", s.Kind)
		g.kw(s.Concurrently, "CONCURRENTLY")
		g.kw(s.IfExists, "IF EXISTS")
		if len(s.Names) > 0 || len(s.Types) > 0 {
			g.w(" ")
		}
		g.nameList(s.Names)
		for i, t := range s.Types {
			if i > 0 {
				g.w(", ")
			}
			g.dataType(t)
		}
		if s.Params != nil {
			g.w("(")
			for i, t := range s.Params {
				if i > 0 {
					g.w(", ")
				}
				g.dataType(t)
			}
			g.w(")")
		}
		if s.On != nil {
			g.w(" ON ")
			g.name(s.On)
		}
		g.kw(s.Behavior != "", s.Behavior)
	case *ast.AlterTable:
		if g.d.Features.DDL.GoogleSQL {
			g.googleAlter(s)
		} else {
			g.alterTable(s)
		}
	case *ast.Begin:
		g.w("BEGIN")
		if g.d.Features.Utility.Scripting { // BEGIN alone may start a block
			g.w(" TRANSACTION")
		}
		if len(s.Modes) > 0 {
			g.w(" ", strings.Join(s.Modes, ", "))
		}
	case *ast.Commit:
		g.w("COMMIT")
		g.kw(s.Chain, "AND CHAIN")
	case *ast.Rollback:
		g.w("ROLLBACK")
		g.kw(s.Chain, "AND CHAIN")
		if s.To != nil {
			g.w(" TO SAVEPOINT ")
			g.ident(*s.To)
		}
	case *ast.Savepoint:
		g.w("SAVEPOINT ")
		g.ident(s.Name)
	case *ast.ReleaseSavepoint:
		g.w("RELEASE SAVEPOINT ")
		g.ident(s.Name)
	case *ast.Explain:
		g.w("EXPLAIN")
		g.kw(s.QueryPlan, "QUERY PLAN")
		if len(s.Options) > 0 {
			g.w(" ")
			g.options(s.Options, " ")
		}
		g.kw(s.Analyze, "ANALYZE")
		g.kw(s.Verbose, "VERBOSE")
		g.w(" ")
		g.stmt(s.Statement)
	case *ast.Set:
		g.w("SET")
		g.kw(s.Scope != "", s.Scope)
		g.w(" ")
		switch {
		case s.TimeZone:
			g.w("TIME ZONE")
		case s.FromCurrent:
			g.name(s.Name)
			g.w(" FROM CURRENT")
			return
		default:
			g.name(s.Name)
			g.w(" TO")
		}
		if s.Default {
			g.w(" DEFAULT")
		} else {
			g.w(" ")
			g.exprList(s.Values)
		}
	case *ast.Show:
		g.w("SHOW ")
		if s.All {
			g.w("ALL")
		} else {
			g.name(s.Name)
		}
	case *ast.Reset:
		g.w("RESET ")
		if s.All {
			g.w("ALL")
		} else {
			g.name(s.Name)
		}
	case *ast.Pragma:
		g.w("PRAGMA ")
		g.name(s.Name)
		switch {
		case s.Call:
			g.w("(")
			g.expr(s.Value)
			g.w(")")
		case s.Value != nil:
			g.w(" = ")
			g.expr(s.Value)
		}
	case *ast.Attach:
		g.w("ATTACH DATABASE ")
		g.expr(s.Expr)
		g.w(" AS ")
		g.expr(s.Schema)
		if s.Key != nil {
			g.w(" KEY ")
			g.expr(s.Key)
		}
	case *ast.Detach:
		g.w("DETACH DATABASE ")
		g.expr(s.Schema)
	case *ast.Vacuum:
		g.w("VACUUM")
		for _, o := range s.Options {
			g.w(" ", o)
		}
		if len(s.Paren) > 0 {
			g.w(" ")
			g.options(s.Paren, " ")
		}
		g.maintTargets(s.Tables)
		if s.Schema != nil {
			g.w(" ")
			g.ident(*s.Schema)
		}
		if s.Into != nil {
			g.w(" INTO ")
			g.expr(s.Into)
		}
	case *ast.Analyze:
		g.w("ANALYZE")
		g.kw(s.Verbose, "VERBOSE")
		if len(s.Paren) > 0 {
			g.w(" ")
			g.options(s.Paren, " ")
		}
		g.maintTargets(s.Tables)
	case *ast.Reindex:
		g.w("REINDEX")
		if len(s.Paren) > 0 {
			g.w(" ")
			g.options(s.Paren, " ")
		}
		g.kw(s.Kind != "", s.Kind)
		g.kw(s.Concurrently, "CONCURRENTLY")
		if s.Name != nil {
			g.w(" ")
			g.name(s.Name)
		}
	default:
		if !g.extendedStmt(s) && !g.scriptStmt(s) && !g.googleStmt(s) {
			g.w("<?stmt>")
		}
	}
}

// --- DML --------------------------------------------------------------------------

func (g *Generator) withPrefix(w *ast.With) {
	if w != nil {
		if !g.d.Features.DML.WithDML {
			g.warn("WITH before INSERT, UPDATE, DELETE or MERGE")
		}
		g.with(w)
		g.w(" ")
	}
}

func (g *Generator) insert(s *ast.Insert) {
	g.withPrefix(s.With)
	switch {
	case s.Replace && g.d.Features.DML.InsertOr:
		g.w("REPLACE")
	case s.Replace:
		g.warn("REPLACE INTO")
		g.w("INSERT")
	default:
		g.w("INSERT")
		g.orAction(s.Or)
	}
	g.w(" INTO ")
	g.name(s.Table)
	if s.Alias != nil {
		if !g.d.Features.DML.InsertAlias {
			g.warn("INSERT INTO t AS alias")
		}
		g.w(" AS ")
		g.ident(*s.Alias)
	}
	if len(s.Columns) > 0 {
		g.w(" (")
		g.exprList(s.Columns)
		g.w(")")
	}
	g.kw(s.Overriding != "", "OVERRIDING "+s.Overriding+" VALUE")
	if s.DefaultValues {
		if !g.d.Features.DML.DefaultValues {
			g.warn("DEFAULT VALUES")
		}
		g.w(" DEFAULT VALUES")
	} else {
		g.w(" ")
		g.query(s.Source)
	}
	for _, oc := range s.OnConflict {
		g.onConflict(oc)
	}
	g.returning(s.Returning)
}

// relationList writes [ONLY] name, ... for TRUNCATE and LOCK.
func (g *Generator) relationList(refs []*ast.TableRef) {
	for i, r := range refs {
		if i > 0 {
			g.w(", ")
		}
		g.kwInline(r.Only, "ONLY ")
		g.name(r.Name)
	}
}

// orAction writes SQLite's OR <action>, or warns when it must be dropped.
func (g *Generator) orAction(action string) {
	if action == "" {
		return
	}
	if !g.d.Features.DML.InsertOr {
		g.warn("OR " + action)
		return
	}
	g.w(" OR ", action)
}

func (g *Generator) onConflict(oc *ast.OnConflict) {
	g.w(" ON CONFLICT")
	if len(oc.Target) > 0 {
		g.w(" (")
		g.indexElems(oc.Target)
		g.w(")")
		if oc.TargetWhere != nil {
			g.w(" WHERE ")
			g.expr(oc.TargetWhere)
		}
	}
	if oc.Constraint != nil {
		g.w(" ON CONSTRAINT ")
		g.ident(*oc.Constraint)
	}
	if oc.DoNothing {
		g.w(" DO NOTHING")
		return
	}
	g.w(" DO UPDATE SET ")
	g.assignments(oc.Updates)
	if oc.Where != nil {
		g.w(" WHERE ")
		g.expr(oc.Where)
	}
}

func (g *Generator) assignments(as []*ast.Assignment) {
	for i, a := range as {
		if i > 0 {
			g.w(", ")
		}
		if a.Paren {
			if !g.d.Features.DML.SetTuples {
				g.warn("SET (a, b) = ...")
			}
			g.w("(")
			g.exprList(a.Targets)
			g.w(")")
		} else {
			g.expr(a.Targets[0])
		}
		g.w(" = ")
		g.expr(a.Value)
	}
}

func (g *Generator) returning(items []*ast.SelectItem) {
	if len(items) == 0 || !g.d.Features.DML.Returning {
		return
	}
	g.w(" RETURNING ")
	g.selectItems(items)
}

func (g *Generator) update(s *ast.Update) {
	g.withPrefix(s.With)
	g.w("UPDATE")
	g.orAction(s.Or)
	g.kw(s.Only, "ONLY")
	g.w(" ")
	g.name(s.Table)
	if s.Alias != nil {
		g.w(" AS ")
		g.ident(*s.Alias)
	}
	g.indexed(s.Indexed)
	g.w(" SET ")
	g.assignments(s.Set)
	if len(s.From) > 0 {
		g.w(" FROM ")
		g.tableExprs(s.From)
	}
	if s.Where != nil {
		g.w(" WHERE ")
		g.expr(s.Where)
	}
	g.returning(s.Returning)
	if len(s.OrderBy) > 0 {
		g.w(" ORDER BY ")
		g.orderItems(s.OrderBy)
	}
	g.limit(s.Limit)
}

func (g *Generator) delete(s *ast.Delete) {
	g.withPrefix(s.With)
	g.w("DELETE FROM")
	g.kw(s.Only, "ONLY")
	g.w(" ")
	g.name(s.Table)
	if s.Alias != nil {
		g.w(" AS ")
		g.ident(*s.Alias)
	}
	g.indexed(s.Indexed)
	if len(s.Using) > 0 {
		g.w(" USING ")
		g.tableExprs(s.Using)
	}
	if s.Where != nil {
		g.w(" WHERE ")
		g.expr(s.Where)
	}
	g.returning(s.Returning)
	if len(s.OrderBy) > 0 {
		g.w(" ORDER BY ")
		g.orderItems(s.OrderBy)
	}
	g.limit(s.Limit)
}

// --- DDL ------------------------------------------------------------------------------

// unlogged writes UNLOGGED, or warns when the target has no such concept.
func (g *Generator) unlogged(on bool) {
	if on && !g.d.Features.DDL.ExtendedDDL {
		g.warn("UNLOGGED")
		return
	}
	g.kw(on, "UNLOGGED")
}

func (g *Generator) createTable(s *ast.CreateTable) {
	g.w("CREATE")
	g.kw(s.Temp, "TEMPORARY")
	g.unlogged(s.Unlogged)
	g.w(" TABLE")
	g.kw(s.IfNotExists, "IF NOT EXISTS")
	g.w(" ")
	g.name(s.Name)
	if s.PartitionOf != nil {
		g.w(" PARTITION OF ")
		g.name(s.PartitionOf)
	}
	if s.OfType != nil {
		g.w(" OF ")
		g.name(s.OfType)
	}
	if s.HasColumns {
		g.tableElements(s)
	}
	if s.Bound != nil {
		g.partitionBound(s.Bound)
	}
	if len(s.Inherits) > 0 {
		g.w(" INHERITS (")
		g.nameList(s.Inherits)
		g.w(")")
	}
	if s.PartitionBy != nil {
		g.w(" PARTITION BY ", s.PartitionBy.Strategy, " (")
		g.indexElems(s.PartitionBy.Elems)
		g.w(")")
	}
	g.kw(s.Using != "", "USING "+s.Using)
	if len(s.With) > 0 {
		g.w(" WITH ")
		g.options(s.With, " = ")
	}
	g.kw(s.OnCommit != "", "ON COMMIT "+s.OnCommit)
	g.kw(s.Tablespace != "", "TABLESPACE "+s.Tablespace)
	g.tableOptions(s)
	if s.AsQuery != nil {
		g.w(" AS ")
		g.query(s.AsQuery)
		g.withData(s.WithData)
	}
	if s.AsExecute != nil {
		g.w(" AS ")
		g.stmt(s.AsExecute)
		g.withData(s.WithData)
	}
}

func (g *Generator) tableElements(s *ast.CreateTable) {
	g.w(" (")
	first := true
	sep := func() {
		if !first {
			g.w(", ")
		}
		first = false
	}
	nextLike := 0
	likesAt := func(pos int) {
		for nextLike < len(s.Likes) && s.Likes[nextLike].Position == pos {
			sep()
			if g.transpiling() && !g.d.Features.DDL.ExtendedDDL {
				g.warn("LIKE in CREATE TABLE") // SQLite reads it as a column named LIKE
			}
			g.w("LIKE ")
			g.name(s.Likes[nextLike].Source)
			for _, o := range s.Likes[nextLike].Options {
				g.w(" ", o)
			}
			nextLike++
		}
	}
	for i, c := range s.Columns {
		likesAt(i)
		sep()
		g.columnDef(c)
	}
	likesAt(len(s.Columns))
	for _, c := range s.Constraints {
		sep()
		g.tableConstraint(c)
	}
	g.w(")")
}

func (g *Generator) tableOptions(s *ast.CreateTable) {
	if !g.d.Features.DDL.WithoutRowid {
		if s.WithoutRowid {
			g.warn("WITHOUT ROWID")
		}
		if s.Strict {
			g.warn("STRICT")
		}
		return
	}
	g.kw(s.WithoutRowid, "WITHOUT ROWID")
	if s.Strict {
		if s.WithoutRowid {
			g.w(",")
		}
		g.w(" STRICT")
	}
}

func (g *Generator) partitionBound(b *ast.PartitionBound) {
	switch {
	case b.Default:
		g.w(" DEFAULT")
	case b.In != nil:
		g.w(" FOR VALUES IN (")
		g.exprList(b.In)
		g.w(")")
	case b.From != nil:
		g.w(" FOR VALUES FROM (")
		g.exprList(b.From)
		g.w(") TO (")
		g.exprList(b.To)
		g.w(")")
	default:
		g.w(" FOR VALUES WITH (MODULUS ")
		g.expr(b.Modulus)
		g.w(", REMAINDER ")
		g.expr(b.Remainder)
		g.w(")")
	}
}

// options writes (name<sep>value, ...).
func (g *Generator) indexWith(opts []ast.Option) {
	if len(opts) > 0 {
		g.w(" WITH ")
		g.options(opts, " = ")
	}
}

func (g *Generator) options(opts []ast.Option, sep string) {
	g.w("(")
	for i, o := range opts {
		if i > 0 {
			g.w(", ")
		}
		g.w(o.Name)
		if o.Value != nil {
			g.w(sep)
			g.expr(o.Value)
		}
	}
	g.w(")")
}

func (g *Generator) maintTargets(ts []*ast.MaintTarget) {
	for i, t := range ts {
		if i > 0 {
			g.w(",")
		}
		g.w(" ")
		g.name(t.Name)
		if len(t.Columns) > 0 {
			g.w(" (")
			g.identList(t.Columns)
			g.w(")")
		}
	}
}

func (g *Generator) withData(b *bool) {
	switch {
	case b == nil:
	case *b:
		g.w(" WITH DATA")
	default:
		g.w(" WITH NO DATA")
	}
}

func (g *Generator) columnDef(c *ast.ColumnDef) {
	g.ident(c.Name)
	if c.Type != nil {
		g.w(" ")
		g.dataType(c.Type)
	}
	g.kw(c.Storage != "", "STORAGE "+c.Storage)
	g.kw(c.Compression != "", "COMPRESSION "+c.Compression)
	if len(c.Options) > 0 {
		g.w(" OPTIONS ")
		g.options(c.Options, " ")
	}
	for _, cc := range c.Constraints {
		g.w(" ")
		g.columnConstraint(cc)
	}
}

func (g *Generator) columnConstraint(cc *ast.ColumnConstraint) {
	if cc.Name != nil {
		g.w("CONSTRAINT ")
		g.ident(*cc.Name)
		if cc.Kind != ast.AttributeConstraint || cc.Deferrable != nil {
			g.w(" ")
		}
	}
	switch cc.Kind {
	case ast.NotNullConstraint:
		g.w("NOT NULL")
	case ast.NullConstraint:
		g.w("NULL")
	case ast.PrimaryKeyConstraint:
		g.w("PRIMARY KEY")
		g.kw(cc.Direction != "", cc.Direction)
		g.indexWith(cc.IndexWith)
		g.kw(cc.IndexTablespace != "", "USING INDEX TABLESPACE "+cc.IndexTablespace)
	case ast.UniqueConstraint:
		g.w("UNIQUE")
		g.kw(cc.NullsNotDist, "NULLS NOT DISTINCT")
		g.indexWith(cc.IndexWith)
		g.kw(cc.IndexTablespace != "", "USING INDEX TABLESPACE "+cc.IndexTablespace)
	case ast.CheckConstraint:
		g.w("CHECK (")
		g.expr(cc.Expr)
		g.w(")")
		g.kw(cc.NoInherit, "NO INHERIT")
	case ast.DefaultConstraint:
		g.w("DEFAULT ")
		g.expr(cc.Expr)
	case ast.ReferencesConstraint:
		g.references(cc.References)
	case ast.GeneratedConstraint:
		g.w("GENERATED ALWAYS AS (")
		g.expr(cc.Expr)
		g.w(")")
		stored := cc.Generated
		if g.from != nil && g.d.Features.DDL.StoredGeneratedOnly && stored != "STORED" {
			g.warn("generated column that is not STORED")
			stored = "STORED"
		}
		g.kw(stored != "", stored)
	case ast.IdentityConstraint:
		g.w("GENERATED ", cc.Generated, " AS IDENTITY")
		if len(cc.Identity) > 0 {
			g.w(" (")
			g.sequenceOptions(cc.Identity, "")
			g.w(")")
		}
	case ast.CollateConstraint:
		g.w("COLLATE ")
		g.collation(cc.Collation)
	}
	g.conflictClause(cc.OnConflict)
	if cc.Autoincrement {
		if g.d.Features.DDL.Autoincrement {
			g.w(" AUTOINCREMENT")
		} else {
			g.warn("AUTOINCREMENT")
		}
	}
	g.deferrable(cc.Deferrable)
}

// conflictClause writes a constraint's ON CONFLICT, or warns when dropped.
func (g *Generator) conflictClause(action string) {
	if action == "" {
		return
	}
	if !g.d.Features.DDL.ConflictClause {
		g.warn("ON CONFLICT " + action)
		return
	}
	g.w(" ON CONFLICT ", action)
}

func (g *Generator) references(r *ast.References) {
	g.w("REFERENCES ")
	g.name(r.Table)
	if len(r.Columns) > 0 {
		g.w(" (")
		g.identList(r.Columns)
		g.w(")")
	}
	switch m := strings.ToUpper(r.Match); {
	case r.Match == "":
	case !g.d.Features.DDL.LooseReferences && m != "FULL" && m != "SIMPLE":
		g.warn("MATCH " + r.Match)
	default:
		g.w(" MATCH ", r.Match)
	}
	g.kw(r.OnDelete != "", "ON DELETE "+r.OnDelete)
	g.kw(r.OnUpdate != "", "ON UPDATE "+r.OnUpdate)
	if r.OnInsert != "" {
		if g.d.Features.DDL.LooseReferences {
			g.w(" ON INSERT ", r.OnInsert)
		} else {
			g.warn("ON INSERT " + r.OnInsert)
		}
	}
}

func (g *Generator) deferrable(d *ast.Deferrable) {
	if d == nil {
		return
	}
	// A lone constraint attribute is printed right after the separator space.
	if g.sb.Len() > 0 && !strings.HasSuffix(g.sb.String(), " ") {
		g.w(" ")
	}
	word, first := d.Deferrable, d.InitiallyFirst
	if !g.d.Features.DDL.ExtendedDDL && d.Initially != "" {
		// SQLite needs [NOT] DEFERRABLE first. In PostgreSQL INITIALLY
		// DEFERRED implies DEFERRABLE, and INITIALLY IMMEDIATE alone is the
		// default, not deferrable.
		first = false
		if word == "" && d.Initially == "DEFERRED" {
			word = "DEFERRABLE"
		} else if word == "" {
			word = "NOT DEFERRABLE"
		}
	}
	initially := ""
	if d.Initially != "" {
		initially = "INITIALLY " + d.Initially
	}
	parts := []string{word, initially}
	if first {
		parts = []string{initially, word}
	}
	g.w(strings.TrimSpace(strings.Join(parts, " ")))
}

func (g *Generator) tableConstraint(tc *ast.TableConstraint) {
	if tc.Name != nil {
		g.w("CONSTRAINT ")
		g.ident(*tc.Name)
		if tc.Kind != ast.TableNameOnly {
			g.w(" ")
		}
	}
	switch tc.Kind {
	case ast.TableNameOnly:
	case ast.TablePrimaryKey:
		g.w("PRIMARY KEY")
		g.keyColumns(tc)
	case ast.TableUnique:
		g.w("UNIQUE")
		g.kw(tc.NullsNotDist, "NULLS NOT DISTINCT")
		g.keyColumns(tc)
	case ast.TableExclude:
		g.w("EXCLUDE")
		g.kw(tc.Method != "", "USING "+tc.Method)
		g.w(" (")
		for i, ex := range tc.Excludes {
			if i > 0 {
				g.w(", ")
			}
			g.indexElems([]*ast.IndexElem{ex.Elem})
			g.w(" WITH ", ex.Op)
		}
		g.w(")")
	case ast.TableCheck:
		g.w("CHECK (")
		g.expr(tc.Expr)
		g.w(")")
	case ast.TableForeignKey:
		g.w("FOREIGN KEY (")
		g.indexElems(tc.Columns)
		g.w(") ")
		g.references(tc.References)
	}
	if len(tc.Include) > 0 {
		g.w(" INCLUDE (")
		g.identList(tc.Include)
		g.w(")")
	}
	g.indexWith(tc.IndexWith)
	g.kw(tc.IndexTablespace != "", "USING INDEX TABLESPACE "+tc.IndexTablespace)
	if tc.Where != nil {
		g.w(" WHERE (")
		g.expr(tc.Where)
		g.w(")")
	}
	g.conflictClause(tc.OnConflict)
	if tc.Deferrable != nil && tc.Kind != ast.TableForeignKey && !g.d.Features.DDL.ExtendedDDL {
		g.warn("DEFERRABLE or INITIALLY on a table constraint other than FOREIGN KEY")
	} else {
		g.deferrable(tc.Deferrable)
	}
	g.kw(tc.NoInherit, "NO INHERIT")
	g.kw(tc.NotValid, "NOT VALID")
}

// keyColumns writes the column list, or USING INDEX, of a key constraint.
func (g *Generator) keyColumns(tc *ast.TableConstraint) {
	if tc.UsingIndex != "" {
		g.w(" USING INDEX ", tc.UsingIndex)
		return
	}
	g.w(" (")
	g.indexElems(tc.Columns)
	g.kw(tc.Autoincrement, "AUTOINCREMENT")
	g.w(")")
}

func (g *Generator) indexElems(elems []*ast.IndexElem) {
	for i, e := range elems {
		if i > 0 {
			g.w(", ")
		}
		g.expr(e.Expr)
		if e.Collation != nil {
			g.w(" COLLATE ")
			g.collation(e.Collation)
		}
		if e.Opclass != nil {
			g.w(" ")
			g.name(e.Opclass)
			if len(e.OpclassOptions) > 0 {
				g.options(e.OpclassOptions, " = ")
			}
		}
		g.kw(e.Direction != "", e.Direction)
		g.kw(e.Nulls != "", "NULLS "+e.Nulls)
	}
}

func (g *Generator) createIndex(s *ast.CreateIndex) {
	g.w("CREATE")
	g.kw(s.Unique, "UNIQUE")
	g.w(" INDEX")
	g.kw(s.Concurrently, "CONCURRENTLY")
	g.kw(s.IfNotExists, "IF NOT EXISTS")
	if s.Name != nil {
		g.w(" ")
		g.name(s.Name)
	}
	g.w(" ON")
	g.kw(s.Only, "ONLY")
	g.w(" ")
	g.name(s.Table)
	g.kw(s.Method != "", "USING "+s.Method)
	g.w(" (")
	g.indexElems(s.Columns)
	g.w(")")
	if len(s.Include) > 0 {
		g.w(" INCLUDE (")
		g.identList(s.Include)
		g.w(")")
	}
	g.kw(s.NullsNotDist, "NULLS NOT DISTINCT")
	if len(s.With) > 0 {
		g.w(" WITH ")
		g.options(s.With, " = ")
	}
	g.kw(s.Tablespace != "", "TABLESPACE "+s.Tablespace)
	if s.Where != nil {
		g.w(" WHERE ")
		g.expr(s.Where)
	}
}

func (g *Generator) createView(s *ast.CreateView) {
	g.w("CREATE")
	g.kw(s.OrReplace, "OR REPLACE")
	g.kw(s.Temp, "TEMPORARY")
	g.unlogged(s.Unlogged)
	g.kw(s.Materialized, "MATERIALIZED")
	g.w(" VIEW")
	g.kw(s.IfNotExists, "IF NOT EXISTS")
	g.w(" ")
	g.name(s.Name)
	if len(s.Columns) > 0 {
		g.w(" (")
		g.identList(s.Columns)
		g.w(")")
	}
	g.kw(s.Using != "", "USING "+s.Using)
	if len(s.With) > 0 {
		g.w(" WITH ")
		g.options(s.With, " = ")
	}
	g.kw(s.Tablespace != "", "TABLESPACE "+s.Tablespace)
	g.w(" AS ")
	g.query(s.Query)
	switch s.CheckOption {
	case "CHECK":
		g.w(" WITH CHECK OPTION")
	case "CASCADED", "LOCAL":
		g.w(" WITH ", s.CheckOption, " CHECK OPTION")
	}
	g.withData(s.WithData)
}

func (g *Generator) alterTable(s *ast.AlterTable) {
	g.w("ALTER TABLE")
	g.kw(s.IfExists, "IF EXISTS")
	g.kw(s.Only, "ONLY")
	g.w(" ")
	g.name(s.Name)
	for i, a := range s.Actions {
		if i > 0 {
			g.w(",")
		}
		g.w(" ")
		g.alterAction(a)
	}
}

func (g *Generator) alterAction(a ast.AlterAction) {
	switch a := a.(type) {
	case *ast.AddColumn:
		g.w("ADD COLUMN")
		g.kw(a.IfNotExists, "IF NOT EXISTS")
		g.w(" ")
		g.columnDef(a.Column)
	case *ast.DropColumn:
		g.w("DROP COLUMN")
		g.kw(a.IfExists, "IF EXISTS")
		g.w(" ")
		g.ident(a.Name)
		g.kw(a.Behavior != "", a.Behavior)
	case *ast.RenameColumn:
		g.w("RENAME COLUMN ")
		g.ident(a.Old)
		g.w(" TO ")
		g.ident(a.New)
	case *ast.RenameTable:
		g.w("RENAME TO ")
		g.ident(a.New)
	case *ast.RenameConstraint:
		g.w("RENAME CONSTRAINT ")
		g.ident(a.Old)
		g.w(" TO ")
		g.ident(a.New)
	case *ast.AlterColumn:
		g.w("ALTER COLUMN ")
		g.ident(a.Name)
		switch a.Kind {
		case ast.SetDefault:
			g.w(" SET DEFAULT ")
			g.expr(a.Expr)
		case ast.DropDefault:
			g.w(" DROP DEFAULT")
		case ast.SetNotNull:
			g.w(" SET NOT NULL")
		case ast.DropNotNull:
			g.w(" DROP NOT NULL")
		case ast.AddIdentity:
			g.w(" ADD GENERATED ", a.Generated, " AS IDENTITY")
			if len(a.Identity) > 0 {
				g.w(" (")
				g.sequenceOptions(a.Identity, "")
				g.w(")")
			}
		case ast.SetType:
			g.w(" SET DATA TYPE ")
			g.dataType(a.Type)
			if a.Collation != nil {
				g.w(" COLLATE ")
				g.collation(a.Collation)
			}
			if a.Expr != nil {
				g.w(" USING ")
				g.expr(a.Expr)
			}
		}
	case *ast.AddConstraint:
		g.w("ADD ")
		g.tableConstraint(a.Constraint)
	case *ast.DropConstraint:
		g.w("DROP CONSTRAINT")
		g.kw(a.IfExists, "IF EXISTS")
		g.w(" ")
		g.ident(a.Name)
		g.kw(a.Behavior != "", a.Behavior)
	case *ast.SetSchema:
		g.w("SET SCHEMA ")
		g.ident(a.Schema)
	case *ast.AlterOpaque:
		g.w(a.Text)
	}
}
