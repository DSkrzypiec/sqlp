package analyze

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"sqlp/ast"
	"sqlp/catalog"
	"sqlp/dialect"
	"sqlp/parser"
)

// QStructGraph shows how a query is built from database objects: which CTEs,
// subqueries, tables, views and table functions it reads, and what each of
// them reads in turn. Joins, conditions, set operations and aliases are left
// out; aliases are resolved to what they name.
//
// It is a DAG: an object read in several places is one node with several
// parents. The one exception is a recursive CTE, which lists itself among its
// children.
type QStructGraph struct {
	Root        *QStructNode
	Diagnostics []Diagnostic
}

// QStructNode is one relation in a QStructGraph.
type QStructNode struct {
	// ID is unique in the graph: the name, made unique with a "#2"-style
	// suffix when needed. The root of a SELECT is "query", and a subquery
	// without an alias is "subquery 1", "subquery 2", ...
	ID   string
	Kind QStructKind
	// Name is the object's name: its full name when the catalog has it,
	// otherwise as written, with the dialect's case folding. CTEs and aliased
	// subqueries have just Name.Object.
	Name catalog.Name
	// Status says whether a database object was found in the catalog. Table
	// functions are never checked.
	Status NameStatus
	// Children are the distinct relations this one reads directly, in the
	// order they are first referenced.
	Children []*QStructNode
}

// QStructKind is what a QStructNode stands for.
type QStructKind string

const (
	QStructQuery            QStructKind = "query"    // the root of a SELECT
	QStructCTE              QStructKind = "cte"      // a common table expression
	QStructSubquery         QStructKind = "subquery" // in FROM, or in an expression
	QStructTable            QStructKind = "table"
	QStructView             QStructKind = "view"
	QStructMaterializedView QStructKind = "materialized_view"
	QStructForeignTable     QStructKind = "foreign_table"
	QStructObject           QStructKind = "object" // a relation whose kind is not known
	QStructFunction         QStructKind = "function"
)

// QStruct builds the QStructGraph of a SELECT or CREATE [MATERIALIZED] VIEW
// statement. For a view, the root is the view being created. The error is
// only for an unsupported statement or an invalid catalog; anything the
// analysis cannot see into is reported in Diagnostics.
func QStruct(stmt ast.Statement, d *dialect.Dialect, o Options) (*QStructGraph, error) {
	r, err := newResolver(d, o)
	if err != nil {
		return nil, err
	}
	b := &builder{
		r:         r,
		o:         o,
		finder:    newCTEFinder(r),
		ids:       map[string]bool{},
		suffixes:  map[string]int{},
		edges:     map[[2]*QStructNode]bool{},
		objects:   map[string]*QStructNode{},
		ctes:      map[*ast.CTE]*QStructNode{},
		expanding: map[*QStructNode]bool{},
	}
	var root *QStructNode
	switch s := stmt.(type) {
	case *ast.Query:
		root = b.node("query", QStructQuery)
		b.query(s, root, nil)
	case *ast.CreateView:
		kind := QStructView
		if s.Materialized {
			kind = QStructMaterializedView
		}
		name := b.name(s.Name)
		name = r.created(name, s.Temp)
		root = b.node(name.String(), kind)
		root.Name = name
		b.query(s.Query, root, nil)
	default:
		return nil, fmt.Errorf("analyze: QStruct needs a SELECT or CREATE VIEW statement, not %T", stmt)
	}
	for _, w := range b.withs {
		for _, c := range w.CTEs {
			if b.ctes[c] == nil {
				b.diag("CTE %s is never used", r.ident(c.Name))
			}
		}
	}
	return &QStructGraph{Root: root, Diagnostics: b.diags}, nil
}

// builder holds the state of one QStruct call.
type builder struct {
	r         *resolver
	o         Options
	finder    *cteFinder
	ids       map[string]bool
	suffixes  map[string]int           // the next "#n" to try for an ID that is taken
	edges     map[[2]*QStructNode]bool // parent and child of every edge so far
	objects   map[string]*QStructNode  // database objects and functions, by name key
	ctes      map[*ast.CTE]*QStructNode
	expanding map[*QStructNode]bool // views whose definitions are being read
	withs     []*ast.With           // every WITH clause seen, to report unused CTEs
	anonymous int                   // subqueries without an alias so far
	diags     []Diagnostic
}

func (b *builder) diag(format string, args ...any) {
	b.diags = append(b.diags, Diagnostic{Message: fmt.Sprintf(format, args...)})
}

// node creates a node with an ID based on base. This and addChild take
// constant time however many nodes share a name or a parent, because the
// input may be hostile (see TestPerfQStruct).
func (b *builder) node(base string, kind QStructKind) *QStructNode {
	id := base
	for i := max(b.suffixes[base], 2); b.ids[id]; i++ {
		id = fmt.Sprintf("%s#%d", base, i)
		b.suffixes[base] = i + 1
	}
	b.ids[id] = true
	return &QStructNode{ID: id, Kind: kind}
}

func (b *builder) addChild(parent, child *QStructNode) {
	edge := [2]*QStructNode{parent, child}
	if child == nil || b.edges[edge] {
		return
	}
	b.edges[edge] = true
	parent.Children = append(parent.Children, child)
}

func (b *builder) name(n *ast.Name) catalog.Name {
	w, err := b.r.written(n)
	if err != nil {
		b.diag("%v", err)
	}
	return w
}

func (b *builder) query(q *ast.Query, n *QStructNode, sc *scope) {
	if q.With != nil {
		b.withs = append(b.withs, q.With)
		sc = b.finder.with(q.With, sc)
	}
	b.body(q.Body, n, sc)
	b.exprs(q.OrderBy, n, sc)
	b.exprs(q.Limit, n, sc)
	// FOR UPDATE OF names aliases, not objects.
	for _, op := range q.Pipes { // BigQuery's |> steps, in order
		switch x := op.(type) {
		case *ast.PipeJoin:
			b.table(x.Join.Right, n, sc)
			b.exprs(x.Join.On, n, sc)
		case *ast.PipeSetOp: // more branches, like a set operation's
			for _, branch := range x.Queries {
				b.query(branch, n, sc)
			}
		case *ast.PipeCall:
			b.function(x.Func, n, sc)
		case *ast.PipeWith: // seen by the steps after it
			b.withs = append(b.withs, x.With)
			sc = b.finder.with(x.With, sc)
		default:
			b.exprs(op, n, sc)
		}
	}
}

func (b *builder) body(body ast.QueryBody, n *QStructNode, sc *scope) {
	switch x := body.(type) {
	case *ast.Select:
		// In the order the clauses are written.
		b.exprs(x.DistinctOn, n, sc)
		b.exprs(x.Items, n, sc)
		if x.Into != nil {
			b.diag("SELECT INTO %s is left out: QStruct shows what a query reads", b.name(x.Into.Name))
		}
		for _, t := range x.From {
			b.table(t, n, sc)
		}
		b.exprs(x.Where, n, sc)
		b.exprs(x.GroupBy, n, sc)
		b.exprs(x.Having, n, sc)
		b.exprs(x.Windows, n, sc)
		b.exprs(x.Qualify, n, sc)
	case *ast.SetOp:
		b.body(x.Left, n, sc)
		b.body(x.Right, n, sc)
	case *ast.Values:
		b.exprs(x.Rows, n, sc)
	case *ast.ParenQuery:
		b.query(x.Query, n, sc)
	case *ast.TableBody:
		b.addChild(n, b.relation(x.Name, sc))
	case *ast.FromQuery:
		for _, t := range x.From {
			b.table(t, n, sc)
		}
	default:
		panic(fmt.Sprintf("analyze: unexpected query body %T", body))
	}
}

func (b *builder) table(t ast.TableExpr, n *QStructNode, sc *scope) {
	switch x := t.(type) {
	case *ast.TableRef:
		b.addChild(n, b.relation(x.Name, sc))
		b.exprs(x.Sample, n, sc)
	case *ast.SubqueryRef:
		alias := ""
		if x.Alias != nil {
			alias = b.r.ident(x.Alias.Name)
		}
		sub := b.subquery(alias)
		b.addChild(n, sub)
		b.query(x.Query, sub, sc)
	case *ast.FuncRef:
		if x.Func != nil {
			b.function(x.Func, n, sc)
		}
		for _, f := range x.RowsFrom {
			b.function(f, n, sc)
		}
		b.exprs(x.Expr, n, sc)
	case *ast.Join:
		b.table(x.Left, n, sc)
		b.table(x.Right, n, sc)
		b.exprs(x.On, n, sc)
	case *ast.ParenTable:
		b.table(x.Expr, n, sc)
	default:
		panic(fmt.Sprintf("analyze: unexpected FROM item %T", t))
	}
}

func (b *builder) subquery(alias string) *QStructNode {
	if alias == "" {
		b.anonymous++
		return b.node(fmt.Sprintf("subquery %d", b.anonymous), QStructSubquery)
	}
	sub := b.node(alias, QStructSubquery)
	sub.Name = catalog.Name{Object: alias}
	return sub
}

// function adds a table function, and walks its arguments for subqueries.
func (b *builder) function(f *ast.FuncCall, n *QStructNode, sc *scope) {
	name := b.name(f.Name)
	key := "f\x00" + b.r.nameKey(name)
	fn := b.objects[key]
	if fn == nil {
		fn = b.node(name.String(), QStructFunction)
		fn.Name, fn.Status = name, Unchecked
		b.objects[key] = fn
	}
	b.addChild(n, fn)
	b.exprs(f, n, sc)
}

// relation resolves a table name: to a CTE in scope, else to an object.
func (b *builder) relation(name *ast.Name, sc *scope) *QStructNode {
	if len(name.Parts) == 1 {
		if c, csc := b.finder.find(b.r.ident(name.Parts[0]), sc); c != nil {
			return b.cteNode(c, csc)
		}
	}
	full, obj, status := b.r.object(b.name(name))
	key := "o\x00" + b.r.nameKey(full)
	if n := b.objects[key]; n != nil {
		if b.expanding[n] {
			b.diag("view %s depends on itself", n.Name)
			return nil
		}
		return n
	}
	kind := QStructObject
	if obj != nil && obj.Kind != "" {
		kind = QStructKind(obj.Kind)
	}
	n := b.node(full.String(), kind)
	n.Name, n.Status = full, status
	b.objects[key] = n
	if obj != nil && obj.Definition != "" && b.o.ExpandViews {
		b.expand(n, obj.Definition)
	}
	return n
}

func (b *builder) cteNode(c *ast.CTE, sc *scope) *QStructNode {
	if n := b.ctes[c]; n != nil {
		return n
	}
	name := b.r.ident(c.Name)
	n := b.node(name, QStructCTE)
	n.Name = catalog.Name{Object: name}
	b.ctes[c] = n
	if c.Query == nil {
		b.diag("data-modifying CTE %s is not analysed", name)
		return n
	}
	b.query(c.Query, n, sc)
	return n
}

// expand reads a view's definition, so that its dependencies become the
// view node's children. The definition sees none of the query's CTEs.
func (b *builder) expand(n *QStructNode, definition string) {
	stmts, err := parser.Parse(definition, b.r.d)
	if err != nil {
		b.diag("the definition of view %s does not parse: %v", n.Name, err)
		return
	}
	var q *ast.Query
	if len(stmts) == 1 {
		switch s := stmts[0].(type) {
		case *ast.Query:
			q = s
		case *ast.CreateView:
			q = s.Query
		}
	}
	if q == nil {
		b.diag("the definition of view %s is not one query or CREATE VIEW statement", n.Name)
		return
	}
	b.expanding[n] = true
	b.query(q, n, nil)
	delete(b.expanding, n)
}

// mayHideQuery matches raw argument text that may contain a subquery.
var mayHideQuery = regexp.MustCompile(`(?i)\bselect\b`)

// exprs finds the dependencies inside expressions: subqueries, and SQLite's
// `x IN table`. It walks any AST value by reflection, so new expression types
// are covered without changes here. It must not be given a FROM item.
func (b *builder) exprs(v any, n *QStructNode, sc *scope) {
	b.walk(reflect.ValueOf(v), n, sc)
}

func (b *builder) walk(v reflect.Value, n *QStructNode, sc *scope) {
	switch v.Kind() {
	case reflect.Interface:
		if !v.IsNil() {
			b.walk(v.Elem(), n, sc)
		}
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		switch x := v.Interface().(type) {
		case *ast.Query:
			sub := b.subquery("")
			b.addChild(n, sub)
			b.query(x, sub, sc)
			return
		case *ast.In:
			b.exprs(x.Expr, n, sc)
			b.exprs(x.List, n, sc)
			b.exprs(x.Subquery, n, sc)
			if x.Table != nil {
				b.addChild(n, b.relation(x.Table, sc))
			}
			if x.Func != nil {
				b.function(x.Func, n, sc)
			}
			return
		case *ast.TableArg: // TABLE t passed to a table-valued function reads t
			if x.Kind == "TABLE" {
				b.addChild(n, b.relation(x.Name, sc))
			}
			return
		case *ast.FuncCall:
			if x.RawArgs != nil && mayHideQuery.MatchString(*x.RawArgs) {
				b.diag("the arguments of %s are kept as text, so a subquery in them is not analysed",
					b.name(x.Name))
			}
		}
		b.walk(v.Elem(), n, sc)
	case reflect.Struct:
		t := v.Type()
		for i := range v.NumField() {
			if t.Field(i).IsExported() {
				b.walk(v.Field(i), n, sc)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			b.walk(v.Index(i), n, sc)
		}
	}
}

// Nodes returns every node once, depth first from the root.
func (g *QStructGraph) Nodes() []*QStructNode {
	if g.Root == nil {
		return nil
	}
	var out []*QStructNode
	seen := map[*QStructNode]bool{}
	var visit func(*QStructNode)
	visit = func(n *QStructNode) {
		if seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
		for _, c := range n.Children {
			visit(c)
		}
	}
	visit(g.Root)
	return out
}

// Objects returns the database objects and table functions the root depends
// on, directly or through CTEs, subqueries and expanded views, depth first.
func (g *QStructGraph) Objects() []*QStructNode {
	var out []*QStructNode
	for _, n := range g.Nodes() {
		switch {
		case n == g.Root, n.Kind == QStructQuery, n.Kind == QStructCTE, n.Kind == QStructSubquery:
		default:
			out = append(out, n)
		}
	}
	return out
}

// String lists the nodes one per line, depth first, as
// "id: kind [status] [-> child, ...]".
func (g *QStructGraph) String() string {
	var sb strings.Builder
	for _, n := range g.Nodes() {
		sb.WriteString(n.ID + ": " + string(n.Kind))
		if n.Status != Resolved {
			sb.WriteString(" " + string(n.Status))
		}
		for i, c := range n.Children {
			if i == 0 {
				sb.WriteString(" -> ")
			} else {
				sb.WriteString(", ")
			}
			sb.WriteString(c.ID)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// MarshalJSON writes the graph as a list of nodes whose children are given by
// ID, because a JSON tree cannot share a node:
//
//	{"root": "query", "nodes": [
//	  {"id": "query", "kind": "query", "children": ["x"]},
//	  {"id": "x", "kind": "cte", "name": "x", "children": ["public.t"]},
//	  {"id": "public.t", "kind": "table", "schema": "public", "name": "t"}]}
func (g *QStructGraph) MarshalJSON() ([]byte, error) {
	type jsonNode struct {
		ID       string      `json:"id"`
		Kind     QStructKind `json:"kind"`
		Database string      `json:"database,omitempty"`
		Schema   string      `json:"schema,omitempty"`
		Name     string      `json:"name,omitempty"`
		Status   NameStatus  `json:"status,omitempty"`
		Children []string    `json:"children,omitempty"`
	}
	out := struct {
		Root        string       `json:"root"`
		Nodes       []jsonNode   `json:"nodes"`
		Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	}{Nodes: []jsonNode{}, Diagnostics: g.Diagnostics}
	for _, n := range g.Nodes() {
		if n == g.Root {
			out.Root = n.ID
		}
		j := jsonNode{ID: n.ID, Kind: n.Kind, Database: n.Name.Database, Schema: n.Name.Schema,
			Name: n.Name.Object, Status: n.Status}
		for _, c := range n.Children {
			j.Children = append(j.Children, c.ID)
		}
		out.Nodes = append(out.Nodes, j)
	}
	return json.Marshal(out)
}
