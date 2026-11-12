package analyze

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"sqlp/ast"
	"sqlp/catalog"
	"sqlp/parser"
)

// This file works out which database columns each column of a query is
// computed from. Every relation a query reads (table, view, CTE, subquery,
// table function, join) becomes a rel: a list of columns, each with the set
// of sources it comes from. Sources are interned, so a set is a sorted slice
// of ints.
//
// Columns that are not known one by one, such as those of a table the
// catalog does not list, are an openCols entry: a name looked up there
// becomes that column of the table.

// maxCols bounds the columns of one relation. Both engines reject more
// (PostgreSQL allows 1664 output columns, SQLite 2000 by default), and
// without a bound, stars over joined CTEs could double the count at each
// level.
const maxCols = 2000

// maxColWork bounds the columns of all the relations one analysis builds,
// which bounds its memory however many CTEs a hostile query stacks up. Real
// queries stay far below it. Past it, each new relation's columns are merged
// into one entry, so a column may list more sources than it uses, but none
// is lost.
const maxColWork = 200_000

// source is one thing a column can be computed from.
type source struct {
	obj    *objRef // nil for a column that could not be given an object
	column string  // "" for a star
	star   bool    // all the columns of obj
	status NameStatus
	// A stand-in for column index of a recursive CTE that is still being
	// read; it resolves to that column's sources once they are known.
	self  *cteState
	index int
}

// objRef is a database object that the query reads.
type objRef struct {
	name   catalog.Name // full name when found, otherwise as written
	key    string
	obj    *catalog.Object
	status NameStatus
	cols   map[string]string // the catalog's columns by key, as stored; nil when it lists none
	rel    *rel              // the object as a relation, built once
}

// rel is a relation as the column references of a query see it.
type rel struct {
	name  string        // the key a one-part qualifier matches; "" when none can
	full  *catalog.Name // an unaliased database object's name, for longer qualifiers
	obj   *objRef       // the database object it is, even under an alias
	label string        // for diagnostics
	cols  []col
	// built on the first lookup
	index map[string][]int
	opens []*openCols
}

// col is one column of a rel or, with open set, an unknown number of them.
type col struct {
	name string // "" when open, or when analysis cannot tell the name
	key  string
	srcs []int  // what it is computed from, sorted
	from string // the relation it belongs to, for diagnostics
	open *openCols
}

// openCols are columns that are not known one by one: a table's that the
// catalog does not list, a table function's, or a mix of those when the
// branches of a UNION cannot be matched column by column.
type openCols struct {
	objs   []*objRef // a column named n is column n of each of these,
	srcs   []int     // computed from these as well
	labels []string  // for diagnostics, at most maxLabels
	more   int       // labels left out
}

const maxLabels = 3

// colScope is what the column references of one query level can see.
type colScope struct {
	parent  *colScope
	ctes    *scope
	items   []*rel // the FROM items: unqualified names search their columns
	pending []*rel // the left sides of the joins being read, for LATERAL
	bases   []*rel // every relation that a qualifier can name
	windows []*ast.NamedWindow
	// indexes, built on first use
	known      map[string][]*col
	opens      []*openCols
	byName     map[string][]*rel
	itemsIndex int // len(items) when known and opens were built
	basesIndex int // len(bases) when byName was built
}

// cteState is a CTE whose columns are known, or being worked out.
type cteState struct {
	name  string
	names []string // the CTE's column list
	done  bool
	rel   *rel  // once done
	first []col // the columns of the first branch, which a self-reference sees
	stand []int // the stand-in source of each column, made on first use
	final [][]int
	// uses of stand-ins before the sources were known
	pending     []pendingUse
	pendingSeen map[pendingUse]bool
}

type pendingUse struct{ id, clause int }

// colBuilder holds the state of one ListOutCols call.
type colBuilder struct {
	r         *resolver
	o         Options
	finder    *cteFinder
	srcs      []source
	srcIDs    map[string]int
	used      []uint8 // per source, a bit for each clause it is used in
	order     [][]int // per clause, the sources first used there, in order
	objects   map[string]*objRef
	expanding map[*objRef]bool // views whose definitions are being read
	ctes      map[*ast.CTE]*cteState
	outer     map[*ast.With]*colScope // the column scope of the query that has each WITH
	diags     []Diagnostic
	diagSeen  map[string]bool
	work      int                       // column entries allocated so far, for maxColWork
	inWindow  map[*ast.NamedWindow]bool // named windows being read
}

func newColBuilder(r *resolver, o Options) *colBuilder {
	return &colBuilder{
		r:         r,
		o:         o,
		finder:    newCTEFinder(r),
		srcIDs:    map[string]int{},
		order:     make([][]int, len(clauses)),
		objects:   map[string]*objRef{},
		expanding: map[*objRef]bool{},
		ctes:      map[*ast.CTE]*cteState{},
		outer:     map[*ast.With]*colScope{},
		diagSeen:  map[string]bool{},
		inWindow:  map[*ast.NamedWindow]bool{},
	}
}

func (b *colBuilder) diag(format string, args ...any) {
	m := fmt.Sprintf(format, args...)
	if !b.diagSeen[m] {
		b.diagSeen[m] = true
		b.diags = append(b.diags, Diagnostic{Message: m})
	}
}

// ---------------------------------------------------------------------------
// Sources

func (b *colBuilder) newSource(s source) int {
	b.srcs = append(b.srcs, s)
	b.used = append(b.used, 0)
	return len(b.srcs) - 1
}

func (b *colBuilder) intern(key string, s source) int {
	if id, ok := b.srcIDs[key]; ok {
		return id
	}
	id := b.newSource(s)
	b.srcIDs[key] = id
	return id
}

// objColumn is the source for the column of o named name.
func (b *colBuilder) objColumn(o *objRef, name string) int {
	key := b.r.key(name)
	s := source{obj: o, column: name, status: o.status}
	switch {
	case o.cols != nil:
		if stored, ok := o.cols[key]; ok {
			s.column, s.status = stored, Resolved
		} else if b.systemColumn(o, key) {
			s.status = Resolved
		} else {
			s.status = NotFound
		}
	case o.status == Resolved:
		s.status = Unchecked // the catalog lists the object without columns
	}
	return b.intern(o.key+"\x00c\x00"+key, s)
}

// objStar is the source for all the columns of o, which the catalog does not
// list.
func (b *colBuilder) objStar(o *objRef) int {
	status := o.status
	if status == Resolved {
		status = Unchecked
	}
	return b.intern(o.key+"\x00*", source{obj: o, star: true, status: status})
}

// systemColumn reports whether key is a column that o has without listing it.
func (b *colBuilder) systemColumn(o *objRef, key string) bool {
	if o.obj != nil && o.obj.Kind == catalog.View {
		return false
	}
	return slices.ContainsFunc(b.r.d.Names.SystemColumns, func(c string) bool { return b.r.key(c) == key })
}

// ambiguous is the source for a column that could belong to any of the
// relations labels names.
func (b *colBuilder) ambiguous(name, written string, labels []string, more int) []int {
	if len(labels) == 1 && more == 0 {
		b.diag("column %s is ambiguous: %s has more than one", written, labels[0])
	} else {
		b.diag("column %s could come from %s", written, orList(labels, more))
	}
	return []int{b.intern("\x00?\x00"+b.r.key(name), source{column: name, status: Ambiguous})}
}

// orList joins names as "a, b or c", saying how many more there are.
func orList(names []string, more int) string {
	if more > 0 {
		return strings.Join(names, ", ") + " or " + strconv.Itoa(more) + " more"
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// addLabel adds name to a deduplicated, bounded list of labels.
func addLabel(labels []string, more int, name string) ([]string, int) {
	if slices.Contains(labels, name) {
		return labels, more
	}
	if len(labels) == maxLabels {
		return labels, more + 1
	}
	return append(labels, name), more
}

// emit records that the sources ids are used in clause cl. A stand-in counts
// as what it resolves to, and waits until that is known.
func (b *colBuilder) emit(ids []int, cl int) {
	for _, id := range ids {
		b.emitOne(id, cl)
	}
}

func (b *colBuilder) emitOne(id, cl int) {
	s := &b.srcs[id]
	if s.self != nil && !s.self.done {
		p := pendingUse{id, cl}
		if !s.self.pendingSeen[p] {
			s.self.pendingSeen[p] = true
			s.self.pending = append(s.self.pending, p)
		}
		return
	}
	if b.used[id]&(1<<cl) != 0 {
		return
	}
	b.used[id] |= 1 << cl
	if s.self == nil {
		b.order[cl] = append(b.order[cl], id)
		return
	}
	if s.index < len(s.self.final) {
		b.emit(s.self.final[s.index], cl)
	}
}

// union merges two sorted sets.
func union(a, b []int) []int {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 || slices.Equal(a, b) {
		return a
	}
	out := make([]int, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			out = append(out, a[i])
			i++
		case a[i] > b[j]:
			out = append(out, b[j])
			j++
		default:
			out = append(out, a[i])
			i++
			j++
		}
	}
	out = append(out, a[i:]...)
	return append(out, b[j:]...)
}

// set sorts ids and drops duplicates.
func set(ids []int) []int {
	slices.Sort(ids)
	return slices.Compact(ids)
}

// allSrcs is everything cols are computed from, with a star for each object
// whose columns are not known.
func (b *colBuilder) allSrcs(cols []col) []int {
	var out []int
	for _, c := range cols {
		if c.open == nil {
			out = append(out, c.srcs...)
			continue
		}
		out = append(out, c.open.srcs...)
		for _, o := range c.open.objs {
			out = append(out, b.objStar(o))
		}
	}
	return set(out)
}

// openSrcs is what column name of the open columns o is computed from.
func (b *colBuilder) openSrcs(o *openCols, name string) []int {
	out := slices.Clone(o.srcs)
	for _, obj := range o.objs {
		out = append(out, b.objColumn(obj, name))
	}
	return set(out)
}

// sameCols reports whether a set operation of a and b would give a.
func sameCols(a, b []col) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].open != b[i].open || !slices.Equal(a[i].srcs, b[i].srcs) {
			return false
		}
	}
	return true
}

func (b *colBuilder) newCol(name string, srcs []int, from string) col {
	c := col{name: name, srcs: srcs, from: from}
	if name != "" {
		c.key = b.r.key(name)
	}
	return c
}

// fit keeps a new relation's columns within maxCols, and merges them once
// the analysis has allocated maxColWork column entries.
func (b *colBuilder) fit(cols []col, what string) []col {
	if len(cols) > maxCols {
		b.diag("%s has more than %d columns; the rest are left out", what, maxCols)
		cols = cols[:maxCols:maxCols]
	}
	if b.work <= maxColWork || len(cols) < 2 {
		return cols
	}
	b.diag("the query has too many columns to follow one by one: past the first %d, the columns of each relation are merged, "+
		"so a column may list sources it does not use", maxColWork)
	return []col{b.merged(cols)}
}

// merged is cols as one entry of unknown columns, which a name looked up
// there resolves to all the sources of.
func (b *colBuilder) merged(cols []col) col {
	if cols[0].open != nil && !slices.ContainsFunc(cols, func(c col) bool { return c.open != cols[0].open }) {
		return cols[0] // copies of one merged entry, as in a self-join
	}
	o := &openCols{}
	var srcs []int
	seen := map[*objRef]bool{}
	for _, c := range cols {
		if c.open == nil {
			srcs = append(srcs, c.srcs...)
			if c.from != "" {
				o.labels, o.more = addLabel(o.labels, o.more, c.from)
			}
			continue
		}
		srcs = append(srcs, c.open.srcs...)
		for _, obj := range c.open.objs {
			if !seen[obj] {
				seen[obj] = true
				o.objs = append(o.objs, obj)
			}
		}
		for _, l := range c.open.labels {
			o.labels, o.more = addLabel(o.labels, o.more, l)
		}
		o.more += c.open.more
	}
	o.srcs = set(srcs)
	return col{open: o}
}

// concat appends more to cols, but stops one column past maxCols, which is
// enough for fit to notice. It shares more when cols is empty: column lists
// are never changed in place.
func (b *colBuilder) concat(cols, more []col) []col {
	if len(cols) == 0 {
		n := min(len(more), maxCols+1)
		return more[:n:n]
	}
	if len(cols) > maxCols {
		return cols
	}
	more = more[:min(len(more), maxCols+1-len(cols))]
	b.work += len(cols) + len(more)
	return append(slices.Clip(cols), more...)
}

// relabel returns cols as columns of the relation label.
func (b *colBuilder) relabel(cols []col, label string) []col {
	b.work += len(cols)
	out := make([]col, len(cols))
	for i, c := range cols {
		c.from = label
		out[i] = c
	}
	return out
}

// ---------------------------------------------------------------------------
// Looking up columns

// lookup finds the columns of rl named key: the known ones, or failing
// those, the open ones that could hold it.
func (b *colBuilder) lookup(rl *rel, key string) ([]*col, []*openCols) {
	if rl.index == nil {
		rl.index = map[string][]int{}
		for i, c := range rl.cols {
			if c.open != nil {
				rl.opens = append(rl.opens, c.open)
			} else if c.key != "" {
				rl.index[c.key] = append(rl.index[c.key], i)
			}
		}
	}
	var known []*col
	for _, i := range rl.index[key] {
		known = append(known, &rl.cols[i])
	}
	if len(known) > 0 {
		return known, nil
	}
	return nil, rl.opens
}

// inRel is what column name of rl is computed from; written is the
// reference as the query has it, for diagnostics.
func (b *colBuilder) inRel(rl *rel, name, written string) []int {
	known, opens := b.lookup(rl, b.r.key(name))
	switch {
	case len(known) == 1:
		return known[0].srcs
	case len(known) > 1:
		var labels []string
		more := 0
		for _, c := range known {
			labels, more = addLabel(labels, more, c.from)
		}
		return b.ambiguous(name, written, labels, more)
	case len(opens) == 1:
		return b.openSrcs(opens[0], name)
	case len(opens) > 1:
		labels, more := openLabels(opens)
		return b.ambiguous(name, written, labels, more)
	case rl.obj != nil:
		return []int{b.objColumn(rl.obj, name)}
	}
	b.diag("%s has no column %s", rl.label, name)
	return nil
}

// openLabels names the relations opens come from. Once maxLabels are named,
// the rest are counted, roughly: they are no longer told apart.
func openLabels(opens []*openCols) ([]string, int) {
	var labels []string
	more := 0
	for i, o := range opens {
		if len(labels) == maxLabels {
			for _, o := range opens[i:] {
				more += len(o.labels) + o.more
			}
			break
		}
		for _, l := range o.labels {
			labels, more = addLabel(labels, more, l)
		}
		more += o.more
	}
	return labels, more
}

func (sc *colScope) indexItems() {
	if sc.known != nil && sc.itemsIndex == len(sc.items) {
		return
	}
	sc.known, sc.opens, sc.itemsIndex = map[string][]*col{}, nil, len(sc.items)
	for _, rl := range sc.items {
		for i := range rl.cols {
			c := &rl.cols[i]
			if c.open != nil {
				sc.opens = append(sc.opens, c.open)
			} else if c.key != "" {
				sc.known[c.key] = append(sc.known[c.key], c)
			}
		}
	}
}

// relNamed finds the relation a qualifier names, from sc outwards.
func (b *colBuilder) relNamed(qual []string, sc *colScope) *rel {
	for s := sc; s != nil; s = s.parent {
		if len(qual) == 1 {
			if s.byName == nil || s.basesIndex != len(s.bases) {
				s.byName, s.basesIndex = map[string][]*rel{}, len(s.bases)
				for _, rl := range s.bases {
					if rl.name != "" {
						s.byName[rl.name] = append(s.byName[rl.name], rl)
					}
				}
			}
			if rs := s.byName[b.r.key(qual[0])]; len(rs) > 0 {
				return rs[0]
			}
			continue
		}
		for _, rl := range s.bases {
			if rl.full != nil && b.fullMatches(*rl.full, qual) {
				return rl
			}
		}
	}
	return nil
}

// fullMatches reports whether a qualifier of two or three parts names the
// object full. A part the object's name lacks matches anything, since the
// object was then not found in the catalog and may be in any schema.
func (b *colBuilder) fullMatches(full catalog.Name, qual []string) bool {
	n := len(qual)
	if n > 3 || !b.r.sameObject(full.Object, qual[n-1]) || full.Schema != "" && !b.r.sameObject(full.Schema, qual[n-2]) {
		return false
	}
	return n < 3 || full.Database == "" || b.r.sameObject(full.Database, qual[0])
}

func (b *colBuilder) idents(n *ast.Name) []string {
	parts := make([]string, len(n.Parts))
	for i, p := range n.Parts {
		parts[i] = b.r.ident(p)
	}
	return parts
}

// column is what a column reference is computed from.
func (b *colBuilder) column(n *ast.Name, sc *colScope) []int {
	parts := b.idents(n)
	written := strings.Join(parts, ".")
	if len(parts) == 1 {
		return b.unqualified(parts[0], sc)
	}
	if len(parts) > 4 {
		b.diag("%s has too many dotted parts for a column", written)
		return nil
	}
	rl := b.relNamed(parts[:len(parts)-1], sc)
	if rl == nil {
		b.diag("%s names no relation in scope", strings.Join(parts[:len(parts)-1], "."))
		return nil
	}
	return b.inRel(rl, parts[len(parts)-1], written)
}

// unqualified finds an unqualified column, from the innermost query
// outwards. A relation that has the column wins over one whose columns are
// not known, since the engine would reject the query if both had it.
func (b *colBuilder) unqualified(name string, sc *colScope) []int {
	key := b.r.key(name)
	for s := sc; s != nil; s = s.parent {
		s.indexItems()
		known := s.known[key]
		switch {
		case len(known) == 1:
			return known[0].srcs
		case len(known) > 1:
			var labels []string
			more := 0
			for _, c := range known {
				labels, more = addLabel(labels, more, c.from)
			}
			return b.ambiguous(name, name, labels, more)
		}
		if b.isSystemName(key) {
			var objs []*objRef
			for _, rl := range s.bases {
				if rl.obj != nil && b.systemColumn(rl.obj, key) && !slices.Contains(objs, rl.obj) {
					objs = append(objs, rl.obj)
				}
			}
			switch {
			case len(objs) == 1:
				return []int{b.objColumn(objs[0], name)}
			case len(objs) > 1:
				var labels []string
				more := 0
				for _, o := range objs {
					labels, more = addLabel(labels, more, o.name.String())
				}
				return b.ambiguous(name, name, labels, more)
			}
		}
		switch {
		case len(s.opens) == 1:
			return b.openSrcs(s.opens[0], name)
		case len(s.opens) > 1:
			labels, more := openLabels(s.opens)
			return b.ambiguous(name, name, labels, more)
		}
	}
	// A relation's name alone is the whole row (PostgreSQL).
	if rl := b.relNamed([]string{name}, sc); rl != nil {
		return b.allSrcs(rl.cols)
	}
	b.diag("column %s is not a column of any relation in scope", name)
	return nil
}

func (b *colBuilder) isSystemName(key string) bool {
	return slices.ContainsFunc(b.r.d.Names.SystemColumns, func(c string) bool { return b.r.key(c) == key })
}

// ---------------------------------------------------------------------------
// Queries

// query works out the output columns of q. Its column references can see
// outer, and its relation names the CTEs of ctes. first, when set, gets the
// columns of q's first branch as soon as they are known, which a recursive
// CTE needs.
func (b *colBuilder) query(q *ast.Query, outer *colScope, ctes *scope, first func([]col)) []col {
	if q.With != nil {
		ctes = b.finder.with(q.With, ctes)
		b.outer[q.With] = outer
	}
	if len(q.Pipes) == 0 {
		return b.body(q.Body, outer, ctes, first)
	}
	var sc *colScope
	if fq, ok := q.Body.(*ast.FromQuery); ok { // the FROM items stay visible to the steps
		sc = b.fromScope(fq, outer, ctes)
	} else {
		rl := &rel{cols: b.body(q.Body, outer, ctes, first)}
		if alias := pipeInputAlias(q.Body); alias != nil { // (query) AS x |> ...
			name := b.r.ident(*alias)
			rl = &rel{name: b.r.key(name), label: name, cols: b.relabel(rl.cols, name)}
		}
		sc = &colScope{parent: outer, ctes: ctes, items: []*rel{rl}}
	}
	return b.pipes(q.Pipes, sc)
}

// pipeInputAlias returns the name BigQuery gives the rows of body in the pipe
// steps after it: the alias of (query) AS x, also from within more parentheses.
func pipeInputAlias(body ast.QueryBody) *ast.Ident {
	for {
		pq, ok := body.(*ast.ParenQuery)
		switch {
		case !ok:
			return nil
		case pq.Alias != nil:
			return pq.Alias
		case pq.Query.With != nil || pq.Query.Pipes != nil:
			return nil
		}
		body = pq.Query.Body
	}
}

func (b *colBuilder) fromScope(fq *ast.FromQuery, outer *colScope, ctes *scope) *colScope {
	sc := &colScope{parent: outer, ctes: ctes}
	for _, t := range fq.From {
		sc.items = append(sc.items, b.fromItem(t, sc))
	}
	return sc
}

// pipes runs BigQuery's |> steps. The table between two steps is a scope of
// relations, so that the names it carries, such as the aliases of FROM t AS
// x, stay visible until a step like SELECT makes a new table. Steps that only
// filter or order rows change no columns: columns used only there are not
// listed, as for WHERE and ORDER BY.
func (b *colBuilder) pipes(ops []ast.PipeOp, sc *colScope) []col {
	table := func(cols []col, name string) *colScope { // a new table, which steps after it read
		rl := &rel{name: b.r.key(name), label: name, cols: b.relabel(cols, name)}
		return &colScope{parent: sc.parent, ctes: sc.ctes, items: []*rel{rl}, bases: []*rel{rl}}
	}
	items := func(list []*ast.SelectItem) []col {
		var cols []col
		for _, it := range list {
			cols = b.appendCols(cols, b.selectItem(it, sc))
		}
		return cols
	}
	// edit makes a copy of the scope whose columns f has changed.
	edit := func(f func(c col) []col) *colScope {
		out := &colScope{parent: sc.parent, ctes: sc.ctes}
		for _, rl := range sc.items {
			cp := *rl
			cp.cols = nil
			for _, c := range rl.cols {
				cp.cols = append(cp.cols, f(c)...)
			}
			out.items = append(out.items, &cp)
		}
		out.bases = out.items
		return out
	}
	for _, op := range ops {
		switch x := op.(type) {
		case *ast.PipeSelect:
			windowed := *sc // its WINDOW clause names windows for its items
			windowed.windows = x.Windows
			outer := sc
			sc = &windowed
			cols := b.fit(items(x.Items), "a SELECT list")
			sc = outer
			sc = table(cols, "")
		case *ast.PipeAggregate:
			var cols []col
			for _, list := range [][]*ast.PipeAggItem{x.GroupBy, x.Items} { // the grouping columns come first
				for _, it := range list {
					name := b.outName(it.Expr)
					if it.Alias != nil {
						name = b.r.ident(*it.Alias)
					}
					cols = b.appendCols(cols, []col{b.newCol(name, b.exprSrcs(it.Expr, sc), "")})
				}
			}
			sc = table(cols, "")
		case *ast.PipeExtend, *ast.PipeWindow:
			var list []*ast.SelectItem
			outer := sc
			if e, ok := x.(*ast.PipeExtend); ok {
				list = e.Items
				windowed := *sc // its WINDOW clause names windows for its items
				windowed.windows = e.Windows
				sc = &windowed
			} else {
				list = x.(*ast.PipeWindow).Items
			}
			added := &rel{cols: items(list)}
			sc = outer
			sc = &colScope{parent: sc.parent, ctes: sc.ctes, items: append(slices.Clip(sc.items), added),
				bases: slices.Clip(sc.bases)}
		case *ast.PipeSet:
			with := map[string]*ast.PipeAssign{}
			for _, a := range x.Items {
				with[b.r.key(b.r.ident(a.Column))] = a
			}
			cur := sc
			sc = edit(func(c col) []col {
				if a := with[c.key]; c.open == nil && a != nil {
					return []col{b.newCol(c.name, b.exprSrcs(a.Value, cur), c.from)}
				}
				return []col{c}
			})
		case *ast.PipeDrop:
			drop := map[string]bool{}
			for _, id := range x.Columns {
				drop[b.r.key(b.r.ident(id))] = true
			}
			sc = edit(func(c col) []col {
				if c.open == nil && drop[c.key] {
					return nil
				}
				return []col{c}
			})
		case *ast.PipeRename:
			to := map[string]string{}
			for _, r := range x.Items {
				to[b.r.key(b.r.ident(r.From))] = b.r.ident(r.To)
			}
			sc = edit(func(c col) []col {
				if n, ok := to[c.key]; c.open == nil && ok {
					c.name, c.key = n, b.r.key(n)
				}
				return []col{c}
			})
		case *ast.PipeAs:
			sc = table(b.star(&ast.Star{}, sc), b.r.ident(x.Alias))
		case *ast.PipeJoin:
			j := x.Join
			right := b.fromItem(j.Right, sc)
			next := &colScope{parent: sc.parent, ctes: sc.ctes, items: append(slices.Clip(sc.items), right),
				bases: append(slices.Clip(sc.bases), right)}
			if j.On != nil {
				b.emit(b.exprSrcs(j.On, next), inJoin)
			}
			for _, id := range j.Using { // both sides' columns of that name
				key := b.r.key(b.r.ident(id))
				for _, rl := range next.items {
					for _, c := range rl.cols {
						if c.open == nil && c.key == key {
							b.emit(c.srcs, inJoin)
						}
					}
				}
			}
			sc = next
		case *ast.PipeSetOp:
			branches := [][]col{b.star(&ast.Star{}, sc)}
			for _, q := range x.Queries {
				branches = append(branches, b.query(q, sc.parent, sc.ctes, nil))
			}
			if x.Match != nil {
				branches = b.alignByName(branches)
			}
			sc = table(b.setOp(&ast.SetOp{Op: x.Op, All: x.All}, branches), "")
		case *ast.PipeCall: // a table-valued function of the table so far
			name := strings.Join(b.idents(x.Func.Name), ".")
			if x.Alias != nil {
				name = b.r.ident(*x.Alias)
			}
			srcs := set(append(b.allSrcs(b.star(&ast.Star{}, sc)), b.callSrcs(x.Func, sc)...))
			rl := &rel{name: b.r.key(name), label: name, cols: []col{{open: &openCols{srcs: srcs, labels: []string{name}}}}}
			sc = &colScope{parent: sc.parent, ctes: sc.ctes, items: []*rel{rl}, bases: []*rel{rl}}
		case *ast.PipePivot, *ast.PipeUnpivot:
			src := &rel{cols: b.star(&ast.Star{}, sc)}
			var alias *ast.Alias
			var t ast.TableExpr
			if pv, ok := x.(*ast.PipePivot); ok {
				t, alias = pv.Pivot, pv.Pivot.Alias
			} else {
				up := x.(*ast.PipeUnpivot)
				t, alias = up.Unpivot, up.Unpivot.Alias
			}
			name := ""
			if alias != nil {
				name = b.r.ident(alias.Name)
			}
			sc = table(b.pivoted(t, src, sc), name)
		case *ast.PipeWith:
			ctes := b.finder.with(x.With, sc.ctes)
			b.outer[x.With] = sc.parent
			sc = &colScope{parent: sc.parent, ctes: ctes, items: sc.items, bases: sc.bases}
		}
	}
	return b.star(&ast.Star{}, sc)
}

// pivoted gives the columns of PIVOT or UNPIVOT over the relation src. A
// pivoted column is computed from its aggregate and the FOR column, which,
// with the aggregates' columns, leave the columns the result is grouped by. An
// unpivoted value column is computed from the columns it gathers, which leave
// the result; the name column is computed from nothing.
func (b *colBuilder) pivoted(t ast.TableExpr, src *rel, sc *colScope) []col {
	in := &colScope{parent: sc.parent, ctes: sc.ctes, items: []*rel{src}, bases: []*rel{src}}
	drop := map[string]bool{}
	var out []col
	keep := func() {
		for _, c := range src.cols {
			if c.open != nil || !drop[c.key] {
				out = append(out, c)
			}
		}
	}
	if pv, ok := t.(*ast.Pivot); ok {
		for _, e := range append([]ast.Expr{pv.For}, exprsOf(pv.Aggs)...) {
			for _, name := range namesIn(e) {
				drop[b.r.key(b.r.ident(name.Last()))] = true
			}
		}
		keep()
		forSrcs := b.exprSrcs(pv.For, in)
		for _, v := range pv.In {
			for _, agg := range pv.Aggs {
				name := b.outName(v.Expr)
				if lit, ok := v.Expr.(*ast.Literal); ok { // BigQuery names 'Q1' Q1, and 1 _1
					switch lit.Kind {
					case ast.StringLit:
						name = lit.Value
					case ast.Integer:
						name = "_" + lit.Value
					}
				}
				if v.Alias != nil {
					name = b.r.ident(*v.Alias)
				}
				if agg.Alias != nil && len(pv.Aggs) > 1 {
					name = b.r.ident(*agg.Alias) + "_" + name
				}
				out = append(out, b.newCol(name, set(append(b.exprSrcs(agg.Expr, in), forSrcs...)), ""))
			}
		}
		return out
	}
	up := t.(*ast.Unpivot)
	gathered := make([][]int, len(up.Values))
	for _, entry := range up.In {
		for i, id := range entry.Columns {
			drop[b.r.key(b.r.ident(id))] = true
			if i < len(gathered) {
				gathered[i] = append(gathered[i], b.column(&ast.Name{Parts: []ast.Ident{id}}, in)...)
			}
		}
	}
	keep()
	for i, v := range up.Values {
		out = append(out, b.newCol(b.r.ident(v), set(gathered[i]), ""))
	}
	return append(out, b.newCol(b.r.ident(up.Name), nil, ""))
}

func exprsOf(items []*ast.SelectItem) []ast.Expr {
	out := make([]ast.Expr, len(items))
	for i, it := range items {
		out[i] = it.Expr
	}
	return out
}

// namesIn lists the column references in e, without looking into subqueries.
func namesIn(e ast.Expr) []*ast.Name {
	var out []*ast.Name
	var visit func(v reflect.Value)
	visit = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Interface, reflect.Pointer:
			if v.IsNil() {
				return
			}
			if n, ok := v.Interface().(*ast.Name); ok {
				out = append(out, n)
				return
			}
			if _, ok := v.Interface().(*ast.Query); ok {
				return
			}
			visit(v.Elem())
		case reflect.Struct:
			for i := range v.NumField() {
				if v.Type().Field(i).IsExported() {
					visit(v.Field(i))
				}
			}
		case reflect.Slice:
			for i := range v.Len() {
				visit(v.Index(i))
			}
		}
	}
	visit(reflect.ValueOf(e))
	return out
}

// alignByName reorders the columns of later branches of a set operation to
// match the first branch's names, as BY NAME and CORRESPONDING do. Columns
// whose names analysis does not know stay where they are.
func (b *colBuilder) alignByName(branches [][]col) [][]col {
	out := [][]col{branches[0]}
	for _, br := range branches[1:] {
		byKey := map[string]col{}
		for _, c := range br {
			if c.open == nil && c.key != "" {
				byKey[c.key] = c
			}
		}
		var aligned []col
		for i, c := range branches[0] {
			switch m, ok := byKey[c.key]; {
			case c.open == nil && ok:
				aligned = append(aligned, m)
			case i < len(br):
				aligned = append(aligned, br[i])
			}
		}
		out = append(out, aligned)
	}
	return out
}

func (b *colBuilder) body(body ast.QueryBody, outer *colScope, ctes *scope, first func([]col)) []col {
	var cols []col
	switch x := body.(type) {
	case *ast.Select:
		cols = b.selectCols(x, outer, ctes)
	case *ast.SetOp:
		var branches [][]col
		for i, br := range setOpBranches(x) {
			if i > 0 {
				first = nil
			}
			branches = append(branches, b.body(br, outer, ctes, first))
		}
		if x.Match != nil { // BigQuery's BY NAME and CORRESPONDING
			branches = b.alignByName(branches)
		}
		return b.setOp(x, branches)
	case *ast.ParenQuery:
		return b.query(x.Query, outer, ctes, first)
	case *ast.Values:
		cols = b.values(x, &colScope{parent: outer, ctes: ctes})
	case *ast.TableBody:
		cols = b.relation(x.Name, ctes).cols
	case *ast.FromQuery:
		cols = b.star(&ast.Star{}, b.fromScope(x, outer, ctes))
	default:
		panic(fmt.Sprintf("analyze: unexpected query body %T", body))
	}
	if first != nil {
		first(cols)
	}
	return cols
}

func (b *colBuilder) selectCols(s *ast.Select, outer *colScope, ctes *scope) []col {
	sc := &colScope{parent: outer, ctes: ctes, windows: s.Windows}
	for _, t := range s.From {
		sc.items = append(sc.items, b.fromItem(t, sc))
	}
	if len(s.Items) == 1 {
		return b.fit(b.selectItem(s.Items[0], sc), "a SELECT list") // may share a relation's columns
	}
	var cols []col
	for _, it := range s.Items {
		cols = b.appendCols(cols, b.selectItem(it, sc))
		if len(cols) > maxCols {
			break
		}
	}
	return b.fit(cols, "a SELECT list")
}

// appendCols appends more to cols, which the caller owns, stopping one
// column past maxCols.
func (b *colBuilder) appendCols(cols, more []col) []col {
	more = more[:max(0, min(len(more), maxCols+1-len(cols)))]
	b.work += len(more)
	return append(cols, more...)
}

func (b *colBuilder) selectItem(it *ast.SelectItem, sc *colScope) []col {
	if st, ok := it.Expr.(*ast.Star); ok {
		return b.starModifiers(b.star(st, sc), st, sc)
	}
	name := ""
	if it.Alias != nil {
		name = b.r.ident(*it.Alias)
	} else {
		name = b.outName(it.Expr)
	}
	return []col{b.newCol(name, b.exprSrcs(it.Expr, sc), "")}
}

func (b *colBuilder) star(st *ast.Star, sc *colScope) []col {
	if st.Qualifier == nil {
		if len(sc.items) == 1 {
			return sc.items[0].cols
		}
		var cols []col
		for _, rl := range sc.items {
			cols = b.appendCols(cols, rl.cols)
		}
		return cols
	}
	qual := b.idents(st.Qualifier)
	if rl := b.relNamed(qual, sc); rl != nil {
		return rl.cols
	}
	b.diag("%s names no relation in scope", strings.Join(qual, "."))
	return nil
}

// starModifiers applies BigQuery's * EXCEPT (a) and * REPLACE (expr AS a):
// a column left out is not part of the result, and a replaced one is
// computed from its expression instead.
func (b *colBuilder) starModifiers(cols []col, st *ast.Star, sc *colScope) []col {
	if len(st.Except) == 0 && len(st.Replace) == 0 {
		return cols
	}
	drop := map[string]bool{}
	for _, id := range st.Except {
		drop[b.r.key(b.r.ident(id))] = true
	}
	with := map[string]*ast.SelectItem{}
	for _, it := range st.Replace {
		with[b.r.key(b.r.ident(*it.Alias))] = it
	}
	var out []col
	for _, c := range cols {
		switch {
		case c.open == nil && drop[c.key]:
		case c.open == nil && with[c.key] != nil:
			out = append(out, b.newCol(c.name, b.exprSrcs(with[c.key].Expr, sc), c.from))
		default:
			out = append(out, c)
		}
	}
	return out
}

func (b *colBuilder) values(v *ast.Values, sc *colScope) []col {
	var cols []col
	for _, row := range v.Rows {
		for i, e := range row {
			if i >= maxCols {
				break
			}
			if i == len(cols) {
				cols = append(cols, b.newCol("column"+strconv.Itoa(i+1), nil, ""))
			}
			cols[i].srcs = union(cols[i].srcs, b.exprSrcs(e, sc))
		}
	}
	return b.fit(cols, "VALUES")
}

// setOpBranches lists the branches of a chain of set operations, left to
// right. The chain is walked without recursion down its left side, where a
// long chain grows.
func setOpBranches(x *ast.SetOp) []ast.QueryBody {
	var rights []ast.QueryBody
	var body ast.QueryBody = x
	for {
		op, ok := body.(*ast.SetOp)
		if !ok {
			break
		}
		rights = append(rights, op.Right)
		body = op.Left
	}
	out := []ast.QueryBody{body}
	for i := len(rights) - 1; i >= 0; i-- {
		if op, ok := rights[i].(*ast.SetOp); ok {
			out = append(out, setOpBranches(op)...)
		} else {
			out = append(out, rights[i])
		}
	}
	return out
}

// setOp matches the columns of a set operation's branches by position; the
// first branch names them. Past a column whose position is not known, such
// as a star over a table the catalog does not list, each column counts
// everything left in the other branches. All branches are merged at once,
// so a long chain takes linear time.
func (b *colBuilder) setOp(x *ast.SetOp, branches [][]col) []col {
	left := branches[0]
	same := true
	stop := len(left) // the first position the branches do not agree on
	for _, br := range branches[1:] {
		same = same && sameCols(left, br)
		for i := 0; i < min(stop, len(br)); i++ {
			if (left[i].open == nil) != (br[i].open == nil) {
				stop = i
			}
		}
	}
	if same {
		return left
	}
	b.work += len(left)
	out := make([]col, len(left))
	for i := range stop {
		l := left[i]
		if l.open == nil {
			var acc []int
			for _, br := range branches {
				if i < len(br) {
					acc = append(acc, br[i].srcs...)
				}
			}
			l.srcs = set(acc)
		} else {
			var opens []*openCols
			for _, br := range branches {
				if i < len(br) {
					opens = append(opens, br[i].open)
				}
			}
			l.open = mergeOpens(opens)
		}
		out[i] = l
	}
	if stop == len(left) {
		return b.fit(out, "a set operation")
	}
	op := [...]string{ast.Union: "UNION", ast.Intersect: "INTERSECT", ast.Except: "EXCEPT"}[x.Op]
	var rests [][]col
	var other []int
	for _, br := range branches {
		if stop < len(br) {
			rests = append(rests, br[stop:])
			if len(rests) > 1 {
				other = append(other, b.allSrcs(br[stop:])...)
			}
		}
	}
	b.diag("the branches of %s are matched column by column only up to column %d, because the columns of %s are not known; "+
		"each later column counts all the remaining columns of the other branches", op, stop, b.firstOpenLabel(rests...))
	other = set(other)
	for i := stop; i < len(left); i++ {
		l := left[i]
		if l.open != nil {
			o := *l.open
			o.srcs = union(o.srcs, other)
			l.open = &o
		} else {
			l.srcs = union(l.srcs, other)
		}
		out[i] = l
	}
	return b.fit(out, op)
}

// mergeOpens merges the open columns that branches have at one position.
func mergeOpens(opens []*openCols) *openCols {
	out := &openCols{}
	var srcs []int
	seen := map[*objRef]bool{}
	for _, o := range opens {
		srcs = append(srcs, o.srcs...)
		for _, obj := range o.objs {
			if !seen[obj] {
				seen[obj] = true
				out.objs = append(out.objs, obj)
			}
		}
		for _, l := range o.labels {
			out.labels, out.more = addLabel(out.labels, out.more, l)
		}
		out.more += o.more
	}
	out.srcs = set(srcs)
	return out
}

func (b *colBuilder) firstOpenLabel(cols ...[]col) string {
	for _, cs := range cols {
		for _, c := range cs {
			if c.open != nil {
				labels, more := openLabels([]*openCols{c.open})
				return strings.Join(labels, " and ") + strings.Repeat(" and more", min(more, 1))
			}
		}
	}
	return "a relation"
}

// rename gives the first columns the names of a column list, as a CTE's or
// an alias's column list does.
func (b *colBuilder) rename(cols []col, names []string, label string) []col {
	if len(names) == 0 {
		return cols
	}
	b.work += len(cols)
	out := slices.Clone(cols)
	for i, n := range names {
		if i == len(out) {
			break // more names than columns, which the engines reject
		}
		if out[i].open != nil {
			labels, _ := openLabels([]*openCols{out[i].open})
			b.diag("the column names given to %s are matched to its columns only up to column %d, because the columns of %s are not known; "+
				"each later name counts all the remaining columns", label, i, strings.Join(labels, " and "))
			rest := b.allSrcs(out[i:])
			var named []col
			for _, n := range names[i:] {
				named = append(named, b.newCol(n, rest, label))
			}
			return append(append(out[:i:i], named...), out[i:]...)
		}
		out[i].name, out[i].key = n, b.r.key(n)
	}
	return out
}

func (b *colBuilder) aliasNames(a *ast.Alias) []string {
	var names []string
	for _, c := range a.Columns {
		names = append(names, b.r.ident(c.Name))
	}
	return names
}

// ---------------------------------------------------------------------------
// FROM

// fromItem reads FROM item t, adds the relations a qualifier can name to sc,
// and returns the item as unqualified names see it.
func (b *colBuilder) fromItem(t ast.TableExpr, sc *colScope) *rel {
	switch x := t.(type) {
	case *ast.TableRef:
		rl := b.relation(x.Name, sc.ctes)
		if x.Alias != nil {
			rl = b.aliased(rl, x.Alias)
		}
		sc.bases = append(sc.bases, rl)
		return rl
	case *ast.SubqueryRef:
		outer := sc.parent
		if x.Lateral {
			outer = sc.lateral()
		}
		label := "subquery"
		if x.Alias != nil {
			label = b.r.ident(x.Alias.Name)
		}
		rl := &rel{label: label, cols: b.relabel(b.query(x.Query, outer, sc.ctes, nil), label)}
		if x.Alias != nil {
			rl = b.aliased(rl, x.Alias)
			sc.bases = append(sc.bases, rl)
		}
		return rl
	case *ast.FuncRef:
		rl := b.funcRel(x, sc.lateral())
		sc.bases = append(sc.bases, rl)
		return rl
	case *ast.Join:
		left := b.fromItem(x.Left, sc)
		sc.pending = append(sc.pending, left)
		right := b.fromItem(x.Right, sc)
		sc.pending = sc.pending[:len(sc.pending)-1]
		return b.join(x, left, right, sc)
	case *ast.Pivot, *ast.Unpivot: // the source's names are hidden behind the result
		n := len(sc.bases)
		var src *rel
		var alias *ast.Alias
		if pv, ok := x.(*ast.Pivot); ok {
			src, alias = b.fromItem(pv.Source, sc), pv.Alias
		} else {
			up := x.(*ast.Unpivot)
			src, alias = b.fromItem(up.Source, sc), up.Alias
		}
		sc.bases = slices.Clip(sc.bases[:n])
		rl := &rel{cols: b.pivoted(x, src, sc)}
		if alias != nil {
			rl = b.aliased(&rel{label: b.r.ident(alias.Name), cols: b.relabel(rl.cols, b.r.ident(alias.Name))}, alias)
		}
		sc.bases = append(sc.bases, rl)
		return rl
	case *ast.ParenTable:
		n := len(sc.bases)
		inner := b.fromItem(x.Expr, sc)
		if x.Alias == nil {
			return inner
		}
		// The alias hides the names inside.
		sc.bases = slices.Clip(sc.bases[:n])
		label := b.r.ident(x.Alias.Name)
		rl := b.aliased(&rel{label: label, cols: b.relabel(inner.cols, label)}, x.Alias)
		sc.bases = append(sc.bases, rl)
		return rl
	}
	panic(fmt.Sprintf("analyze: unexpected FROM item %T", t))
}

// lateral is the scope of a LATERAL subquery or a table function: the FROM
// items to its left, then the enclosing query.
func (sc *colScope) lateral() *colScope {
	return &colScope{
		parent: sc.parent,
		ctes:   sc.ctes,
		items:  append(slices.Clip(sc.items), sc.pending...),
		bases:  slices.Clip(sc.bases),
	}
}

// aliased is rl under an alias, with the alias's column names.
func (b *colBuilder) aliased(rl *rel, a *ast.Alias) *rel {
	out := &rel{name: b.r.key(b.r.ident(a.Name)), obj: rl.obj, label: rl.label, cols: rl.cols}
	if len(a.Columns) > 0 {
		out.cols = b.rename(rl.cols, b.aliasNames(a), b.r.ident(a.Name))
		out.obj = nil // the columns are known by their new names only
	}
	return out
}

// relation is the CTE or database object a relation name refers to.
func (b *colBuilder) relation(n *ast.Name, ctes *scope) *rel {
	if len(n.Parts) == 1 {
		if c, csc := b.finder.find(b.r.ident(n.Parts[0]), ctes); c != nil {
			return b.cteRel(c, csc)
		}
	}
	w, err := b.r.written(n)
	if err != nil {
		b.diag("%v", err)
	}
	return b.objectRel(b.object(w))
}

func (b *colBuilder) object(w catalog.Name) *objRef {
	full, obj, status := b.r.object(w)
	key := b.r.nameKey(full)
	if o := b.objects[key]; o != nil {
		return o
	}
	o := &objRef{name: full, key: key, obj: obj, status: status}
	if obj != nil && len(obj.Columns) > 0 {
		o.cols = make(map[string]string, len(obj.Columns))
		for _, c := range obj.Columns {
			if k := b.r.key(c.Name); o.cols[k] == "" {
				o.cols[k] = c.Name
			}
		}
	}
	b.objects[key] = o
	return o
}

// objectRel is a database object as a relation. With ExpandViews, a view's
// columns are those of its definition, so they resolve to the columns the
// view reads.
func (b *colBuilder) objectRel(o *objRef) *rel {
	if o.rel != nil {
		return o.rel
	}
	label := o.name.String()
	rl := &rel{name: b.r.key(o.name.Object), full: &o.name, obj: o, label: label}
	if o.obj != nil && o.obj.Definition != "" && b.o.ExpandViews {
		if b.expanding[o] {
			b.diag("view %s depends on itself", label)
			return b.plainRel(o, rl) // not kept: the expansion under way will finish it
		}
		if cols, ok := b.expand(o); ok {
			rl.cols = b.relabel(cols, label)
			o.rel = rl
			return rl
		}
	}
	o.rel = b.plainRel(o, rl)
	return o.rel
}

// plainRel gives rl the columns the catalog lists for o.
func (b *colBuilder) plainRel(o *objRef, rl *rel) *rel {
	if o.cols == nil {
		rl.cols = []col{{open: &openCols{objs: []*objRef{o}, labels: []string{rl.label}}}}
		return rl
	}
	b.work += len(o.obj.Columns)
	for _, c := range o.obj.Columns {
		rl.cols = append(rl.cols, b.newCol(c.Name, []int{b.objColumn(o, c.Name)}, rl.label))
	}
	return rl
}

// expand reads a view's definition. Its columns take the names the catalog
// lists for the view, when the two agree in number.
func (b *colBuilder) expand(o *objRef) ([]col, bool) {
	stmts, err := parser.Parse(o.obj.Definition, b.r.d)
	if err != nil {
		b.diag("the definition of view %s does not parse: %v", o.name, err)
		return nil, false
	}
	var q *ast.Query
	var names []ast.Ident
	if len(stmts) == 1 {
		switch s := stmts[0].(type) {
		case *ast.Query:
			q = s
		case *ast.CreateView:
			q, names = viewQuery(s), s.Columns
		}
	}
	if q == nil {
		b.diag("the definition of view %s is not one query or CREATE VIEW statement", o.name)
		return nil, false
	}
	b.expanding[o] = true
	cols := b.query(q, nil, nil, nil)
	delete(b.expanding, o)
	var list []string
	for _, n := range names {
		list = append(list, b.r.ident(n))
	}
	cols = b.rename(cols, list, o.name.String())
	if len(o.obj.Columns) > 0 && !slices.ContainsFunc(cols, func(c col) bool { return c.open != nil }) {
		if len(cols) != len(o.obj.Columns) {
			b.diag("the definition of view %s has %d columns, but the catalog lists %d", o.name, len(cols), len(o.obj.Columns))
		} else {
			list = list[:0]
			for _, c := range o.obj.Columns {
				list = append(list, c.Name)
			}
			cols = b.rename(cols, list, o.name.String())
		}
	}
	return cols, true
}

// cteRel is a CTE as a relation. Inside its own query, a recursive CTE is a
// stand-in whose columns resolve once the whole query has been read.
func (b *colBuilder) cteRel(c *ast.CTE, csc *scope) *rel {
	st := b.ctes[c]
	if st == nil {
		st = b.readCTE(c, csc)
	}
	if st.done {
		return st.rel
	}
	return b.standIn(st)
}

func (b *colBuilder) readCTE(c *ast.CTE, csc *scope) *cteState {
	st := &cteState{name: b.r.ident(c.Name), pendingSeen: map[pendingUse]bool{}}
	b.ctes[c] = st
	for _, id := range c.Columns {
		st.names = append(st.names, b.r.ident(id))
	}
	var cols []col
	if c.Query == nil {
		b.diag("data-modifying CTE %s is not analysed", st.name)
		cols = []col{{open: &openCols{labels: []string{st.name}}}}
	} else {
		cols = b.query(c.Query, b.outer[csc.with], csc, func(first []col) {
			if st.first == nil {
				st.first = b.rename(first, st.names, st.name)
			}
		})
		cols = b.rename(cols, st.names, st.name)
		cols = append(cols, b.searchCycle(c, st.name, cols)...)
	}
	b.finish(st, cols)
	return st
}

// standIn is a recursive CTE as its own query sees it: the columns of its
// first branch, each standing for whatever that column turns out to be
// computed from.
func (b *colBuilder) standIn(st *cteState) *rel {
	first := st.first
	if first == nil {
		for _, n := range st.names {
			first = append(first, b.newCol(n, nil, st.name))
		}
	}
	b.work += len(first)
	cols := make([]col, len(first))
	for i, c := range first {
		if c.open == nil {
			for len(st.stand) <= i {
				st.stand = append(st.stand, -1)
			}
			if st.stand[i] < 0 {
				st.stand[i] = b.newSource(source{self: st, index: i})
			}
			c.srcs = []int{st.stand[i]}
		}
		c.from = st.name
		cols[i] = c
	}
	return &rel{name: b.r.key(st.name), label: st.name, cols: cols}
}

// finish records a CTE's columns. Each stand-in in them is replaced by what
// its column is computed from, followed transitively.
func (b *colBuilder) finish(st *cteState, cols []col) {
	st.done = true
	if len(st.stand) == 0 { // not recursive
		st.rel = &rel{name: b.r.key(st.name), label: st.name, cols: b.relabel(cols, st.name)}
		return
	}
	n := len(cols)
	direct := make([][]int, n)
	deps := make([][]int, n)
	for i, c := range cols {
		srcs := c.srcs
		if c.open != nil {
			srcs = c.open.srcs
		}
		for _, id := range srcs {
			if s := b.srcs[id]; s.self == st {
				if s.index < n {
					deps[i] = append(deps[i], s.index)
				}
			} else {
				direct[i] = append(direct[i], id)
			}
		}
	}
	st.final = make([][]int, n)
	mark := make([]int, n) // i+1 once column i's search has reached it
	for i := range n {
		var acc []int
		stack := []int{i}
		mark[i] = i + 1
		for len(stack) > 0 {
			j := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			acc = append(acc, direct[j]...)
			for _, k := range deps[j] {
				if mark[k] != i+1 {
					mark[k] = i + 1
					stack = append(stack, k)
				}
			}
		}
		st.final[i] = set(acc)
	}
	b.work += n
	out := make([]col, n)
	for i, c := range cols {
		c.from = st.name
		if c.open != nil {
			o := *c.open
			o.srcs = st.final[i]
			c.open = &o
		} else {
			c.srcs = st.final[i]
		}
		out[i] = c
	}
	st.rel = &rel{name: b.r.key(st.name), label: st.name, cols: out}
	for _, p := range st.pending {
		b.emitOne(p.id, p.clause)
	}
	st.pending, st.pendingSeen = nil, nil
}

// searchCycle adds the columns of a CTE's SEARCH and CYCLE clauses, computed
// from the columns they name.
func (b *colBuilder) searchCycle(c *ast.CTE, name string, cols []col) []col {
	rl := &rel{label: name, cols: cols}
	srcsOf := func(ids []ast.Ident) []int {
		var out []int
		for _, id := range ids {
			n := b.r.ident(id)
			out = append(out, b.inRel(rl, n, n)...)
		}
		return set(out)
	}
	var out []col
	if s := c.Search; s != nil {
		out = append(out, b.newCol(b.r.ident(s.Set), srcsOf(s.By), name))
	}
	if cy := c.Cycle; cy != nil {
		srcs := srcsOf(cy.Columns)
		out = append(out, b.newCol(b.r.ident(cy.Set), srcs, name), b.newCol(b.r.ident(cy.Using), srcs, name))
	}
	return out
}

// funcRel is a table function as a relation. Every column it returns is
// computed from its arguments. Its columns are known by name only from a
// column list; a function that returns one value has a column named after
// the alias, or the function (PostgreSQL).
func (b *colBuilder) funcRel(x *ast.FuncRef, sc *colScope) *rel {
	calls := x.RowsFrom
	label := "ROWS FROM"
	if x.Func != nil {
		calls = []*ast.FuncCall{x.Func}
		label = b.r.ident(x.Func.Name.Last())
	}
	var srcs []int
	for _, f := range calls {
		srcs = append(srcs, b.callSrcs(f, sc)...)
	}
	if x.Expr != nil {
		srcs = append(srcs, b.exprSrcs(x.Expr, sc)...)
		label = b.outName(x.Expr)
	}
	srcs = set(srcs)
	name := label
	if x.Alias != nil {
		name = b.r.ident(x.Alias.Name)
	}
	rl := &rel{name: b.r.key(name), label: name}
	var names []string
	if x.Alias != nil {
		names = b.aliasNames(x.Alias)
	}
	if len(names) == 0 {
		for _, defs := range x.RowsFromColumns {
			for _, d := range defs {
				names = append(names, b.r.ident(d.Name))
			}
		}
	}
	// BigQuery's WITH OFFSET adds a column computed from nothing.
	var offset []col
	if x.Offset {
		n := "offset"
		if x.OffsetAlias != nil {
			n = b.r.ident(*x.OffsetAlias)
		}
		offset = []col{b.newCol(n, nil, name)}
	}
	if len(names) > 0 {
		for i, n := range names {
			s := srcs
			if x.Ordinality && i == len(names)-1 {
				s = nil
			}
			rl.cols = append(rl.cols, b.newCol(n, s, name))
		}
		rl.cols = append(rl.cols, offset...)
		return rl
	}
	rl.cols = append(append([]col{b.newCol(name, srcs, name)}, offset...), col{open: &openCols{srcs: srcs, labels: []string{label}}})
	if x.Ordinality {
		rl.cols = append(rl.cols, b.newCol("ordinality", nil, name))
	}
	return rl
}

// join reads a join's condition, whose columns count as used in a join, and
// returns the joined columns. Each column of USING or NATURAL is one column
// computed from both sides.
func (b *colBuilder) join(x *ast.Join, left, right *rel, sc *colScope) *rel {
	if x.On != nil {
		on := &colScope{parent: sc.parent, ctes: sc.ctes, items: []*rel{left, right}, bases: slices.Clip(sc.bases)}
		b.emit(b.exprSrcs(x.On, on), inJoin)
	}
	var names []string
	if x.Natural {
		names = b.common(left, right)
	} else {
		for _, id := range x.Using {
			names = append(names, b.r.ident(id))
		}
	}
	if len(names) == 0 {
		return &rel{label: "the join", cols: b.fit(b.concat(left.cols, right.cols), "a join")}
	}
	b.work += len(names) + len(left.cols) + len(right.cols)
	merged := make([]col, len(names))
	at := map[string]int{} // a merged column's position, by key
	for i, n := range names {
		l, r := b.inRel(left, n, n), b.inRel(right, n, n)
		b.emit(l, inJoin)
		b.emit(r, inJoin)
		merged[i] = b.newCol(n, union(l, r), "join")
		at[merged[i].key] = i
	}
	rest := func(cols []col, placed []bool) []col {
		var out []col
		for _, c := range cols {
			i, ok := at[c.key]
			switch {
			case c.open != nil || !ok:
				out = append(out, c)
			case placed != nil && !placed[i]:
				placed[i] = true
				out = append(out, merged[i])
			}
		}
		return out
	}
	var cols []col
	if b.r.d.Names.UsingColumnsFirst {
		cols = append(append(merged, rest(left.cols, nil)...), rest(right.cols, nil)...)
	} else {
		placed := make([]bool, len(merged))
		cols = rest(left.cols, placed)
		for i, p := range placed {
			if !p { // the left side's columns are not known
				cols = append(cols, merged[i])
			}
		}
		cols = append(cols, rest(right.cols, nil)...)
	}
	if x.UsingAlias != nil {
		alias := b.r.ident(*x.UsingAlias)
		sc.bases = append(sc.bases, &rel{name: b.r.key(alias), label: alias, cols: b.relabel(merged, alias)})
	}
	return &rel{label: "the join", cols: b.fit(cols, "a join")}
}

// common is the columns a NATURAL JOIN matches on: those both sides have.
func (b *colBuilder) common(left, right *rel) []string {
	for _, rl := range []*rel{left, right} {
		if slices.ContainsFunc(rl.cols, func(c col) bool { return c.open != nil }) {
			b.diag("the columns a NATURAL JOIN matches on are not known, because the columns of %s are not known", b.firstOpenLabel(rl.cols))
			return nil
		}
	}
	var names []string
	seen := map[string]bool{}
	for _, c := range left.cols {
		if c.key == "" || seen[c.key] {
			continue
		}
		if known, _ := b.lookup(right, c.key); len(known) > 0 {
			seen[c.key] = true
			names = append(names, c.name)
		}
	}
	return names
}

// ---------------------------------------------------------------------------
// Expressions

// exprSrcs is what expression e is computed from: the columns it refers to,
// and the output columns of the subqueries in it. EXISTS adds nothing, since
// it does not use what its subquery returns. Expressions are walked by
// reflection, so new expression types are covered without changes here.
func (b *colBuilder) exprSrcs(e any, sc *colScope) []int {
	var acc []int
	b.walk(reflect.ValueOf(e), sc, &acc)
	return set(acc)
}

func (b *colBuilder) walk(v reflect.Value, sc *colScope, acc *[]int) {
	switch v.Kind() {
	case reflect.Interface:
		if !v.IsNil() {
			b.walk(v.Elem(), sc, acc)
		}
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		switch x := v.Interface().(type) {
		case *ast.Name:
			*acc = append(*acc, b.column(x, sc)...)
		case *ast.Star:
			// A whole row, as in row_to_json(t.*). A bare * is count(*).
			if x.Qualifier != nil {
				qual := b.idents(x.Qualifier)
				if rl := b.relNamed(qual, sc); rl != nil {
					*acc = append(*acc, b.allSrcs(rl.cols)...)
				} else {
					b.diag("%s names no relation in scope", strings.Join(qual, "."))
				}
			}
		case *ast.Query:
			*acc = append(*acc, b.allSrcs(b.query(x, sc, sc.ctes, nil))...)
		case *ast.Exists:
		case *ast.TableArg: // TABLE t passed to a table-valued function reads all of t
			switch {
			case x.Func != nil: // TABLE f(args): what f's arguments read
				*acc = append(*acc, b.callSrcs(x.Func, sc)...)
			case x.Kind == "TABLE":
				*acc = append(*acc, b.allSrcs(b.relation(x.Name, sc.ctes).cols)...)
			}
		case *ast.FuncCall:
			*acc = append(*acc, b.callSrcs(x, sc)...)
		case *ast.WindowSpec:
			b.window(x, sc, acc)
		case *ast.In:
			b.walk(reflect.ValueOf(x.Expr), sc, acc)
			b.walk(reflect.ValueOf(x.List), sc, acc)
			b.walk(reflect.ValueOf(x.Subquery), sc, acc)
			if x.Table != nil { // SQLite: x IN table
				*acc = append(*acc, b.allSrcs(b.relation(x.Table, sc.ctes).cols)...)
			}
			if x.Func != nil {
				*acc = append(*acc, b.callSrcs(x.Func, sc)...)
			}
		case *ast.Collate:
			b.walk(reflect.ValueOf(x.Expr), sc, acc)
		default:
			b.walk(v.Elem(), sc, acc)
		}
	case reflect.Struct:
		t := v.Type()
		for i := range v.NumField() {
			if t.Field(i).IsExported() {
				b.walk(v.Field(i), sc, acc)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			b.walk(v.Index(i), sc, acc)
		}
	}
}

// callSrcs is what a function call is computed from: its arguments,
// including ORDER BY, FILTER and the window it runs over, but not its name.
func (b *colBuilder) callSrcs(f *ast.FuncCall, sc *colScope) []int {
	if f.RawArgs != nil && strings.TrimSpace(*f.RawArgs) != "" {
		b.diag("the arguments of %s are kept as text, so the columns in them are not listed", strings.Join(b.idents(f.Name), "."))
	}
	var acc []int
	for _, a := range f.Args {
		if st, ok := a.(*ast.Star); ok && st.Qualifier == nil {
			continue // count(*)
		}
		b.walk(reflect.ValueOf(a), sc, &acc)
	}
	b.walk(reflect.ValueOf(f.OrderBy), sc, &acc)
	b.walk(reflect.ValueOf(f.Receiver), sc, &acc) // x in x.F(args)
	b.walk(reflect.ValueOf(f.Where), sc, &acc)    // BigQuery's f(x WHERE cond)
	b.walk(reflect.ValueOf(f.Having), sc, &acc)   // HAVING MAX|MIN picks the rows the result comes from
	b.walk(reflect.ValueOf(f.Limit), sc, &acc)
	b.walk(reflect.ValueOf(f.WithinGroup), sc, &acc)
	b.walk(reflect.ValueOf(f.Filter), sc, &acc)
	b.walk(reflect.ValueOf(f.Over), sc, &acc)
	return acc
}

// window adds the columns a window partitions and orders by, including
// those of the named window it builds on.
func (b *colBuilder) window(w *ast.WindowSpec, sc *colScope, acc *[]int) {
	if w.Name != nil {
		key := b.r.key(b.r.ident(*w.Name))
		for _, nw := range sc.windows {
			// A window that names itself, which the engines reject, is read once.
			if b.r.key(b.r.ident(nw.Name)) == key && !b.inWindow[nw] {
				b.inWindow[nw] = true
				b.window(nw.Spec, sc, acc)
				delete(b.inWindow, nw)
				break
			}
		}
	}
	b.walk(reflect.ValueOf(w.PartitionBy), sc, acc)
	b.walk(reflect.ValueOf(w.OrderBy), sc, acc)
	b.walk(reflect.ValueOf(w.Frame), sc, acc)
}

// ---------------------------------------------------------------------------
// Output names

// outName is the name of an output column without an alias, or "" when
// analysis cannot tell it.
func (b *colBuilder) outName(e ast.Expr) string {
	if n, ok := e.(*ast.Name); ok {
		return b.r.ident(n.Last())
	}
	if !b.r.d.Names.NamedExprs {
		return ""
	}
	if name, _ := b.figure(e); name != "" {
		return name
	}
	return "?column?"
}

// figure follows PostgreSQL's FigureColnameInternal: it returns the name an
// expression suggests, and how strongly (2 for a name, 1 for a fallback
// such as a type name).
func (b *colBuilder) figure(e ast.Expr) (string, int) {
	switch x := e.(type) {
	case *ast.Name:
		return b.r.ident(x.Last()), 2
	case *ast.Paren:
		return b.figure(x.Expr)
	case *ast.Collate:
		return b.figure(x.Expr)
	case *ast.Subscript:
		return b.figure(x.Expr)
	case *ast.Field:
		if x.Star {
			return b.figure(x.Expr)
		}
		return b.r.ident(x.Name), 2
	case *ast.FuncCall:
		return b.r.ident(x.Name.Last()), 2
	case *ast.Cast:
		if name, strength := b.figure(x.Expr); strength > 1 {
			return name, strength
		}
		return pgTypeName(x.Type), 1
	case *ast.TypedString:
		return pgTypeName(x.Type), 1
	case *ast.Case:
		if name, strength := b.figure(x.Else); strength > 1 {
			return name, strength
		}
		return "case", 1
	case *ast.Exists:
		return "exists", 2
	case *ast.Array:
		return "array", 2
	case *ast.Tuple:
		return "row", 2
	case *ast.Subquery:
		if name := b.firstName(x.Query.Body); name != "" {
			return name, 2
		}
	case *ast.Extract:
		return "extract", 2
	case *ast.Substring:
		return "substring", 2
	case *ast.Position:
		return "position", 2
	case *ast.Overlay:
		return "overlay", 2
	case *ast.AtTimeZone:
		return "timezone", 2
	case *ast.Trim:
		switch x.Where {
		case "LEADING":
			return "ltrim", 2
		case "TRAILING":
			return "rtrim", 2
		}
		return "btrim", 2
	}
	return "", 0
}

// firstName is the name of a scalar subquery's column.
func (b *colBuilder) firstName(body ast.QueryBody) string {
	switch x := body.(type) {
	case *ast.Select:
		if len(x.Items) == 0 {
			return ""
		}
		if it := x.Items[0]; it.Alias != nil {
			return b.r.ident(*it.Alias)
		} else if _, star := it.Expr.(*ast.Star); !star {
			return b.outName(it.Expr)
		}
	case *ast.SetOp:
		return b.firstName(x.Left)
	case *ast.ParenQuery:
		return b.firstName(x.Query.Body)
	}
	return ""
}

// pgTypeNames are the names PostgreSQL's grammar gives the SQL-standard
// types, as pg_query shows them.
var pgTypeNames = map[string]string{
	"int": "int4", "integer": "int4", "smallint": "int2", "bigint": "int8",
	"real": "float4", "double precision": "float8", "float": "float8",
	"decimal": "numeric", "dec": "numeric", "boolean": "bool",
	"char": "bpchar", "character": "bpchar", "nchar": "bpchar",
	"national char": "bpchar", "national character": "bpchar",
	"char varying": "varchar", "character varying": "varchar", "nchar varying": "varchar",
	"national char varying": "varchar", "national character varying": "varchar",
	"bit varying": "varbit",
}

// pgTypeName is the last part of the name PostgreSQL gives a type.
func pgTypeName(t *ast.DataType) string {
	name, quoted := lastNamePart(t.Name)
	if quoted {
		return name
	}
	name = strings.Join(strings.Fields(asciiLower(name)), " ")
	switch name {
	case "timestamp", "time":
		if t.TimeZone == ast.TZWith {
			return name + "tz"
		}
		return name
	case "float":
		if len(t.Args) == 1 {
			if l, ok := t.Args[0].(*ast.Literal); ok {
				if p, err := strconv.Atoi(l.Value); err == nil && p <= 24 {
					return "float4"
				}
			}
		}
	}
	if pg, ok := pgTypeNames[name]; ok {
		return pg
	}
	return name
}

// lastNamePart returns the last dotted part of a name as written, without
// its quotes, and whether it was quoted.
func lastNamePart(s string) (string, bool) {
	start, quoted := 0, false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			quoted = !quoted // a doubled quote inside a name flips twice
		case s[i] == '.' && !quoted:
			start = i + 1
		}
	}
	part := s[start:]
	if len(part) >= 2 && part[0] == '"' && part[len(part)-1] == '"' {
		return strings.ReplaceAll(part[1:len(part)-1], `""`, `"`), true
	}
	return part, false
}
